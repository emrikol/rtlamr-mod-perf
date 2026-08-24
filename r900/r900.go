// RTLAMR - An rtl-sdr receiver for smart meters operating in the 900MHz ISM band.
// Copyright (C) 2014 Douglas Hall
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

package r900

import (
	"fmt"
	"math"
	"strconv"
	"sync"

	"github.com/bemasher/rtlamr/protocol"
	"github.com/bemasher/rtlamr/r900/gf"
)

const (
	PayloadSymbols = 42
)

func init() {
	protocol.RegisterParser("r900", NewParser)
}

func NewPacketConfig(chipLength int) (cfg protocol.PacketConfig) {

	return
}

type Parser struct {
	*protocol.Decoder
	cfg          protocol.PacketConfig
	field        *gf.Field
	rsBuf        [31]byte
	powerHistory protocol.Power16History

	filterSignal   []float64
	csum           []float64
	quantized      []byte
	quantizedStart int
	seen           map[[21]byte]struct{}
	candidateIdx   []int

	once sync.Once
}

func NewParser(chipLength int) protocol.Parser {
	var p Parser

	// Freeze the immutable process-wide Power16 quantizer selection during
	// parser construction. This keeps the native startup self-test outside the
	// decoder's measured and steady-state paths.
	_ = power16R900CurrentPlatform()

	p.cfg = protocol.PacketConfig{
		Protocol:        "r900",
		CenterFreq:      912380000,
		DataRate:        32768,
		ChipLength:      chipLength,
		PreambleSymbols: 32,
		PacketSymbols:   116,
		Preamble:        "00000000000000001110010101100100",
	}

	// GF of order 32, polynomial 37, generator 2.
	p.field = gf.NewField(32, 37, 2)
	p.seen = make(map[[21]byte]struct{})

	return &p
}

func (p *Parser) SetDecoder(d *protocol.Decoder) {
	p.Decoder = d
}

func (p *Parser) SignalHistoryOverlap() int {
	return p.cfg.ChipLength * 4
}

func (p *Parser) Power16Compatible() bool {
	return true
}

func (p *Parser) SetPower16History(history protocol.Power16History) {
	p.powerHistory = history
}

func (p *Parser) Cfg() protocol.PacketConfig {
	return p.cfg
}

// Retain only the signal history required to filter the next block.
func (p *Parser) appendSignal(block []float64) {
	historyLength := p.cfg.ChipLength * 4
	copy(p.filterSignal, p.filterSignal[p.cfg.BlockSize:])
	copy(p.filterSignal[historyLength:], block)
}

func (p *Parser) quantizedAt(idx int) byte {
	physicalIdx := p.quantizedStart + idx
	if physicalIdx >= len(p.quantized) {
		physicalIdx -= len(p.quantized)
	}
	return p.quantized[physicalIdx]
}

func (p *Parser) quantizeSignalAt(idx int) byte {
	if p.powerHistory != nil {
		return p.quantizePower16At(idx)
	}

	chipLength := p.cfg.ChipLength
	signal := p.Decoder.SignalWindow(idx, chipLength*4)
	var sum float64
	for offset := 0; offset < chipLength; offset++ {
		sum += signal[offset]
	}
	c1 := sum * 2
	for offset := chipLength; offset < chipLength*2; offset++ {
		sum += signal[offset]
	}
	c2 := sum * 2
	for offset := chipLength * 2; offset < chipLength*3; offset++ {
		sum += signal[offset]
	}
	c3 := sum * 2
	for offset := chipLength * 3; offset < chipLength*4; offset++ {
		sum += signal[offset]
	}
	c4 := sum

	v0 := c2 - c4
	v1 := c1 - c2 + c3 - c4
	v2 := c1 - c3 + c4
	return quantizeSymbol(v0, v1, v2)
}

func power16Abs(value int32) int32 {
	if value < 0 {
		return -value
	}
	return value
}

// quantizePower16Symbol preserves quantizeSymbol's strict-greater argmax and
// v0, then v1, then v2 tie precedence. Valid R900 chip sums and correlations
// are bounded well inside int32.
func quantizePower16Symbol(v0, v1, v2 int32) byte {
	symbol := byte(0)
	selected := v0
	maximum := power16Abs(v0)
	if candidate := power16Abs(v1); candidate > maximum {
		maximum = candidate
		selected = v1
		symbol = 1
	}
	if candidate := power16Abs(v2); candidate > maximum {
		selected = v2
		symbol = 2
	}
	if selected > 0 {
		symbol += 3
	}
	return symbol
}

func quantizeSymbol(v0, v1, v2 float64) byte {
	symbol := byte(0)
	selected := v0
	max := math.Abs(v0)

	if abs := math.Abs(v1); abs > max {
		max = abs
		selected = v1
		symbol = 1
	}
	if math.Abs(v2) > max {
		selected = v2
		symbol = 2
	}
	if selected > 0 {
		symbol += 3
	}

	return symbol
}

