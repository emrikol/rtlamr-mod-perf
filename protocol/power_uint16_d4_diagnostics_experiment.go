//go:build d3_power_neon && d4_power16_fusion

package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
)

// Power16CandidateSnapshotExperiment is one deterministic preamble-search
// result. Preamble contains the same ASCII zero/one form accepted by parsers.
// Indices is an owned copy and remains valid after either decoder advances.
type Power16CandidateSnapshotExperiment struct {
	Preamble string
	Indices  []int
}

// Power16BlockDiagnosticsExperiment compares the two complete decision/search
// states after explicit float-only and policy-enabled ordinary Decoders have
// consumed the same block. Oracle mismatch counters validate each arm
// independently; cross-arm differences are classified by the exact integer
// margin so rounding-boundary differences cannot hide non-tie regressions.
type Power16BlockDiagnosticsExperiment struct {
	Samples                     int
	IntegerTies                 int
	FloatZeroMargins            int
	FloatOracleMismatches       int
	IntegerOracleMismatches     int
	DecisionMismatches          int
	TieDecisionMismatches       int
	ForbiddenDecisionMismatches int

	FloatCandidates       int
	Power16Candidates     int
	FloatOnlyCandidates   int
	Power16OnlyCandidates int
	CandidatePreambles    int
	CandidateMismatches   int
	FloatPackets          int
	Power16Packets        int
	FloatOnlyPackets      int
	Power16OnlyPackets    int
	PacketMismatches      int
	PackedSearchBytes     int
	PackedMismatches      int

	FloatDecisionSHA256    string
	Power16DecisionSHA256  string
	IntegerMarginSHA256    string
	FloatMarginSHA256      string
	FloatCandidateSHA256   string
	Power16CandidateSHA256 string
	FloatPacketSHA256      string
	Power16PacketSHA256    string
	Power16PackedSHA256    string
	ReferencePackedSHA256  string

	FloatCandidateSnapshots   []Power16CandidateSnapshotExperiment
	Power16CandidateSnapshots []Power16CandidateSnapshotExperiment
}

func power16PacketKeysExperiment(d *Decoder, snapshots []Power16CandidateSnapshotExperiment) []string {
	var keys []string
	for _, snapshot := range snapshots {
		packets := d.Slice(snapshot.Indices)
		for _, packet := range packets {
			keys = append(keys, fmt.Sprintf("%s/%d/%x", snapshot.Preamble, packet.Idx, packet.Bytes))
		}
	}
	sort.Strings(keys)
	return keys
}

