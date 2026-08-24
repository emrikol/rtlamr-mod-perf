package protocol

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"math"
	"testing"
)

var integerPipelineOracleChipLengths = [...]int{8, 32, 40, 48, 56, 64, 72, 80, 88, 96}

const integerPipelineOracleDiagnosticLimit = 8

type integerPipelineOracleManchesterTrace struct {
	decisions []byte
	margins   []int64
}

type integerPipelineOracleFloatTrace struct {
	decisions []byte
	margins   []float64
}

type integerPipelineOracleComparison struct {
	total               int
	ties                int
	tieMismatches       int
	forbiddenMismatches int
	diagnostics         []string
}

func integerPipelineOraclePower(i, q byte) uint16 {
	ai := int32(i)*2 - 255
	aq := int32(q)*2 - 255
	sum := ai*ai + aq*aq
	if sum&1 != 0 {
		panic("integer pipeline oracle: odd squared-power sum")
	}
	return uint16(sum >> 1)
}

func integerPipelineOracleRotatedPower(i, q byte) uint32 {
	u := int32(i) + int32(q) - 255
	v := int32(i) - int32(q)
	return uint32(u*u + v*v)
}

func integerPipelineOraclePowerStreams(iq []byte) ([]uint16, []float64) {
	if len(iq)&1 != 0 {
		panic("integer pipeline oracle: odd IQ byte count")
	}
	count := len(iq) >> 1
	integerPower := make([]uint16, count)
	floatPower := make([]float64, count)
	lut := NewMagLUT()
	for idx := 0; idx < count; idx++ {
		i := iq[idx<<1]
		q := iq[idx<<1|1]
		integerPower[idx] = integerPipelineOraclePower(i, q)
		floatPower[idx] = lut[i] + lut[q]
	}
	return integerPower, floatPower
}

// integerPipelineOracleManchester recomputes both chip sums independently for
// every output. It intentionally does not share the production rolling state.
func integerPipelineOracleManchester(power []uint16, outputLength, chipLength int) integerPipelineOracleManchesterTrace {
	if outputLength < 0 || chipLength < 0 || len(power) < outputLength+chipLength*2 {
		panic("integer pipeline oracle: short Manchester power input")
	}
	trace := integerPipelineOracleManchesterTrace{
		decisions: make([]byte, outputLength),
		margins:   make([]int64, outputLength),
	}
	for outputIdx := 0; outputIdx < outputLength; outputIdx++ {
		var lower, upper int64
		for chipIdx := 0; chipIdx < chipLength; chipIdx++ {
			lower += int64(power[outputIdx+chipIdx])
			upper += int64(power[outputIdx+chipLength+chipIdx])
		}
		margin := lower - upper
		trace.margins[outputIdx] = margin
		if margin >= 0 {
			trace.decisions[outputIdx] = 1
		}
	}
	return trace
}

// integerPipelineOracleFloatManchester is a literal, test-local copy of the
// deployed operation order. It records the pre-update decision margin.
func integerPipelineOracleFloatManchester(power []float64, outputLength, chipLength int) integerPipelineOracleFloatTrace {
	if outputLength < 0 || chipLength < 0 || len(power) < outputLength+chipLength*2 {
		panic("integer pipeline oracle: short float Manchester input")
	}
	trace := integerPipelineOracleFloatTrace{
		decisions: make([]byte, outputLength),
		margins:   make([]float64, outputLength),
	}
	var lower, upper float64
	for idx := 0; idx < chipLength; idx++ {
		lower += power[idx]
		upper += power[idx+chipLength]
	}
	for idx := 0; idx < outputLength; idx++ {
		margin := lower - upper
		trace.margins[idx] = margin
		trace.decisions[idx] = 1 - byte(math.Float64bits(margin)>>63)
		lower += power[idx+chipLength] - power[idx]
		upper += power[idx+chipLength*2] - power[idx+chipLength]
	}
	return trace
}

func integerPipelineOracleAppendUint64(digest hash.Hash, value uint64) {
	var encoded [8]byte
	binary.LittleEndian.PutUint64(encoded[:], value)
	_, _ = digest.Write(encoded[:])
}