func filterAndQuantizeWindows(output []byte, c0s, c1s, c2s, c3s, c4s []float64) {
	n := len(output)
	if len(c0s) < n || len(c1s) < n || len(c2s) < n || len(c3s) < n || len(c4s) < n {
		panic("r900: filter window shorter than output")
	}

	for idx := range output {
		c0 := c0s[idx]
		c1 := c1s[idx] * 2
		c2 := c2s[idx] * 2
		c3 := c3s[idx] * 2
		c4 := c4s[idx]

		v0 := c2 - c4 - c0           // 1100
		v1 := c1 - c2 + c3 - c4 - c0 // 1010
		v2 := c1 - c3 + c4 - c0      // 1001

		output[idx] = quantizeSymbol(v0, v1, v2)
	}
}

func clearBytes(buf []byte) {
	for idx := range buf {
		buf[idx] = 0
	}
}

// Perform matched filtering and quantization for the newly appended block.
func (p *Parser) filterAndQuantize() {
	// This function computes the convolution of each symbol kernel with the
	// signal. The naive approach requires for each symbol to calculate the
	// summation of samples between a pair of indices.

	// 0 |--------|
	// 1   |--------|
	// 2     |--------|
	// 3       |--------|

	// To avoid redundant calculations we compute the cumulative sum of the
	// signal. This reduces each summation to the difference between the two
	// indices of the cumulative sum.

	cfg := p.cfg
	filterEnd := cfg.BufferLength - cfg.ChipLength*4
	filterStart := filterEnd - cfg.BlockSize

	// Advance the logical start instead of moving the retained decisions. The
	// tail is replaced below with decisions for the newly appended block.
	p.quantizedStart += cfg.BlockSize
	if p.quantizedStart >= len(p.quantized) {
		p.quantizedStart -= len(p.quantized)
	}

	p.csum[0] = 0
	sums := p.csum[1 : len(p.filterSignal)+1]
	var sum float64
	for idx, v := range p.filterSignal {
		sum += v
		sums[idx] = sum
	}

	// There are six symbols, composed of three base symbols and their bitwise
	// inversions. Compute the convolution of each base symbol with the
	// signal.

	// 1100 -> 0011
	// 1010 -> 0101
	// 1001 -> 0110

	// This is basically unreadable because of a lot of algebraic
	// simplification but is necessary for efficiency.

	qIdx := p.quantizedStart + filterStart
	if qIdx >= len(p.quantized) {
		qIdx -= len(p.quantized)
	}
	n := cfg.BlockSize
	c0s := p.csum[:n]
	c1s := p.csum[cfg.ChipLength : cfg.ChipLength+n]
	c2s := p.csum[cfg.ChipLength*2 : cfg.ChipLength*2+n]
	c3s := p.csum[cfg.ChipLength*3 : cfg.ChipLength*3+n]
	c4s := p.csum[cfg.ChipLength*4 : cfg.ChipLength*4+n]
	first := n
	if remaining := len(p.quantized) - qIdx; remaining < first {
		first = remaining
	}
	filterAndQuantizeWindows(p.quantized[qIdx:qIdx+first], c0s[:first], c1s[:first], c2s[:first], c3s[:first], c4s[:first])
	if first < n {
		filterAndQuantizeWindows(p.quantized[:n-first], c0s[first:], c1s[first:], c2s[first:], c3s[first:], c4s[first:])
	}
	qIdx += n
	if qIdx >= len(p.quantized) {
		qIdx -= len(p.quantized)
	}

	// Decisions after filterEnd are invalid. qIdx now points at filterEnd in
	// the logical buffer, so clear its short tail in at most two pieces.
	tailLength := len(p.quantized) - filterEnd
	first = tailLength
	if remaining := len(p.quantized) - qIdx; remaining < first {
		first = remaining
	}
	clearBytes(p.quantized[qIdx : qIdx+first])
	if first < tailLength {
		clearBytes(p.quantized[:tailLength-first])
	}
}

// Given a list of indices the preamble exists at, decode and parse a message.
func (p *Parser) Parse(pkts []protocol.Data, messages []protocol.Message) []protocol.Message {
	p.candidateIdx = p.candidateIdx[:0]
	for _, pkt := range pkts {
		p.candidateIdx = append(p.candidateIdx, pkt.Idx)
	}
	return p.ParseCandidateIndices(p.candidateIdx, messages)
}

