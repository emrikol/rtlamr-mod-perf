// RTLAMR - An rtl-sdr receiver for smart meters operating in the 900MHz ISM band.
// Copyright (C) 2015 Douglas Hall
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package protocol

import (
	"encoding/binary"
	"log"
	"math"
	"math/bits"
	"strings"
)

const (
	fixedIDMPreambleASCII  = "01010101010101010001011010100011"
	fixedR900PreambleASCII = "00000000000000001110010101100100"
)

// PacketConfig specifies packet-specific radio configuration.
type PacketConfig struct {
	Protocol string
	Preamble string

	DataRate int

	BlockSize, BlockSize2    int
	ChipLength, SymbolLength int
	SampleRate               int

	PreambleSymbols, PacketSymbols int
	PreambleLength, PacketLength   int

	BufferLength int
	CenterFreq   uint32
}

func (d Decoder) Log() {
	log.Println("CenterFreq:", d.Cfg.CenterFreq)
	log.Println("SampleRate:", d.Cfg.SampleRate)
	log.Println("DataRate:", d.Cfg.DataRate)
	log.Println("ChipLength:", d.Cfg.ChipLength)
	log.Println("PreambleSymbols:", d.Cfg.PreambleSymbols)
	log.Println("PreambleLength:", d.Cfg.PreambleLength)
	log.Println("PacketSymbols:", d.Cfg.PacketSymbols)
	log.Println("PacketLength:", d.Cfg.PacketLength)

	var preambles []string
	for preamble, _ := range d.preambleStrs {
		preambles = append(preambles, preamble)
	}

	log.Println("Protocols:", strings.Join(d.protocols, ","))
	log.Println("Preambles:", strings.Join(preambles, ","))
	status := d.DispatchStatus()
	log.Println("DecoderImplementation:", status.Implementation)
	log.Println("Power16Active:", status.Power16Active)
	if status.FallbackReason != "" {
		log.Println("Power16FallbackReason:", status.FallbackReason)
	}
}

// Decoder contains buffers and radio configuration.
type Decoder struct {
	Cfg PacketConfig

	Signal    []float64
	Quantized []byte

	demod                Demodulator
	filterOutput         []byte
	filterScratch        []byte
	quantizedBacking     []byte
	quantizedStart       int
	signalHistoryOverlap int
	signalBacking        []float64
	signalBlock          int
	signalBlockCount     int
	signalBlockStride    int
	signalBlockShift     int
	signalBlockMask      int
	signalGap            int

	power16Policy            power16Policy
	power16Probe             power16PlatformProbe
	power16                  *power16State
	power16ParserCount       int
	power16ParsersCompatible bool
	power16Consumers         []Power16HistoryConsumer
	dispatchStatus           DecoderDispatchStatus

	preambleStrs                              map[string]bool
	preambles                                 map[string][]Parser
	protocols                                 []string
	fixedIDMParsers, fixedR900Parsers         []Parser
	fixedIDMPreambleKey, fixedR900PreambleKey string

	pkt []byte

	packed       []byte
	alignedMasks []byte
	sIdxA, sIdxB []int
}

func NewDecoder() Decoder {
	return newDecoder(power16PolicyAutomatic, probePower16Platform)
}