func integerPipelineOracleCompareManchester(
	label string,
	integerTrace integerPipelineOracleManchesterTrace,
	floatTrace integerPipelineOracleFloatTrace,
	digest hash.Hash,
	comparison *integerPipelineOracleComparison,
) {
	if len(integerTrace.decisions) != len(floatTrace.decisions) {
		panic("integer pipeline oracle: trace length mismatch")
	}
	for idx := range integerTrace.decisions {
		integerMargin := integerTrace.margins[idx]
		floatMargin := floatTrace.margins[idx]
		integerDecision := integerTrace.decisions[idx]
		floatDecision := floatTrace.decisions[idx]
		comparison.total++
		if integerMargin == 0 {
			comparison.ties++
		}
		integerPipelineOracleAppendUint64(digest, uint64(integerMargin))
		integerPipelineOracleAppendUint64(digest, math.Float64bits(floatMargin))
		_, _ = digest.Write([]byte{integerDecision, floatDecision})
		if integerDecision == floatDecision {
			continue
		}
		diagnostic := fmt.Sprintf(
			"%s idx=%d integer-margin=%d integer-decision=%d float-margin=%016x float-decision=%d",
			label, idx, integerMargin, integerDecision, math.Float64bits(floatMargin), floatDecision,
		)
		if len(comparison.diagnostics) < integerPipelineOracleDiagnosticLimit {
			comparison.diagnostics = append(comparison.diagnostics, diagnostic)
		}
		if integerMargin == 0 {
			comparison.tieMismatches++
		} else {
			comparison.forbiddenMismatches++
		}
	}
}

func integerPipelineOracleAssertDeployedFloat(t *testing.T, label string, power []float64, chipLength int, want []byte) {
	t.Helper()
	got := make([]byte, len(want))
	Decoder{Cfg: PacketConfig{ChipLength: chipLength}}.Filter(power, got)
	for idx := range want {
		if got[idx] != want[idx] {
			t.Fatalf("%s deployed float decision[%d]=%d, literal oracle=%d", label, idx, got[idx], want[idx])
		}
	}
}

func integerPipelineOracleIQ(count int, seed uint64, pattern int) []byte {
	boundary := [...]byte{0, 0, 255, 255, 0, 255, 255, 0, 127, 128, 128, 127, 1, 254, 254, 1}
	iq := make([]byte, count*2)
	state := seed
	for idx := range iq {
		switch pattern {
		case 0:
			state = state*6364136223846793005 + 1442695040888963407
			iq[idx] = byte(state >> 56)
		case 1:
			iq[idx] = boundary[(idx+int(seed))&(len(boundary)-1)]
		case 2:
			iq[idx] = byte((idx*73 + int(seed)*29 + idx/7) & 255)
		default:
			panic("integer pipeline oracle: unknown IQ pattern")
		}
	}
	return iq
}

func integerPipelineOracleNextPowerOfTwo(value int) int {
	result := 1
	for result < value {
		result <<= 1
	}
	return result
}

func TestIntegerPipelineOraclePower16Exhaustive(t *testing.T) {
	lut := NewMagLUT()
	digest := sha256.New()
	minimum := uint16(math.MaxUint16)
	maximum := uint16(0)
	for i := 0; i <= math.MaxUint8; i++ {
		for q := 0; q <= math.MaxUint8; q++ {
			power := integerPipelineOraclePower(byte(i), byte(q))
			rotated := integerPipelineOracleRotatedPower(byte(i), byte(q))
			if rotated > math.MaxUint16 {
				t.Fatalf("power I=%d Q=%d rotated wide result=%d overflows uint16", i, q, rotated)
			}
			if uint32(power) != rotated {
				t.Fatalf("power I=%d Q=%d formula=%d rotated=%d", i, q, power, rotated)
			}
			if power < minimum {
				minimum = power
			}
			if power > maximum {
				maximum = power
			}
			var encoded [2]byte
			binary.LittleEndian.PutUint16(encoded[:], power)
			_, _ = digest.Write(encoded[:])
			// Force the production table addition into the exhaustive digest too:
			// the downstream float oracle must use these exact rounded values.
			integerPipelineOracleAppendUint64(digest, math.Float64bits(lut[i]+lut[q]))
		}
	}
	if minimum != 1 || maximum != 65025 {
		t.Fatalf("Power16 range=[%d,%d], want [1,65025]", minimum, maximum)
	}
	gotDigest := fmt.Sprintf("%x", digest.Sum(nil))
	const wantDigest = "90bb4505e69243e9ec1ef873a64616543588fae949e3c4e92655f9328131744a"
	if gotDigest != wantDigest {
		t.Fatalf("Power16 exhaustive digest=%s, want %s", gotDigest, wantDigest)
	}
	t.Logf("Power16 pairs=65536 range=[%d,%d] sha256=%s", minimum, maximum, gotDigest)
}

