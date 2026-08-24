package protocol

import "math/bits"

const (
	power16ChipLength = 72
	power16BlockSize  = 8192
	power16History    = power16ChipLength * 2
	power16Window     = power16History + power16BlockSize

	power16FloatImplementation = "exact-float64"
)

type power16Policy uint8

const (
	power16PolicyDisabled power16Policy = iota
	power16PolicyAutomatic
)

type power16PlatformProbe func() power16Platform

type power16Platform struct {
	implementation  string
	fallbackReason  string
	midr            string
	nativeAvailable bool
	genuineA72      bool
	asimd           bool
	selfTestPassed  bool
	killSwitch      bool
	validationOnly  bool
	run             func(decisions []byte, window []uint16, input []byte) bool
	runPacked       func(decisions, packed []byte, window []uint16, input []byte) bool
}

// DecoderDispatchStatus reports the immutable allocation-time backend choice
// plus the number of blocks that reached the selected fused leaf. Status does
// not alter dispatch and is intended for startup logs, replay gates, and
// operational diagnostics.
type DecoderDispatchStatus struct {
	Implementation     string `json:"implementation"`
	FallbackReason     string `json:"fallback_reason,omitempty"`
	MIDR               string `json:"midr,omitempty"`
	Power16Active      bool   `json:"power16_active"`
	PolicyAutomatic    bool   `json:"policy_automatic"`
	ParserCompatible   bool   `json:"parser_compatible"`
	GeometryCompatible bool   `json:"geometry_compatible"`
	NativeAvailable    bool   `json:"native_available"`
	GenuineA72         bool   `json:"genuine_a72"`
	ASIMD              bool   `json:"asimd"`
	SelfTestPassed     bool   `json:"self_test_passed"`
	KillSwitch         bool   `json:"kill_switch"`
	ValidationOnly     bool   `json:"validation_only"`
	NativeBlocks       uint64 `json:"native_blocks"`
	ValidationBlocks   uint64 `json:"validation_blocks"`
}

type power16State struct {
	historyOverlap  int
	backing         []uint16
	block           int
	blockCount      int
	blockStride     int
	blockShift      int
	blockMask       int
	gap             int
	nativeBlocks    uint64
	run             func(decisions []byte, window []uint16, input []byte) bool
	runPacked       func(decisions, packed []byte, window []uint16, input []byte) bool
	packedBacking   []byte
	packedLength    int
	packedStart     int
	packedLookahead int
}

func (d *Decoder) selectPower16Platform() (power16Platform, bool) {
	parserCompatible := d.power16ParserCount > 0 && d.power16ParsersCompatible
	geometryCompatible := d.Cfg.ChipLength == power16ChipLength &&
		d.Cfg.SymbolLength == power16History &&
		d.Cfg.BlockSize == power16BlockSize &&
		d.Cfg.BlockSize2 == power16BlockSize*2
	d.dispatchStatus = DecoderDispatchStatus{
		Implementation:     power16FloatImplementation,
		Power16Active:      false,
		PolicyAutomatic:    d.power16Policy == power16PolicyAutomatic,
		ParserCompatible:   parserCompatible,
		GeometryCompatible: geometryCompatible,
	}

	if d.power16Policy != power16PolicyAutomatic {
		d.dispatchStatus.FallbackReason = "policy-disabled"
		return power16Platform{}, false
	}
	if !parserCompatible {
		d.dispatchStatus.FallbackReason = "parser-incompatible"
		return power16Platform{}, false
	}
	if !geometryCompatible {
		d.dispatchStatus.FallbackReason = "geometry-incompatible"
		return power16Platform{}, false
	}
	if d.power16Probe == nil {
		d.dispatchStatus.FallbackReason = "platform-probe-unavailable"
		return power16Platform{}, false
	}

	platform := d.power16Probe()
	d.dispatchStatus.NativeAvailable = platform.nativeAvailable
	d.dispatchStatus.GenuineA72 = platform.genuineA72
	d.dispatchStatus.ASIMD = platform.asimd
	d.dispatchStatus.SelfTestPassed = platform.selfTestPassed
	d.dispatchStatus.KillSwitch = platform.killSwitch
	d.dispatchStatus.ValidationOnly = platform.validationOnly
	d.dispatchStatus.MIDR = platform.midr
	if platform.killSwitch || !platform.asimd ||
		(!platform.genuineA72 && !platform.validationOnly) ||
		!platform.selfTestPassed ||
		(!platform.nativeAvailable && !platform.validationOnly) || platform.run == nil {
		d.dispatchStatus.FallbackReason = platform.fallbackReason
		if d.dispatchStatus.FallbackReason == "" {
			switch {
			case platform.killSwitch:
				d.dispatchStatus.FallbackReason = "kill-switch"
			case !platform.asimd:
				d.dispatchStatus.FallbackReason = "asimd-unavailable"
			case !platform.genuineA72 && !platform.validationOnly:
				d.dispatchStatus.FallbackReason = "unsupported-cpu"
			case !platform.selfTestPassed:
				d.dispatchStatus.FallbackReason = "self-test-failed"
			default:
				d.dispatchStatus.FallbackReason = "native-unavailable"
			}
		}
		return power16Platform{}, false
	}

	d.dispatchStatus.Implementation = platform.implementation
	d.dispatchStatus.FallbackReason = ""
	d.dispatchStatus.Power16Active = true
	return platform, true
}