func newDecoder(policy power16Policy, probe power16PlatformProbe) Decoder {
	return Decoder{
		preambles:                make(map[string][]Parser),
		preambleStrs:             make(map[string]bool),
		power16Policy:            policy,
		power16Probe:             probe,
		power16ParsersCompatible: true,
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Using a single decoder, register protocols to pass off decoded packets to.
func (d *Decoder) RegisterProtocol(p Parser) {
	// Protocols such as R900 require the use of internal decoder data for further processing.
	p.SetDecoder(d)
	d.power16ParserCount++
	compatible, ok := p.(Power16Compatible)
	if !ok || !compatible.Power16Compatible() {
		d.power16ParsersCompatible = false
	}
	consumer, consumesPower16 := p.(Power16HistoryConsumer)
	if consumesPower16 {
		d.power16Consumers = append(d.power16Consumers, consumer)
	}
	if historyParser, ok := p.(interface{ SignalHistoryOverlap() int }); ok {
		overlap := historyParser.SignalHistoryOverlap()
		if overlap > d.signalHistoryOverlap {
			d.signalHistoryOverlap = overlap
		}
		if overlap > 0 && !consumesPower16 {
			d.power16ParsersCompatible = false
		}
	}

	// Take the largest value for each protocol. Choosing the highest registered
	// center frequency makes mixed-protocol defaults independent of registration
	// order. Callers may still explicitly override the resulting center.
	if center := p.Cfg().CenterFreq; center > d.Cfg.CenterFreq {
		d.Cfg.CenterFreq = center
	}
	d.Cfg.DataRate = max(d.Cfg.DataRate, p.Cfg().DataRate)
	d.Cfg.ChipLength = max(d.Cfg.ChipLength, p.Cfg().ChipLength)
	d.Cfg.PreambleSymbols = max(d.Cfg.PreambleSymbols, p.Cfg().PreambleSymbols)
	d.Cfg.PacketSymbols = max(d.Cfg.PacketSymbols, p.Cfg().PacketSymbols)

	// Take a string of ascii 0's and 1's, convert them to numerical 0's and 1's.
	// This is used during preamble searching.
	preambleBytes := make([]byte, len(p.Cfg().Preamble))
	for idx, bit := range p.Cfg().Preamble {
		if bit == '1' {
			preambleBytes[idx] = 1
		}
	}

	// Keep track of registered preambles for logging back to the user.
	d.preambleStrs[p.Cfg().Preamble] = true

	// Associate the parser with the appropriate preamble. Cache the two fixed
	// 32-symbol families so the A72 can search them together without scanning
	// or classifying disabled protocols in the hot path.
	preambleKey := string(preambleBytes)
	d.preambles[preambleKey] = append(d.preambles[preambleKey], p)
	switch p.Cfg().Preamble {
	case fixedIDMPreambleASCII:
		d.fixedIDMParsers = d.preambles[preambleKey]
		d.fixedIDMPreambleKey = preambleKey
	case fixedR900PreambleASCII:
		d.fixedR900Parsers = d.preambles[preambleKey]
		d.fixedR900PreambleKey = preambleKey
	}

	// Add the protocol to the list for logging back to the user.
	d.protocols = append(d.protocols, p.Cfg().Protocol)
}

// Calculate lengths and allocate internal buffers.
func (d *Decoder) Allocate() {
	d.detachPower16Consumers()
	d.power16 = nil
	d.Cfg.SymbolLength = d.Cfg.ChipLength << 1
	d.Cfg.SampleRate = d.Cfg.DataRate * d.Cfg.ChipLength

	d.Cfg.PreambleLength = d.Cfg.PreambleSymbols * d.Cfg.SymbolLength
	d.Cfg.PacketLength = d.Cfg.PacketSymbols * d.Cfg.SymbolLength

	d.Cfg.BlockSize = NextPowerOf2(d.Cfg.PreambleLength)
	d.Cfg.BlockSize2 = d.Cfg.BlockSize << 1

	d.Cfg.BufferLength = d.Cfg.PacketLength + d.Cfg.BlockSize

	// Allocate one representation at an allocation-time boundary. Automatic
	// Power16 selection still requires every parser, geometry, CPU, feature,
	// self-test, and kill-switch gate to pass.
	if platform, ok := d.selectPower16Platform(); ok {
		d.allocatePower16(platform)
		d.attachPower16Consumers()
	} else {
		d.allocateFloatSignal()
	}
	d.quantizedBacking = nil
	if d.power16 != nil && d.power16.runPacked != nil {
		d.quantizedBacking = make([]byte, d.Cfg.BufferLength+d.Cfg.BlockSize)
		// Do not expose the direct-write extension through Quantized's capacity.
		d.Quantized = d.quantizedBacking[:d.Cfg.BufferLength:d.Cfg.BufferLength]
		// The extension is also the fallback scratch if a caller replaces the
		// exported Quantized slice. Direct and fallback writes are exclusive.
		d.filterScratch = d.quantizedBacking[d.Cfg.BufferLength:]
	} else {
		d.Quantized = make([]byte, d.Cfg.BufferLength)
		d.filterScratch = make([]byte, d.Cfg.BlockSize)
	}
	d.filterOutput = d.filterScratch

	// Signal up to the final stage is 1-bit per byte. Allocate a buffer to
	// store packed version 8-bits per byte.
	d.pkt = make([]byte, (d.Cfg.PacketSymbols+7)>>3)

	d.sIdxA = make([]int, 0, d.Cfg.BlockSize)
	d.sIdxB = make([]int, 0, d.Cfg.BlockSize)

	if d.power16 != nil && d.power16.runPacked != nil {
		d.packed = d.power16.packedSearchWindow()
	} else {
		d.packed = make([]byte, (d.Cfg.BlockSize+d.Cfg.PreambleLength+7)>>3)
	}

	return
}

// Decode accepts a sample block and returns the messages it contains.
func (d *Decoder) Decode(input []byte) (messages []Message) {
	if d.power16 != nil {
		if d.appendPower16AndFilter(input) {
			return d.decodeQuantized()
		}
	} else {
		d.appendSignal(input)

		// Perform matched filter on new block.
		d.Filter(d.Signal, d.filterOutput)
	}
	return d.decodeFiltered(d.filterOutput)
}

// decodeFiltered advances the decision history and services every registered
// parser. Keeping this representation-independent tail in one place preserves
// parser object identity and ordering when another exact producer is wired in.
func (d *Decoder) decodeFiltered(block []byte) (messages []Message) {
	d.appendQuantized(block)
	if d.power16 == nil || d.power16.runPacked == nil {
		packQuantizedRing(d.packed, d.Quantized, d.quantizedStart)
	}
	return d.decodeQuantized()
}

// decodeQuantized services the common search/parser tail after the decision
// history has already advanced. The packed Power16 producer uses this entry
// point to avoid copying its contiguous output into the same ring afterward.
func (d *Decoder) decodeQuantized() (messages []Message) {
	dualFixed := false
	if len(d.fixedIDMParsers) != 0 && len(d.fixedR900Parsers) != 0 {
		d.sIdxA, d.sIdxB, dualFixed = searchAlignedCandidates32DualFixedPlatform(d.packed, d.Cfg.SymbolLength>>3, d.sIdxA[:0], d.sIdxB[:0])
		if dualFixed {
			messages = d.parseCandidates(d.sIdxA, d.fixedIDMParsers, messages)
			messages = d.parseCandidates(d.sIdxB, d.fixedR900Parsers, messages)
			if len(d.preambles) == 2 {
				return messages
			}
		}
	}

	for preamble, parsers := range d.preambles {
		if dualFixed && (preamble == d.fixedIDMPreambleKey || preamble == d.fixedR900PreambleKey) {
			continue
		}
		messages = d.parseCandidates(d.searchPacked([]byte(preamble)), parsers, messages)
	}

	return messages
}

// parseCandidates keeps the existing packet representation for ordinary and
// external parsers, while allowing index-only parsers to skip packet slicing
// and byte-only parsers to skip bit-string construction. Parser order remains
// unchanged; a shared packet projection is materialized lazily at most once
// per preamble.
func (d Decoder) parseCandidates(indices []int, parsers []Parser, messages []Message) []Message {
	var packets []Data
	materialized := false
	includeBits := false
	for _, parser := range parsers {
		if _, ok := parser.(CandidateIndexParser); ok {
			continue
		}
		bytesOnly, ok := parser.(PacketBytesOnly)
		if !ok || !bytesOnly.PacketBytesOnly() {
			includeBits = true
			break
		}
	}
	for _, parser := range parsers {
		if indexParser, ok := parser.(CandidateIndexParser); ok {
			messages = indexParser.ParseCandidateIndices(indices, messages)
			continue
		}
		if !materialized {
			packets = d.slice(indices, includeBits)
			materialized = true
		}
		messages = parser.Parse(packets, messages)
	}
	return messages
}

// appendSignal computes a magnitude block and retains the full history only
// when a registered parser needs it. Signal remains the same contiguous short
// filter window in either mode.
func (d *Decoder) appendSignal(input []byte) {
	if len(d.signalBacking) == 0 {
		copy(d.Signal, d.Signal[d.Cfg.BlockSize:])
		d.demod.Execute(input, d.Signal[d.Cfg.SymbolLength:])
		return
	}

	previousStart := d.signalBlock * d.signalBlockStride
	d.signalBlock++
	if d.signalBlock == d.signalBlockCount {
		d.signalBlock = 0
	}
	currentStart := d.signalBlock * d.signalBlockStride
	overlap := d.signalHistoryOverlap
	copy(d.signalBacking[currentStart:currentStart+overlap], d.signalBacking[previousStart+d.Cfg.BlockSize:previousStart+d.Cfg.BlockSize+overlap])
	d.demod.Execute(input, d.signalBacking[currentStart+overlap:currentStart+d.signalBlockStride])
	d.Signal = d.signalBacking[currentStart+overlap-d.Cfg.SymbolLength : currentStart+d.signalBlockStride]
}

// SignalAt returns a magnitude sample by logical position in the retained
// signal history. It is available only to parsers that request that history.
func (d Decoder) SignalAt(idx int) float64 {
	physicalIdx := d.signalGap + idx
	blockOffset := physicalIdx >> uint(d.signalBlockShift)
	sampleOffset := physicalIdx & d.signalBlockMask
	blockIdx := d.signalBlock + 1 + blockOffset
	if blockIdx >= d.signalBlockCount {
		blockIdx -= d.signalBlockCount
	}
	return d.signalBacking[blockIdx*d.signalBlockStride+d.signalHistoryOverlap+sampleOffset]
}

// SignalWindow returns a contiguous range from the retained magnitude
// history. Each block carries enough overlap from its predecessor to make a
// parser's complete correlation window contiguous across block boundaries.
func (d Decoder) SignalWindow(idx, length int) []float64 {
	if length > d.signalHistoryOverlap {
		panic("protocol: requested signal window exceeds retained overlap")
	}
	physicalIdx := d.signalGap + idx
	blockOffset := physicalIdx >> uint(d.signalBlockShift)
	sampleOffset := physicalIdx & d.signalBlockMask
	blockIdx := d.signalBlock + 1 + blockOffset
	if blockIdx >= d.signalBlockCount {
		blockIdx -= d.signalBlockCount
	}

	start := blockIdx*d.signalBlockStride + d.signalHistoryOverlap + sampleOffset
	if sampleOffset+length > d.Cfg.BlockSize {
		beforeBoundary := d.Cfg.BlockSize - sampleOffset
		blockIdx++
		if blockIdx == d.signalBlockCount {
			blockIdx = 0
		}
		start = blockIdx*d.signalBlockStride + d.signalHistoryOverlap - beforeBoundary
	}
	return d.signalBacking[start : start+length]
}

func (d *Decoder) allocateSignalHistory() {
	d.signalBlockStride = d.Cfg.BlockSize + d.signalHistoryOverlap
	d.signalBlockCount = (d.Cfg.BufferLength + d.Cfg.BlockSize - 1) / d.Cfg.BlockSize
	d.signalBacking = make([]float64, d.signalBlockCount*d.signalBlockStride)
	d.signalBlock = d.signalBlockCount - 1
	d.signalBlockShift = bits.TrailingZeros(uint(d.Cfg.BlockSize))
	d.signalBlockMask = d.Cfg.BlockSize - 1
	d.signalGap = d.signalBlockCount*d.Cfg.BlockSize - d.Cfg.BufferLength
	start := d.signalBlock * d.signalBlockStride
	d.Signal = d.signalBacking[start+d.signalHistoryOverlap-d.Cfg.SymbolLength : start+d.signalBlockStride]
}

func (d *Decoder) allocateFloatSignal() {
	d.Signal = nil
	d.signalBacking = nil
	if d.signalHistoryOverlap > 0 {
		d.allocateSignalHistory()
	} else {
		d.Signal = make([]float64, d.Cfg.BlockSize+d.Cfg.SymbolLength)
	}
	// Calculate magnitude lookup table specified by -fastmag flag.
	d.demod = NewMagLUT()
}

// A Demodulator knows how to demodulate an array of uint8 IQ samples into an
// array of float64 samples.
type Demodulator interface {
	Execute([]byte, []float64)
}

// Default Magnitude Lookup Table
type MagLUT []float64

// Pre-computes normalized squares with most common DC offset for rtl-sdr dongles.
func NewMagLUT() (lut MagLUT) {
	lut = make([]float64, 0x100)
	for idx := range lut {
		lut[idx] = (127.5 - float64(idx)) / 127.5
		lut[idx] *= lut[idx]
	}
	return
}

// Calculates complex magnitude on given IQ stream writing result to output.
func (lut MagLUT) Execute(input []byte, output []float64) {
	if len(output) == 0 {
		return
	}
	// Prove the production table and input lengths once instead of checking
	// both lookup operands for every complex sample.
	_ = lut[255]
	_ = input[len(output)*2-1]

	if magnitudeLUTA72Available() {
		bulk := len(output) &^ 7
		if bulk != 0 {
			magnitudeLUTA72Platform(output[:bulk], input[:bulk*2], lut)
			input = input[bulk*2:]
			output = output[bulk:]
		}
	}
	magnitudeLUTGo(input, output, lut)
}

// Keep the portable loop out of callers: Go 1.26 otherwise loses the bounds
// proofs above the loop and restores checks for each sample.
//
//go:noinline
func magnitudeLUTGo(input []byte, output []float64, lut []float64) {
	if len(output) == 0 {
		return
	}
	_ = lut[255]
	_ = input[len(output)*2-1]
	i := 0
	for idx := range output {
		output[idx] = lut[input[i]] + lut[input[i+1]]
		i += 2
	}
}

// Matched filter for Manchester coded signals. Output signal's sign at each
// sample determines the bit-value due to Manchester symbol odd symmetry.
func filterManchester(output []byte, lowerInput, middleInput, upperInput []float64, lower, upper float64) {
	n := len(output)
	if len(lowerInput) < n || len(middleInput) < n || len(upperInput) < n {
		panic("protocol: Manchester filter input shorter than output")
	}
	for idx := range output {
		f := lower - upper
		output[idx] = 1 - byte(math.Float64bits(f)>>63)
		lower += middleInput[idx] - lowerInput[idx]
		upper += upperInput[idx] - middleInput[idx]
	}
}

func (d Decoder) Filter(input []float64, output []byte) {
	chipLength := d.Cfg.ChipLength
	if filterManchesterA72Platform(input, output, chipLength) {
		return
	}
	var lower, upper float64
	for idx := 0; idx < chipLength; idx++ {
		lower += input[idx]
		upper += input[idx+chipLength]
	}

	// Advance the two chip windows by one sample after each decision.
	n := len(output)
	filterManchester(output, input[:n], input[chipLength:chipLength+n], input[chipLength*2:chipLength*2+n], lower, upper)

	return
}

func (d *Decoder) ownsQuantizedRing() bool {
	return len(d.Quantized) == d.Cfg.BufferLength && len(d.Quantized) != 0 &&
		len(d.quantizedBacking) == d.Cfg.BufferLength+d.Cfg.BlockSize &&
		&d.Quantized[0] == &d.quantizedBacking[0]
}

// nextQuantizedOutput advances the logical ring position and returns one
// contiguous block-sized destination. quantizedBacking extends the physical
// ring by one block so a cross-boundary producer write never needs a split ABI.
func (d *Decoder) nextQuantizedOutput() (output []byte, nextStart, tail int) {
	nextStart = d.quantizedStart + d.Cfg.BlockSize
	if nextStart >= len(d.Quantized) {
		nextStart -= len(d.Quantized)
	}
	tail = nextStart + d.Cfg.PacketLength
	if tail >= len(d.Quantized) {
		tail -= len(d.Quantized)
	}
	return d.quantizedBacking[tail : tail+d.Cfg.BlockSize], nextStart, tail
}

// commitQuantizedOutput restores only the wrapped physical prefix. Ordinary
// in-ring producer writes need no copy at all.
func (d *Decoder) commitQuantizedOutput(nextStart, tail, count int) {
	if end := tail + count; end > len(d.Quantized) {
		overflow := end - len(d.Quantized)
		copy(d.Quantized[:overflow], d.quantizedBacking[len(d.Quantized):end])
	}
	d.quantizedStart = nextStart
}

// appendQuantized advances the logical decision history and writes only the
// newly filtered block. Both lengths are multiples of eight for every decoder
// configuration, preserving byte alignment for packed preamble searches.
func (d *Decoder) appendQuantized(block []byte) {
	d.quantizedStart += d.Cfg.BlockSize
	if d.quantizedStart >= len(d.Quantized) {
		d.quantizedStart -= len(d.Quantized)
	}

	tail := d.quantizedStart + d.Cfg.PacketLength
	if tail >= len(d.Quantized) {
		tail -= len(d.Quantized)
	}
	first := len(block)
	if remaining := len(d.Quantized) - tail; remaining < first {
		first = remaining
	}
	copy(d.Quantized[tail:], block[:first])
	copy(d.Quantized, block[first:])
}

func (d Decoder) quantizedAt(idx int) byte {
	idx += d.quantizedStart
	if idx >= len(d.Quantized) {
		idx -= len(d.Quantized)
	}
	return d.Quantized[idx]
}

// Return a list of indices into the quantized signal at which a valid preamble
// exists.
//  1. Pack the quantized signal into bytes.
//  2. Build a list of indices by eliminating bytes that contain no bits matching
//     the first bit of the preamble.
//  3. Continue eliminating indices at which the preamble cannot exist.
//  4. Convert indices from byte-based to sample-based.
//  5. Check each of these indices for the preamble.
func (d *Decoder) Search(preamble []byte) []int {
	// Pack the bit-wise quantized signal into bytes.
	packQuantizedRing(d.packed, d.Quantized, d.quantizedStart)
	return d.searchPacked(preamble)
}

func (d *Decoder) searchPacked(preamble []byte) []int {
	if d.Cfg.SymbolLength&7 == 0 {
		return d.searchPackedByteAligned(preamble)
	}
	return d.searchPackedLegacy(preamble)
}

// searchPackedByteAligned tests all eight sample phases represented by a
// packed byte at once. Symbol alignment keeps every preamble decision at the
// same bit position in its respective byte.
func (d *Decoder) searchPackedByteAligned(preamble []byte) []int {
	symLenByte := d.Cfg.SymbolLength >> 3
	d.sIdxA = d.sIdxA[:0]
	if len(preamble) < 4 {
		return d.searchPackedByteAlignedGeneric(preamble, symLenByte)
	}
	blockBytes := d.Cfg.BlockSize >> 3
	if len(preamble) == 32 && blockBytes&15 == 0 {
		if len(d.alignedMasks) != blockBytes {
			d.alignedMasks = make([]byte, blockBytes)
		}
		if fixedIndices, ok := searchAlignedCandidates32FixedPlatform(preamble, d.alignedMasks, d.packed, symLenByte, d.sIdxA); ok {
			d.sIdxA = fixedIndices
			return d.sIdxA
		}
	}
	if len(preamble) == 32 && blockBytes&15 == 0 && searchAlignedCandidates4Available() {
		if len(d.alignedMasks) != blockBytes {
			d.alignedMasks = make([]byte, blockBytes)
		}
		var masks [32]byte
		for idx, pBit := range preamble {
			masks[idx] = (pBit ^ 1) * 0xff
		}
		d.sIdxA = searchAlignedCandidates32Platform(d.alignedMasks, d.packed, symLenByte, masks[:], d.sIdxA)
		return d.sIdxA
	}
	if (len(preamble) == 16 || len(preamble) == 21) && blockBytes&63 == 0 {
		if len(d.alignedMasks) != blockBytes {
			d.alignedMasks = make([]byte, blockBytes)
		}
		if fixedIndices, ok := searchAlignedCandidates4FixedPlatform(preamble, d.alignedMasks, d.packed, symLenByte, d.sIdxA); ok {
			d.sIdxA = fixedIndices
			return d.sIdxA
		}
	}
	m0 := (preamble[0] ^ 1) * 0xff
	m1 := (preamble[1] ^ 1) * 0xff
	m2 := (preamble[2] ^ 1) * 0xff
	m3 := (preamble[3] ^ 1) * 0xff
	if blockBytes&15 == 0 && searchAlignedCandidates4Available() {
		if len(d.alignedMasks) != blockBytes {
			d.alignedMasks = make([]byte, blockBytes)
		}
		masks := [4]byte{m0, m1, m2, m3}
		searchAlignedCandidates4Platform(d.alignedMasks, d.packed, symLenByte, masks)
		return d.finishAlignedCandidates(preamble, symLenByte, d.alignedMasks)
	}

	for qByte := 0; qByte < blockBytes; qByte++ {
		candidates := (d.packed[qByte] ^ m0) &
			(d.packed[qByte+symLenByte] ^ m1) &
			(d.packed[qByte+symLenByte*2] ^ m2) &
			(d.packed[qByte+symLenByte*3] ^ m3)
		if candidates != 0 {
			for relativeIdx, pBit := range preamble[4:] {
				pIdx := relativeIdx + 4
				signal := d.packed[qByte+pIdx*symLenByte]
				candidates &= signal ^ ((pBit ^ 1) * 0xff)
				if candidates == 0 {
					break
				}
			}
		}

		// Packed decisions are MSB-first, so leading-zero order preserves the
		// sample-index order returned by the legacy search.
		for candidates != 0 {
			phase := bits.LeadingZeros8(candidates)
			d.sIdxA = append(d.sIdxA, (qByte<<3)+phase)
			candidates &^= byte(0x80 >> uint(phase))
		}
	}

	return d.sIdxA
}

func (d *Decoder) expandAlignedCandidates(candidateMasks []byte) []int {
	for qByte, candidates := range candidateMasks {
		for candidates != 0 {
			phase := bits.LeadingZeros8(candidates)
			d.sIdxA = append(d.sIdxA, (qByte<<3)+phase)
			candidates &^= byte(0x80 >> uint(phase))
		}
	}
	return d.sIdxA
}

// finishAlignedCandidates checks the remainder of the preamble only for
// byte positions whose first four symbols have at least one matching phase.
func (d *Decoder) finishAlignedCandidates(preamble []byte, symLenByte int, candidateMasks []byte) []int {
	for qByte, candidates := range candidateMasks {
		if candidates != 0 {
			for relativeIdx, pBit := range preamble[4:] {
				pIdx := relativeIdx + 4
				signal := d.packed[qByte+pIdx*symLenByte]
				candidates &= signal ^ ((pBit ^ 1) * 0xff)
				if candidates == 0 {
					break
				}
			}
		}

		for candidates != 0 {
			phase := bits.LeadingZeros8(candidates)
			d.sIdxA = append(d.sIdxA, (qByte<<3)+phase)
			candidates &^= byte(0x80 >> uint(phase))
		}
	}
	return d.sIdxA
}

func searchAlignedCandidates4Go(dst, packed []byte, symLenByte int, masks [4]byte) {
	if len(dst) == 0 {
		return
	}
	_ = packed[len(dst)-1+symLenByte*3]
	for qByte := range dst {
		dst[qByte] = (packed[qByte] ^ masks[0]) &
			(packed[qByte+symLenByte] ^ masks[1]) &
			(packed[qByte+symLenByte*2] ^ masks[2]) &
			(packed[qByte+symLenByte*3] ^ masks[3])
	}
}

func searchAlignedCandidates32Go(dst, packed []byte, symLenByte int, masks []byte) {
	if len(dst) == 0 {
		return
	}
	if len(masks) != 32 {
		panic("protocol: 32-symbol search requires exactly 32 masks")
	}
	_ = packed[len(dst)-1+symLenByte*(len(masks)-1)]
	for qByte := range dst {
		candidates := byte(0xff)
		for pIdx, mask := range masks {
			candidates &= packed[qByte+pIdx*symLenByte] ^ mask
		}
		dst[qByte] = candidates
	}
}

func (d *Decoder) searchPackedByteAlignedGeneric(preamble []byte, symLenByte int) []int {
	for qByte := 0; qByte < d.Cfg.BlockSize>>3; qByte++ {
		candidates := byte(0xff)
		for pIdx, pBit := range preamble {
			signal := d.packed[qByte+pIdx*symLenByte]
			candidates &= signal ^ ((pBit ^ 1) * 0xff)
		}
		for candidates != 0 {
			phase := bits.LeadingZeros8(candidates)
			d.sIdxA = append(d.sIdxA, (qByte<<3)+phase)
			candidates &^= byte(0x80 >> uint(phase))
		}
	}
	return d.sIdxA
}

func (d *Decoder) searchPackedLegacy(preamble []byte) []int {
	symLenByte := d.Cfg.SymbolLength >> 3

	// For each bit in the preamble.
	for pIdx, pBit := range preamble {
		// For 0, mask is 0xFF, for 1, mask is 0x00
		pBit = (pBit ^ 1) * 0xFF
		offset := pIdx * symLenByte
		// If this is the first bit of the preamble.
		if pIdx == 0 {
			// Truncate the list of possible indices.
			d.sIdxA = d.sIdxA[:0]
			// For each packed byte.
			for qIdx, b := range d.packed[:d.Cfg.BlockSize>>3] {
				// If the byte contains any bits that match the current preamble bit.
				if b != pBit {
					// Add the index to the list.
					d.sIdxA = append(d.sIdxA, qIdx)
				}
			}
		} else {
			// From the list of possible indices, eliminate any indices at which
			// the preamble does not exist for the current preamble bit.
			d.sIdxB, d.sIdxA = searchPassByte(pBit, d.packed[offset:], d.sIdxA, d.sIdxB[:0])

			// If we've eliminated all possible indices, there is no preamble.
			if len(d.sIdxA) == 0 {
				return nil
			}
		}
	}

	symLen := d.Cfg.SymbolLength

	// Truncate index list B.
	d.sIdxB = d.sIdxB[:0]
	// For each index in list A.
	for _, qIdx := range d.sIdxA {
		// For each bit in the current byte.
		for idx := 0; idx < 8; idx++ {
			// Add the signal-based index to index list B.
			d.sIdxB = append(d.sIdxB, (qIdx<<3)+idx)
		}
	}

	// Swap index lists A and B.
	d.sIdxA, d.sIdxB = d.sIdxB, d.sIdxA

	// Check which indices the preamble actually exists at.
	for pIdx, pBit := range preamble {
		offset := pIdx * symLen
		// Search the list of possible indices for indices at which the preamble actually exists.
		d.sIdxB, d.sIdxA = searchPassRing(pBit, d.Quantized, d.quantizedStart+offset, d.sIdxA, d.sIdxB[:0])

		// If at the current bit of the preamble, there are no indices left to
		// check, the preamble does not exist in the current sample block.
		if len(d.sIdxA) == 0 {
			return nil
		}
	}

	return d.sIdxA
}

func packQuantized(dst, src []byte) {
	for byteIdx := range dst {
		srcIdx := byteIdx << 3
		decisions := binary.LittleEndian.Uint64(src[srcIdx : srcIdx+8])
		dst[byteIdx] = byte((decisions * 0x8040201008040201) >> 56)
	}
}

func packQuantizedRing(dst, src []byte, start int) {
	if start == 0 {
		packQuantized(dst, src)
		return
	}

	bitsNeeded := len(dst) << 3
	bitsBeforeWrap := len(src) - start
	if bitsNeeded <= bitsBeforeWrap {
		packQuantized(dst, src[start:start+bitsNeeded])
		return
	}

	firstBytes := bitsBeforeWrap >> 3
	packQuantized(dst[:firstBytes], src[start:])
	packQuantized(dst[firstBytes:], src[:bitsNeeded-bitsBeforeWrap])
}

func searchPassByte(pBit byte, sig []byte, a, b []int) ([]int, []int) {
	for _, qIdx := range a {
		if sig[qIdx] != pBit {
			b = append(b, qIdx)
		}
	}

	return a, b
}

func searchPass(pBit byte, sig []byte, a, b []int) ([]int, []int) {
	for _, qIdx := range a {
		if sig[qIdx] == pBit {
			b = append(b, qIdx)
		}
	}

	return a, b
}

func searchPassRing(pBit byte, sig []byte, start int, a, b []int) ([]int, []int) {
	if start >= len(sig) {
		start -= len(sig)
	}
	for _, qIdx := range a {
		idx := start + qIdx
		if idx >= len(sig) {
			idx -= len(sig)
		}
		if sig[idx] == pBit {
			b = append(b, qIdx)
		}
	}

	return a, b
}

// Given a list of indices the preamble exists at, sample the appropriate bits
// of the signal's bit-decision. Pack bits of each index into an array of bytes
// and return each packet.
func (d Decoder) Slice(indices []int) (pkts []Data) {
	return d.slice(indices, true)
}

func (d Decoder) slice(indices []int, includeBits bool) (pkts []Data) {
	// For each of the indices the preamble exists at.
	for _, qIdx := range indices {
		// Check that we're still within the first sample block. We'll catch
		// the message on the next sample block otherwise.
		if qIdx > d.Cfg.BlockSize {
			continue
		}

		// Walk the decision ring once and assemble each output byte in a
		// register. This avoids a multiply, modulo-style ring lookup, and
		// read/modify/write of d.pkt for every packet symbol.
		qPhysical := d.quantizedStart + qIdx
		if qPhysical >= len(d.Quantized) {
			qPhysical -= len(d.Quantized)
		}
		remainingSymbols := d.Cfg.PacketSymbols
		for packetByte := range d.pkt {
			bitsInByte := 8
			if remainingSymbols < bitsInByte {
				bitsInByte = remainingSymbols
			}
			value := d.pkt[packetByte]
			for bit := 0; bit < bitsInByte; bit++ {
				value = value<<1 | d.Quantized[qPhysical]
				qPhysical += d.Cfg.SymbolLength
				if qPhysical >= len(d.Quantized) {
					qPhysical -= len(d.Quantized)
				}
			}
			d.pkt[packetByte] = value
			remainingSymbols -= bitsInByte
		}

		// Store the packet in the seen map and append to the packet list.
		data := newData(d.pkt, includeBits)
		data.Idx = qIdx
		pkts = append(pkts, data)
	}

	return
}

func NextPowerOf2(v int) int {
	return 1 << uint(math.Ceil(math.Log2(float64(v))))
}