// ParseCandidateIndices consumes the only packet property R900 needs. The
// ordinary Parse entry point remains available for callers and external
// decoders that construct protocol.Data.
func (p *Parser) ParseCandidateIndices(indices []int, messages []protocol.Message) []protocol.Message {
	p.once.Do(func() {
		p.cfg = p.Decoder.Cfg
	})

	cfg := p.cfg

	preambleLength := cfg.PreambleLength
	chipLength := cfg.ChipLength

	var symbols [21]byte
	for key := range p.seen {
		delete(p.seen, key)
	}

	for _, candidateIdx := range indices {
		if candidateIdx > cfg.BlockSize {
			break
		}

		payloadIdx := candidateIdx + preambleLength - p.cfg.SymbolLength
		badSymbol := false
		qIdx := payloadIdx
		for idx := range symbols {
			first := p.quantizeSignalAt(qIdx)
			second := p.quantizeSignalAt(qIdx + chipLength*4)
			symbol := first*6 + second
			if symbol > 31 {
				badSymbol = true
			}
			symbols[idx] = symbol
			qIdx += chipLength * 8
		}

		if badSymbol {
			continue
		}
		if _, duplicate := p.seen[symbols]; duplicate {
			continue
		}
		p.seen[symbols] = struct{}{}

		copy(p.rsBuf[:], symbols[:16])
		copy(p.rsBuf[26:], symbols[16:])
		syndromes := p.field.Syndrome(p.rsBuf[:], 5, 29)

		if syndromes[0]|syndromes[1]|syndromes[2]|syndromes[3]|syndromes[4] != 0 {
			continue
		}

		messages = append(messages, r900FromSymbols(&symbols))
	}

	return messages
}

// r900FromSymbols decodes the first 80 payload bits without constructing the
// legacy 105-character binary string. Twelve five-bit symbols form the high
// 60 bits and four form the low 20 bits; the remaining five are the checksum.
func r900FromSymbols(symbols *[21]byte) (message R900) {
	var high uint64
	for _, symbol := range symbols[:12] {
		high = high<<5 | uint64(symbol)
	}
	var low uint32
	for _, symbol := range symbols[12:16] {
		low = low<<5 | uint32(symbol)
	}

	message.ID = uint32(high >> 28)
	message.Unkn1 = uint8(high >> 20)
	message.NoUse = uint8(high>>14) & 0x3f
	message.BackFlow = uint8(high>>12) & 0x03
	message.Consumption = uint32(high&0x0fff)<<12 | low>>8
	message.Unkn3 = uint8(low>>6) & 0x03
	message.Leak = uint8(low>>2) & 0x0f
	message.LeakNow = uint8(low) & 0x03
	copy(message.checksum[:], symbols[16:])
	return message
}

type R900 struct {
	ID          uint32 `xml:",attr"` // 32 bits
	Unkn1       uint8  `xml:",attr"` // 8 bits
	NoUse       uint8  `xml:",attr"` // 6 bits, day bins of no use
	BackFlow    uint8  `xml:",attr"` // 2 bits, backflow past 35d hi/lo
	Consumption uint32 `xml:",attr"` // 24 bits
	Unkn3       uint8  `xml:",attr"` // 2 bits
	Leak        uint8  `xml:",attr"` // 4 bits, day bins of leak
	LeakNow     uint8  `xml:",attr"` // 2 bits, leak past 24h hi/lo
	checksum    [5]byte
}

func (r900 R900) MsgType() string {
	return "R900"
}

func (r900 R900) MeterID() uint32 {
	return r900.ID
}

func (r900 R900) MeterType() uint8 {
	return r900.Unkn1
}

func (r900 R900) Checksum() []byte {
	return r900.checksum[:]
}

func (r900 R900) String() string {
	return fmt.Sprintf("{ID:%10d Unkn1:0x%02X NoUse:%2d BackFlow:%1d Consumption:%8d Unkn3:0x%02X Leak:%2d LeakNow:%1d}",
		r900.ID,
		r900.Unkn1,
		r900.NoUse,
		r900.BackFlow,
		r900.Consumption,
		r900.Unkn3,
		r900.Leak,
		r900.LeakNow,
	)
}

func (r900 R900) Record() (r []string) {
	r = append(r, strconv.FormatUint(uint64(r900.ID), 10))
	r = append(r, strconv.FormatUint(uint64(r900.Unkn1), 10))
	r = append(r, strconv.FormatUint(uint64(r900.NoUse), 10))
	r = append(r, strconv.FormatUint(uint64(r900.BackFlow), 10))
	r = append(r, strconv.FormatUint(uint64(r900.Consumption), 10))
	r = append(r, strconv.FormatUint(uint64(r900.Unkn3), 10))
	r = append(r, strconv.FormatUint(uint64(r900.Leak), 10))
	r = append(r, strconv.FormatUint(uint64(r900.LeakNow), 10))

	return
}

var _ protocol.Power16Compatible = (*Parser)(nil)
var _ protocol.Power16HistoryConsumer = (*Parser)(nil)
var _ protocol.CandidateIndexParser = (*Parser)(nil)