func TestIntegerPipelineOracleSupportedBounds(t *testing.T) {
	const (
		maxPower  = uint64(65025)
		maxInt32  = uint64(1<<31 - 1)
		maxUint32 = uint64(1<<32 - 1)
	)
	digest := sha256.New()
	for _, chipLength := range integerPipelineOracleChipLengths {
		chipSum := uint64(chipLength) * maxPower
		fourChip := chipSum * 4
		doubledThreeChip := chipSum * 6
		correlation := chipSum * 2
		blockSize := integerPipelineOracleNextPowerOfTwo(chipLength * 64)
		prefix := uint64(blockSize+chipLength*4) * maxPower
		if chipSum > maxInt32 || fourChip > maxInt32 || doubledThreeChip > maxInt32 || correlation > maxInt32 {
			t.Fatalf(
				"chip=%d signed filter bound overflow: chip=%d four=%d doubled-three=%d correlation=%d",
				chipLength, chipSum, fourChip, doubledThreeChip, correlation,
			)
		}
		if prefix > maxUint32 || prefix*2 > maxInt32 {
			t.Fatalf("chip=%d block prefix bound overflow: prefix=%d doubled=%d", chipLength, prefix, prefix*2)
		}
		packetLength := uint64(736 * chipLength * 2)
		bufferLength := packetLength + uint64(blockSize)
		blockCount := (bufferLength + uint64(blockSize) - 1) / uint64(blockSize)
		retainedSamples := blockCount * uint64(blockSize+chipLength*4)
		retainedPrefix := retainedSamples * maxPower
		for _, value := range [...]uint64{
			uint64(chipLength), chipSum, fourChip, doubledThreeChip, correlation,
			uint64(blockSize), prefix, retainedPrefix,
		} {
			integerPipelineOracleAppendUint64(digest, value)
		}
		if chipLength == 96 && retainedPrefix <= maxUint32 {
			t.Fatalf("chip=96 retained-history prefix=%d unexpectedly fits uint32; all-history uint32 prefix must remain prohibited", retainedPrefix)
		}
	}
	gotDigest := fmt.Sprintf("%x", digest.Sum(nil))
	const wantDigest = "17e5a6fcc81f16dd44ee161f022813ac415da2aa8997f1d80dd8011e4957df00"
	if gotDigest != wantDigest {
		t.Fatalf("supported-bound digest=%s, want %s", gotDigest, wantDigest)
	}
	t.Logf("supported chip lengths=%v bounds-sha256=%s", integerPipelineOracleChipLengths, gotDigest)
}

func TestIntegerPipelineOracleManchesterFixtures(t *testing.T) {
	digest := sha256.New()
	comparison := new(integerPipelineOracleComparison)

	for _, chipLength := range integerPipelineOracleChipLengths {
		for pattern := 0; pattern < 3; pattern++ {
			outputLength := 257
			iq := integerPipelineOracleIQ(outputLength+chipLength*2, uint64(chipLength*257+pattern+1), pattern)
			integerPower, floatPower := integerPipelineOraclePowerStreams(iq)
			integerTrace := integerPipelineOracleManchester(integerPower, outputLength, chipLength)
			floatTrace := integerPipelineOracleFloatManchester(floatPower, outputLength, chipLength)
			label := fmt.Sprintf("chip=%d/pattern=%d", chipLength, pattern)
			integerPipelineOracleAssertDeployedFloat(t, label, floatPower, chipLength, floatTrace.decisions)
			integerPipelineOracleCompareManchester(label, integerTrace, floatTrace, digest, comparison)
		}
	}

	const (
		productionChip = 72
		productionN    = 8192
	)
	productionIQ := integerPipelineOracleIQ(productionN+productionChip*2, 0x706f7765723136, 0)
	integerPower, floatPower := integerPipelineOraclePowerStreams(productionIQ)
	integerTrace := integerPipelineOracleManchester(integerPower, productionN, productionChip)
	floatTrace := integerPipelineOracleFloatManchester(floatPower, productionN, productionChip)
	integerPipelineOracleAssertDeployedFloat(t, "production", floatPower, productionChip, floatTrace.decisions)
	integerPipelineOracleCompareManchester("production", integerTrace, floatTrace, digest, comparison)

	if comparison.forbiddenMismatches != 0 {
		t.Fatalf("Manchester forbidden non-tie mismatches=%d first=%v", comparison.forbiddenMismatches, comparison.diagnostics)
	}
	if comparison.total != 15902 || comparison.ties != 2827 || comparison.tieMismatches != 0 {
		t.Fatalf(
			"Manchester fixture classification total=%d ties=%d tie-mismatches=%d, want 15902/2827/0",
			comparison.total, comparison.ties, comparison.tieMismatches,
		)
	}
	gotDigest := fmt.Sprintf("%x", digest.Sum(nil))
	const wantDigest = "0636f107769c4b38e387dfa7fb7b6d2eb2589ea2aa646dca2f53c3375dad742c"
	if gotDigest != wantDigest {
		t.Fatalf("Manchester fixture digest=%s, want %s", gotDigest, wantDigest)
	}
	t.Logf(
		"Manchester fixtures total=%d ties=%d tie-mismatches=%d forbidden=%d sha256=%s first=%v",
		comparison.total, comparison.ties, comparison.tieMismatches, comparison.forbiddenMismatches, gotDigest, comparison.diagnostics,
	)
}