func (d *Decoder) allocatePower16(platform power16Platform) {
	overlap := d.signalHistoryOverlap
	if overlap < d.Cfg.SymbolLength {
		overlap = d.Cfg.SymbolLength
	}
	state := &power16State{
		historyOverlap: overlap,
		blockStride:    d.Cfg.BlockSize + overlap,
		blockCount:     (d.Cfg.BufferLength + d.Cfg.BlockSize - 1) / d.Cfg.BlockSize,
		run:            platform.run,
		runPacked:      platform.runPacked,
	}
	if state.runPacked != nil {
		state.packedLength = d.Cfg.BufferLength >> 3
		state.packedLookahead = (d.Cfg.BlockSize + d.Cfg.PreambleLength) >> 3
		state.packedBacking = make([]byte, state.packedLength+state.packedLookahead)
	}
	state.backing = make([]uint16, state.blockCount*state.blockStride)
	state.block = state.blockCount - 1
	state.blockShift = bits.TrailingZeros(uint(d.Cfg.BlockSize))
	state.blockMask = d.Cfg.BlockSize - 1
	state.gap = state.blockCount*d.Cfg.BlockSize - d.Cfg.BufferLength
	d.power16 = state

	// Only one representation is owned at a time.
	d.Signal = nil
	d.signalBacking = nil
	d.demod = nil
}

func (d *Decoder) detachPower16Consumers() {
	for _, consumer := range d.power16Consumers {
		consumer.SetPower16History(nil)
	}
}

func (d *Decoder) attachPower16Consumers() {
	for _, consumer := range d.power16Consumers {
		consumer.SetPower16History(d)
	}
}

// Power16Window returns a contiguous decoder-owned history window. It is valid
// only while the Power16 backend is active and until Decode advances the ring.
func (d *Decoder) Power16Window(idx, length int) []uint16 {
	state := d.power16
	if state == nil {
		panic("protocol: Power16 history is not active")
	}
	if idx < 0 || idx >= d.Cfg.BufferLength || length < 0 || length > state.historyOverlap ||
		length > d.Cfg.BufferLength || idx > d.Cfg.BufferLength-length {
		panic("protocol: invalid Power16 history window")
	}

	physicalIdx := state.gap + idx
	blockOffset := physicalIdx >> uint(state.blockShift)
	sampleOffset := physicalIdx & state.blockMask
	blockIdx := state.block + 1 + blockOffset
	if blockIdx >= state.blockCount {
		blockIdx -= state.blockCount
	}

	start := blockIdx*state.blockStride + state.historyOverlap + sampleOffset
	if sampleOffset+length > d.Cfg.BlockSize {
		beforeBoundary := d.Cfg.BlockSize - sampleOffset
		blockIdx++
		if blockIdx == state.blockCount {
			blockIdx = 0
		}
		start = blockIdx*state.blockStride + state.historyOverlap - beforeBoundary
	}
	return state.backing[start : start+length]
}