func power16StringDigestExperiment(values []string) string {
	h := sha256.New()
	var encoded [8]byte
	for _, value := range values {
		binary.LittleEndian.PutUint64(encoded[:], uint64(len(value)))
		_, _ = h.Write(encoded[:])
		_, _ = h.Write([]byte(value))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func power16StringDifferenceExperiment(left, right []string) (leftOnly, rightOnly int) {
	i, j := 0, 0
	for i < len(left) && j < len(right) {
		switch {
		case left[i] < right[j]:
			leftOnly++
			i++
		case right[j] < left[i]:
			rightOnly++
			j++
		default:
			i++
			j++
		}
	}
	return leftOnly + len(left) - i, rightOnly + len(right) - j
}

func power16CurrentFilterWindowExperiment(d *Decoder) ([]uint16, error) {
	if d.power16 == nil {
		return nil, fmt.Errorf("protocol: D4 comparison Power16 decoder is inactive")
	}
	state := d.power16
	currentStart := state.block * state.blockStride
	start := currentStart + state.historyOverlap - d.Cfg.SymbolLength
	return state.backing[start : currentStart+state.blockStride], nil
}

func power16CandidateSnapshotsExperiment(d *Decoder) []Power16CandidateSnapshotExperiment {
	keys := make([]string, 0, len(d.preambles))
	for preamble := range d.preambles {
		keys = append(keys, preamble)
	}
	sort.Strings(keys)
	result := make([]Power16CandidateSnapshotExperiment, 0, len(keys))
	for _, key := range keys {
		// Decode has already populated d.packed. Search it directly so the
		// diagnostic cannot overwrite a native packed-ring defect by rebuilding
		// the bytes from the ordinary decision history.
		indices := append([]int(nil), d.searchPacked([]byte(key))...)
		ascii := make([]byte, len(key))
		for idx := range ascii {
			ascii[idx] = '0' + key[idx]
		}
		result = append(result, Power16CandidateSnapshotExperiment{
			Preamble: string(ascii),
			Indices:  indices,
		})
	}
	return result
}

func power16PackedSearchDiagnosticsExperiment(d *Decoder) (actualSHA256, referenceSHA256 string, searchBytes, mismatches int, err error) {
	if d.power16 == nil || d.power16.runPacked == nil {
		return "", "", 0, 0, nil
	}
	if len(d.packed) == 0 {
		return "", "", 0, 0, fmt.Errorf("protocol: active packed Power16 decoder has an empty search window")
	}

	// Preserve and digest the exact bytes consumed by Decode's search/parser
	// tail. Build the oracle in disjoint storage from the ordinary Quantized
	// history; never call public Search here because it repacks d.packed.
	reference := make([]byte, len(d.packed))
	packQuantizedRing(reference, d.Quantized, d.quantizedStart)
	actualSHA256 = power16BytesDigestExperiment(d.packed)
	referenceSHA256 = power16BytesDigestExperiment(reference)
	for idx := range reference {
		if d.packed[idx] != reference[idx] {
			mismatches++
		}
	}
	if mismatches != 0 || !bytes.Equal(d.packed, reference) {
		return actualSHA256, referenceSHA256, len(reference), mismatches,
			fmt.Errorf("protocol: native packed search window differs from independent decision-ring pack: bytes=%d mismatches=%d", len(reference), mismatches)
	}
	return actualSHA256, referenceSHA256, len(reference), 0, nil
}

func power16CandidateDigestExperiment(snapshots []Power16CandidateSnapshotExperiment) string {
	h := sha256.New()
	var encoded [8]byte
	for _, snapshot := range snapshots {
		binary.LittleEndian.PutUint64(encoded[:], uint64(len(snapshot.Preamble)))
		_, _ = h.Write(encoded[:])
		_, _ = h.Write([]byte(snapshot.Preamble))
		binary.LittleEndian.PutUint64(encoded[:], uint64(len(snapshot.Indices)))
		_, _ = h.Write(encoded[:])
		for _, index := range snapshot.Indices {
			binary.LittleEndian.PutUint64(encoded[:], uint64(index))
			_, _ = h.Write(encoded[:])
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func power16CandidateDifferenceExperiment(a, b []int) (aOnly, bOnly int) {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] < b[j]:
			aOnly++
			i++
		case b[j] < a[i]:
			bOnly++
			j++
		default:
			i++
			j++
		}
	}
	return aOnly + len(a) - i, bOnly + len(b) - j
}

type power16IntegerMarginStateExperiment struct {
	power      []uint16
	chipLength int
	output     int
	lower      int64
	upper      int64
}

// newPower16IntegerMarginStateExperiment initializes two exact chip sums once.
// Each subsequent margin and advance is O(1), so a complete block is
// O(chip+block). The state is diagnostic-only and independent from the fused
// decoder's assembly recurrence.
func newPower16IntegerMarginStateExperiment(power []uint16, outputLength, chipLength int) (power16IntegerMarginStateExperiment, error) {
	var state power16IntegerMarginStateExperiment
	if outputLength < 0 || chipLength <= 0 || len(power) < outputLength+chipLength*2 {
		return state, fmt.Errorf("protocol: short D4 rolling-margin input")
	}
	state.power = power
	state.chipLength = chipLength
	for idx := 0; idx < chipLength; idx++ {
		state.lower += int64(power[idx])
		state.upper += int64(power[idx+chipLength])
	}
	return state, nil
}

func (state *power16IntegerMarginStateExperiment) margin() int64 {
	return state.lower - state.upper
}

func (state *power16IntegerMarginStateExperiment) advance() {
	idx := state.output
	chipLength := state.chipLength
	middle := int64(state.power[idx+chipLength])
	state.lower += middle - int64(state.power[idx])
	state.upper += int64(state.power[idx+chipLength*2]) - middle
	state.output++
}

// power16DirectIntegerMarginExperiment deliberately recomputes both sums. It
// is kept off the ordinary replay path and is called only to validate a
// reported candidate-oracle mismatch.
func power16DirectIntegerMarginExperiment(power []uint16, output, chipLength int) int64 {
	var lower, upper int64
	for idx := 0; idx < chipLength; idx++ {
		lower += int64(power[output+idx])
		upper += int64(power[output+chipLength+idx])
	}
	return lower - upper
}

// ComparePower16DecodedBlockExperiment is intentionally diagnostic, not part
// of either timed Decode path. Its independent integer oracle uses an exact
// O(chip+block) rolling recurrence. Direct O(chip) recomputation remains only
// on the candidate-oracle mismatch path to validate any reported failure.
func ComparePower16DecodedBlockExperiment(floatDecoder, powerDecoder *Decoder) (Power16BlockDiagnosticsExperiment, error) {
	var result Power16BlockDiagnosticsExperiment
	if floatDecoder == nil || powerDecoder == nil {
		return result, fmt.Errorf("protocol: nil D4 comparison decoder")
	}
	if floatDecoder.Cfg != powerDecoder.Cfg {
		return result, fmt.Errorf("protocol: D4 comparison geometry differs")
	}
	if len(floatDecoder.filterOutput) != floatDecoder.Cfg.BlockSize || len(powerDecoder.filterOutput) != powerDecoder.Cfg.BlockSize {
		return result, fmt.Errorf("protocol: D4 comparison requires one decoded block")
	}
	var packedErr error
	result.Power16PackedSHA256, result.ReferencePackedSHA256,
		result.PackedSearchBytes, result.PackedMismatches, packedErr =
		power16PackedSearchDiagnosticsExperiment(powerDecoder)
	if packedErr != nil {
		return result, packedErr
	}
	integerPower, err := power16CurrentFilterWindowExperiment(powerDecoder)
	if err != nil {
		return result, err
	}
	floatPower := floatDecoder.Signal
	outputLength := floatDecoder.Cfg.BlockSize
	chipLength := floatDecoder.Cfg.ChipLength
	if len(integerPower) < outputLength+chipLength*2 || len(floatPower) < outputLength+chipLength*2 {
		return result, fmt.Errorf("protocol: D4 comparison filter window is short")
	}

	result.Samples = outputLength
	result.FloatDecisionSHA256 = power16BytesDigestExperiment(floatDecoder.filterOutput)
	result.Power16DecisionSHA256 = power16BytesDigestExperiment(powerDecoder.filterOutput)
	integerMargins := sha256.New()
	floatMargins := sha256.New()
	var encoded [8]byte
	integerState, err := newPower16IntegerMarginStateExperiment(integerPower, outputLength, chipLength)
	if err != nil {
		return result, err
	}

	var floatLower, floatUpper float64
	for idx := 0; idx < chipLength; idx++ {
		floatLower += floatPower[idx]
		floatUpper += floatPower[idx+chipLength]
	}
	for outputIdx := 0; outputIdx < outputLength; outputIdx++ {
		integerMargin := integerState.margin()
		floatMargin := floatLower - floatUpper
		binary.LittleEndian.PutUint64(encoded[:], uint64(integerMargin))
		_, _ = integerMargins.Write(encoded[:])
		binary.LittleEndian.PutUint64(encoded[:], math.Float64bits(floatMargin))
		_, _ = floatMargins.Write(encoded[:])

		integerDecision := byte(0)
		if integerMargin >= 0 {
			integerDecision = 1
		}
		// Match the deployed exact-float leaf's terminal sign-bit mapping,
		// including the otherwise unreachable distinction between +0 and -0.
		floatDecision := byte(1 - math.Float64bits(floatMargin)>>63)
		if integerMargin == 0 {
			result.IntegerTies++
		}
		if floatMargin == 0 {
			result.FloatZeroMargins++
		}
		if floatDecoder.filterOutput[outputIdx] != floatDecision {
			result.FloatOracleMismatches++
		}
		if powerDecoder.filterOutput[outputIdx] != integerDecision {
			directMargin := power16DirectIntegerMarginExperiment(integerPower, outputIdx, chipLength)
			if directMargin != integerMargin {
				return result, fmt.Errorf("protocol: D4 rolling integer margin differs at output %d: rolling=%d direct=%d", outputIdx, integerMargin, directMargin)
			}
			result.IntegerOracleMismatches++
		}
		if floatDecoder.filterOutput[outputIdx] != powerDecoder.filterOutput[outputIdx] {
			result.DecisionMismatches++
			if integerMargin == 0 {
				result.TieDecisionMismatches++
			} else {
				result.ForbiddenDecisionMismatches++
			}
		}
		floatLower += floatPower[outputIdx+chipLength] - floatPower[outputIdx]
		floatUpper += floatPower[outputIdx+chipLength*2] - floatPower[outputIdx+chipLength]
		integerState.advance()
	}
	result.IntegerMarginSHA256 = hex.EncodeToString(integerMargins.Sum(nil))
	result.FloatMarginSHA256 = hex.EncodeToString(floatMargins.Sum(nil))

	result.FloatCandidateSnapshots = power16CandidateSnapshotsExperiment(floatDecoder)
	result.Power16CandidateSnapshots = power16CandidateSnapshotsExperiment(powerDecoder)
	result.FloatCandidateSHA256 = power16CandidateDigestExperiment(result.FloatCandidateSnapshots)
	result.Power16CandidateSHA256 = power16CandidateDigestExperiment(result.Power16CandidateSnapshots)
	if len(result.FloatCandidateSnapshots) != len(result.Power16CandidateSnapshots) {
		return result, fmt.Errorf("protocol: D4 comparison preamble sets differ")
	}
	result.CandidatePreambles = len(result.FloatCandidateSnapshots)
	for idx := range result.FloatCandidateSnapshots {
		left := result.FloatCandidateSnapshots[idx]
		right := result.Power16CandidateSnapshots[idx]
		if left.Preamble != right.Preamble {
			return result, fmt.Errorf("protocol: D4 comparison preamble order differs")
		}
		result.FloatCandidates += len(left.Indices)
		result.Power16Candidates += len(right.Indices)
		leftOnly, rightOnly := power16CandidateDifferenceExperiment(left.Indices, right.Indices)
		result.FloatOnlyCandidates += leftOnly
		result.Power16OnlyCandidates += rightOnly
		result.CandidateMismatches += leftOnly + rightOnly
	}
	floatPackets := power16PacketKeysExperiment(floatDecoder, result.FloatCandidateSnapshots)
	powerPackets := power16PacketKeysExperiment(powerDecoder, result.Power16CandidateSnapshots)
	result.FloatPackets = len(floatPackets)
	result.Power16Packets = len(powerPackets)
	result.FloatOnlyPackets, result.Power16OnlyPackets = power16StringDifferenceExperiment(floatPackets, powerPackets)
	result.PacketMismatches = result.FloatOnlyPackets + result.Power16OnlyPackets
	result.FloatPacketSHA256 = power16StringDigestExperiment(floatPackets)
	result.Power16PacketSHA256 = power16StringDigestExperiment(powerPackets)
	return result, nil
}

func power16BytesDigestExperiment(values []byte) string {
	hash := sha256.Sum256(values)
	return hex.EncodeToString(hash[:])
}