func TestIntegerPipelineOracleManchesterSequentialBoundaries(t *testing.T) {
	const (
		chipLength = 72
		blockSize  = 8192
		blocks     = 4
	)
	integerHistory := make([]uint16, chipLength*2)
	floatHistory := make([]float64, chipLength*2)
	digest := sha256.New()
	comparison := new(integerPipelineOracleComparison)

	for block := 0; block < blocks; block++ {
		pattern := block % 3
		iq := integerPipelineOracleIQ(blockSize, uint64(0x626c6f636b0000+block), pattern)
		integerBlock, floatBlock := integerPipelineOraclePowerStreams(iq)
		integerWindow := make([]uint16, 0, chipLength*2+blockSize)
		integerWindow = append(integerWindow, integerHistory...)
		integerWindow = append(integerWindow, integerBlock...)
		floatWindow := make([]float64, 0, chipLength*2+blockSize)
		floatWindow = append(floatWindow, floatHistory...)
		floatWindow = append(floatWindow, floatBlock...)

		integerTrace := integerPipelineOracleManchester(integerWindow, blockSize, chipLength)
		floatTrace := integerPipelineOracleFloatManchester(floatWindow, blockSize, chipLength)
		label := fmt.Sprintf("sequential/block=%d/pattern=%d", block, pattern)
		integerPipelineOracleAssertDeployedFloat(t, label, floatWindow, chipLength, floatTrace.decisions)
		integerPipelineOracleCompareManchester(label, integerTrace, floatTrace, digest, comparison)

		copy(integerHistory, integerWindow[len(integerWindow)-chipLength*2:])
		copy(floatHistory, floatWindow[len(floatWindow)-chipLength*2:])
	}

	if comparison.forbiddenMismatches != 0 {
		t.Fatalf("sequential Manchester forbidden non-tie mismatches=%d first=%v", comparison.forbiddenMismatches, comparison.diagnostics)
	}
	if comparison.total != 32768 || comparison.ties != 8050 || comparison.tieMismatches != 0 {
		t.Fatalf(
			"sequential Manchester classification total=%d ties=%d tie-mismatches=%d, want 32768/8050/0",
			comparison.total, comparison.ties, comparison.tieMismatches,
		)
	}
	gotDigest := fmt.Sprintf("%x", digest.Sum(nil))
	const wantDigest = "c1cb5c9d59b95da56ab6020966afa377ec311a8b0114991b1fdf8e5676c4315a"
	if gotDigest != wantDigest {
		t.Fatalf("sequential Manchester digest=%s, want %s", gotDigest, wantDigest)
	}
	t.Logf(
		"Manchester sequential blocks=%d total=%d ties=%d tie-mismatches=%d forbidden=%d sha256=%s first=%v",
		blocks, comparison.total, comparison.ties, comparison.tieMismatches, comparison.forbiddenMismatches, gotDigest, comparison.diagnostics,
	)
}