// appendPower16AndFilter returns true when the packed producer wrote its
// decision block directly into the Quantized ring. Callers can then enter the
// common search/parser tail without copying that block a second time.
func (d *Decoder) appendPower16AndFilter(input []byte) bool {
	if len(input) < d.Cfg.BlockSize2 {
		panic("protocol: IQ block shorter than configured block size")
	}
	input = input[:d.Cfg.BlockSize2]
	state := d.power16
	previousStart := state.block * state.blockStride
	state.block++
	if state.block == state.blockCount {
		state.block = 0
	}
	currentStart := state.block * state.blockStride
	copy(
		state.backing[currentStart:currentStart+state.historyOverlap],
		state.backing[previousStart+d.Cfg.BlockSize:previousStart+d.Cfg.BlockSize+state.historyOverlap],
	)
	filterStart := currentStart + state.historyOverlap - d.Cfg.SymbolLength
	window := state.backing[filterStart : currentStart+state.blockStride]
	if state.runPacked == nil {
		d.filterOutput = d.filterScratch
		if !state.run(d.filterOutput, window, input) {
			panic("protocol: active Power16 fused backend rejected decoder-owned buffers")
		}
	} else {
		decisions := d.filterScratch
		directDecisions := d.ownsQuantizedRing()
		var quantizedNext, quantizedTail int
		if directDecisions {
			decisions, quantizedNext, quantizedTail = d.nextQuantizedOutput()
		}
		packedOutput, packedNext, packedTail := state.nextPackedOutput(d.Cfg)
		if !state.runPacked(decisions, packedOutput, window, input) {
			panic("protocol: active Power16 fused packed backend rejected decoder-owned buffers")
		}
		if directDecisions {
			d.commitQuantizedOutput(quantizedNext, quantizedTail, len(decisions))
		}
		d.filterOutput = decisions
		state.commitPackedOutput(packedNext, packedTail, len(packedOutput))
		d.packed = state.packedSearchWindow()
		state.nativeBlocks++
		return directDecisions
	}
	state.nativeBlocks++
	return false
}

// nextPackedOutput returns contiguous storage for the packed form of the new
// decision block. packedBacking carries a lookahead-sized mirror of the ring's
// prefix, so even a block ending across the physical ring boundary is writable
// by the fixed-geometry native leaf without a split ABI.
func (s *power16State) nextPackedOutput(cfg PacketConfig) (output []byte, nextStart, tail int) {
	blockBytes := cfg.BlockSize >> 3
	nextStart = s.packedStart + blockBytes
	if nextStart >= s.packedLength {
		nextStart -= s.packedLength
	}
	tail = nextStart + (cfg.PacketLength >> 3)
	if tail >= s.packedLength {
		tail -= s.packedLength
	}
	return s.packedBacking[tail : tail+blockBytes], nextStart, tail
}

// commitPackedOutput restores the physical prefix after a cross-boundary
// direct write, then refreshes only the portion of the lookahead mirror touched
// by an in-ring write. It never copies the full search window.
func (s *power16State) commitPackedOutput(nextStart, tail, count int) {
	end := tail + count
	if end > s.packedLength {
		overflow := end - s.packedLength
		copy(s.packedBacking[:overflow], s.packedBacking[s.packedLength:end])
		end = s.packedLength
	}
	if tail < s.packedLookahead {
		mirrorEnd := end
		if mirrorEnd > s.packedLookahead {
			mirrorEnd = s.packedLookahead
		}
		copy(s.packedBacking[s.packedLength+tail:s.packedLength+mirrorEnd], s.packedBacking[tail:mirrorEnd])
	}
	s.packedStart = nextStart
}

func (s *power16State) packedSearchWindow() []byte {
	return s.packedBacking[s.packedStart : s.packedStart+s.packedLookahead]
}

// DispatchStatus returns a copy of the allocation-time backend decision.
func (d *Decoder) DispatchStatus() DecoderDispatchStatus {
	status := d.dispatchStatus
	if d.power16 != nil {
		if status.ValidationOnly {
			status.ValidationBlocks = d.power16.nativeBlocks
		} else {
			status.NativeBlocks = d.power16.nativeBlocks
		}
	}
	return status
}

func power16ReferenceBlock(decisions []byte, window []uint16, input []byte) bool {
	if len(decisions) != power16BlockSize || len(window) != power16Window || len(input) != power16BlockSize*2 {
		return false
	}
	power16IQReference(window[power16History:], input)
	power16ManchesterReference(decisions, window, power16ChipLength)
	return true
}

func power16IQReference(output []uint16, input []byte) {
	if len(output) > len(input)/2 {
		panic("protocol: integer-power input shorter than output")
	}
	for idx := range output {
		i := int32(input[idx*2])*2 - 255
		q := int32(input[idx*2+1])*2 - 255
		output[idx] = uint16((i*i + q*q) >> 1)
	}
}

func power16ManchesterReference(output []byte, input []uint16, chipLength int) {
	if chipLength <= 0 || len(input) < len(output)+chipLength*2 {
		panic("protocol: integer Manchester input shorter than output")
	}
	var lower, upper int32
	for idx := 0; idx < chipLength; idx++ {
		lower += int32(input[idx])
		upper += int32(input[idx+chipLength])
	}
	for idx := range output {
		if lower-upper >= 0 {
			output[idx] = 1
		} else {
			output[idx] = 0
		}
		lower += int32(input[idx+chipLength]) - int32(input[idx])
		upper += int32(input[idx+chipLength*2]) - int32(input[idx+chipLength])
	}
}

var _ Power16History = (*Decoder)(nil)
