package r900

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"math"
	"testing"

	"github.com/bemasher/rtlamr/protocol"
)

var r900IntegerPipelineOracleChipLengths = [...]int{8, 32, 40, 48, 56, 64, 72, 80, 88, 96}

const r900IntegerPipelineOracleDiagnosticLimit = 8

type r900IntegerPipelineOracleResult struct {
	values   [3]int64
	symbol   byte
	selected int
	tie      bool
	signTie  bool
}

type r900IntegerPipelineOracleFloatResult struct {
	values [3]float64
	symbol byte
}

type r900IntegerPipelineOracleComparison struct {
	total               int
	ties                int
	signTies            int
	tieMismatches       int
	forbiddenMismatches int
	diagnostics         []string
}

func r900IntegerPipelineOraclePower(i, q byte) uint16 {
	ai := int64(i)*2 - 255
	aq := int64(q)*2 - 255
	return uint16((ai*ai + aq*aq) >> 1)
}

func r900IntegerPipelineOracleAbs(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func r900IntegerPipelineOracleQuantize(values [3]int64) r900IntegerPipelineOracleResult {
	selected := 0
	selectedValue := values[0]
	maximum := r900IntegerPipelineOracleAbs(values[0])
	if candidate := r900IntegerPipelineOracleAbs(values[1]); candidate > maximum {
		selected = 1
		selectedValue = values[1]
		maximum = candidate
	}
	if candidate := r900IntegerPipelineOracleAbs(values[2]); candidate > maximum {
		selected = 2
		selectedValue = values[2]
		maximum = candidate
	}
	symbol := byte(selected)
	if selectedValue > 0 {
		symbol += 3
	}
	top := 0
	for _, value := range values {
		if r900IntegerPipelineOracleAbs(value) == maximum {
			top++
		}
	}
	return r900IntegerPipelineOracleResult{
		values:   values,
		symbol:   symbol,
		selected: selected,
		tie:      top > 1,
		signTie:  selectedValue == 0,
	}
}

// r900IntegerPipelineOracleDirect uses four independent chip sums rather than
// the production cumulative-sum algebra.
func r900IntegerPipelineOracleDirect(power []uint16, chipLength int) r900IntegerPipelineOracleResult {
	if chipLength < 0 || len(power) < chipLength*4 {
		panic("r900 integer pipeline oracle: short power window")
	}
	var chip [4]int64
	for segment := 0; segment < len(chip); segment++ {
		start := segment * chipLength
		for idx := 0; idx < chipLength; idx++ {
			chip[segment] += int64(power[start+idx])
		}
	}
	return r900IntegerPipelineOracleQuantize([3]int64{
		chip[0] + chip[1] - chip[2] - chip[3],
		chip[0] - chip[1] + chip[2] - chip[3],
		chip[0] - chip[1] - chip[2] + chip[3],
	})
}

// r900IntegerPipelineOracleFloat reproduces quantizeSignalAt's summation and
// expression order over a standalone four-chip window.
func r900IntegerPipelineOracleFloat(power []float64, chipLength int) r900IntegerPipelineOracleFloatResult {
	if chipLength < 0 || len(power) < chipLength*4 {
		panic("r900 integer pipeline oracle: short float window")
	}
	var sum float64
	for idx := 0; idx < chipLength; idx++ {
		sum += power[idx]
	}
	c1 := sum * 2
	for idx := chipLength; idx < chipLength*2; idx++ {
		sum += power[idx]
	}
	c2 := sum * 2
	for idx := chipLength * 2; idx < chipLength*3; idx++ {
		sum += power[idx]
	}
	c3 := sum * 2
	for idx := chipLength * 3; idx < chipLength*4; idx++ {
		sum += power[idx]
	}
	c4 := sum
	values := [3]float64{
		c2 - c4,
		c1 - c2 + c3 - c4,
		c1 - c3 + c4,
	}
	return r900IntegerPipelineOracleFloatResult{
		values: values,
		symbol: quantizeSymbol(values[0], values[1], values[2]),
	}
}

func r900IntegerPipelineOracleAppendUint64(digest hash.Hash, value uint64) {
	var encoded [8]byte
	binary.LittleEndian.PutUint64(encoded[:], value)
	_, _ = digest.Write(encoded[:])
}

func r900IntegerPipelineOracleCompare(
	label string,
	integerResult r900IntegerPipelineOracleResult,
	floatResult r900IntegerPipelineOracleFloatResult,
	digest hash.Hash,
	comparison *r900IntegerPipelineOracleComparison,
) {
	comparison.total++
	if integerResult.tie {
		comparison.ties++
	}
	if integerResult.signTie {
		comparison.signTies++
	}
	for idx := range integerResult.values {
		r900IntegerPipelineOracleAppendUint64(digest, uint64(integerResult.values[idx]))
		r900IntegerPipelineOracleAppendUint64(digest, math.Float64bits(floatResult.values[idx]))
	}
	_, _ = digest.Write([]byte{integerResult.symbol, floatResult.symbol})
	if integerResult.symbol == floatResult.symbol {
		return
	}
	diagnostic := fmt.Sprintf(
		"%s integer-v=%v selected=%d symbol=%d tie=%t sign-tie=%t float-v=[%016x %016x %016x] symbol=%d",
		label,
		integerResult.values,
		integerResult.selected,
		integerResult.symbol,
		integerResult.tie,
		integerResult.signTie,
		math.Float64bits(floatResult.values[0]),
		math.Float64bits(floatResult.values[1]),
		math.Float64bits(floatResult.values[2]),
		floatResult.symbol,
	)
	if len(comparison.diagnostics) < r900IntegerPipelineOracleDiagnosticLimit {
		comparison.diagnostics = append(comparison.diagnostics, diagnostic)
	}
	if integerResult.tie || integerResult.signTie {
		comparison.tieMismatches++
	} else {
		comparison.forbiddenMismatches++
	}
}

func r900IntegerPipelineOracleWindow(values [4]uint16, chipLength int) []uint16 {
	power := make([]uint16, chipLength*4)
	for segment, value := range values {
		start := segment * chipLength
		for idx := 0; idx < chipLength; idx++ {
			power[start+idx] = value
		}
	}
	return power
}

func r900IntegerPipelineOracleIQ(count int, seed uint64, pattern int) []byte {
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
			iq[idx] = byte((idx*97 + int(seed)*17 + idx/5) & 255)
		default:
			panic("r900 integer pipeline oracle: unknown IQ pattern")
		}
	}
	return iq
}

func r900IntegerPipelineOraclePowerStreams(iq []byte) ([]uint16, []float64) {
	if len(iq)&1 != 0 {
		panic("r900 integer pipeline oracle: odd IQ byte count")
	}
	count := len(iq) >> 1
	integerPower := make([]uint16, count)
	floatPower := make([]float64, count)
	lut := protocol.NewMagLUT()
	for idx := 0; idx < count; idx++ {
		i := iq[idx<<1]
		q := iq[idx<<1|1]
		integerPower[idx] = r900IntegerPipelineOraclePower(i, q)
		floatPower[idx] = lut[i] + lut[q]
	}
	return integerPower, floatPower
}

func TestIntegerPipelineOracleR900TiePrecedence(t *testing.T) {
	testCases := []struct {
		name   string
		chips  [4]uint16
		symbol byte
		tie    bool
	}{
		{name: "all zero correlations", chips: [4]uint16{5, 5, 5, 5}, symbol: 0, tie: true},
		{name: "positive v0 v1 tie", chips: [4]uint16{13, 9, 9, 5}, symbol: 3, tie: true},
		{name: "positive v1 v2 tie", chips: [4]uint16{13, 5, 9, 9}, symbol: 4, tie: true},
		{name: "negative v0 v1 tie", chips: [4]uint16{5, 9, 9, 13}, symbol: 0, tie: true},
		{name: "negative v1 v2 tie", chips: [4]uint16{5, 13, 9, 9}, symbol: 1, tie: true},
		{name: "positive near tie v1", chips: [4]uint16{13, 5, 9, 5}, symbol: 4},
		{name: "positive near tie v2", chips: [4]uint16{13, 5, 5, 9}, symbol: 5},
		{name: "negative near tie v1", chips: [4]uint16{5, 13, 9, 13}, symbol: 1},
		{name: "negative unique v0", chips: [4]uint16{5, 5, 13, 9}, symbol: 0},
	}
	representable := make(map[uint16]bool, 1<<16)
	for i := 0; i <= math.MaxUint8; i++ {
		for q := 0; q <= math.MaxUint8; q++ {
			representable[r900IntegerPipelineOraclePower(byte(i), byte(q))] = true
		}
	}
	digest := sha256.New()
	for _, chipLength := range r900IntegerPipelineOracleChipLengths {
		for _, testCase := range testCases {
			for segment, power := range testCase.chips {
				if !representable[power] {
					t.Fatalf("%s chip segment %d power=%d is not attainable from uint8 IQ", testCase.name, segment, power)
				}
			}
			power := r900IntegerPipelineOracleWindow(testCase.chips, chipLength)
			result := r900IntegerPipelineOracleDirect(power, chipLength)
			if result.symbol != testCase.symbol || result.tie != testCase.tie {
				t.Fatalf("chip=%d %s result=%+v, want symbol=%d tie=%t", chipLength, testCase.name, result, testCase.symbol, testCase.tie)
			}
			floatPower := make([]float64, len(power))
			for idx, value := range power {
				floatPower[idx] = float64(value)
			}
			floatResult := r900IntegerPipelineOracleFloat(floatPower, chipLength)
			if floatResult.symbol != result.symbol {
				t.Fatalf("chip=%d %s production quantize=%d, integer oracle=%d values=%v", chipLength, testCase.name, floatResult.symbol, result.symbol, result.values)
			}
			for idx, value := range result.values {
				if floatResult.values[idx] != float64(value) {
					t.Fatalf("chip=%d %s float correlation[%d]=%g, direct integer=%d", chipLength, testCase.name, idx, floatResult.values[idx], value)
				}
				r900IntegerPipelineOracleAppendUint64(digest, uint64(value))
				r900IntegerPipelineOracleAppendUint64(digest, math.Float64bits(floatResult.values[idx]))
			}
			_, _ = digest.Write([]byte{result.symbol})
		}
	}
	gotDigest := fmt.Sprintf("%x", digest.Sum(nil))
	const wantDigest = "f4912d2b43106f2e8c456c6a28a20abaf8b7ab77c9f7b62f230c4bd1f173ddf5"
	if gotDigest != wantDigest {
		t.Fatalf("R900 tie digest=%s, want %s", gotDigest, wantDigest)
	}
	t.Logf("R900 constructed cases=%d chip-lengths=%d sha256=%s", len(testCases), len(r900IntegerPipelineOracleChipLengths), gotDigest)
}

func TestIntegerPipelineOracleR900ProductionLUT(t *testing.T) {
	digest := sha256.New()
	comparison := new(r900IntegerPipelineOracleComparison)
	for _, chipLength := range r900IntegerPipelineOracleChipLengths {
		for pattern := 0; pattern < 3; pattern++ {
			for fixture := 0; fixture < 64; fixture++ {
				seed := uint64(chipLength*65537 + pattern*257 + fixture + 1)
				iq := r900IntegerPipelineOracleIQ(chipLength*4, seed, pattern)
				integerPower, floatPower := r900IntegerPipelineOraclePowerStreams(iq)
				integerResult := r900IntegerPipelineOracleDirect(integerPower, chipLength)
				floatResult := r900IntegerPipelineOracleFloat(floatPower, chipLength)
				label := fmt.Sprintf("chip=%d/pattern=%d/fixture=%d", chipLength, pattern, fixture)
				r900IntegerPipelineOracleCompare(label, integerResult, floatResult, digest, comparison)
			}
		}
	}
	if comparison.forbiddenMismatches != 0 {
		t.Fatalf("R900 forbidden non-tie mismatches=%d first=%v", comparison.forbiddenMismatches, comparison.diagnostics)
	}
	if comparison.total != 1920 || comparison.ties != 642 || comparison.signTies != 640 || comparison.tieMismatches != 408 {
		t.Fatalf(
			"R900 classification total=%d ties=%d sign-ties=%d tie-mismatches=%d, want 1920/642/640/408",
			comparison.total, comparison.ties, comparison.signTies, comparison.tieMismatches,
		)
	}
	gotDigest := fmt.Sprintf("%x", digest.Sum(nil))
	const wantDigest = "d9b4cebed8d71640fe9eed635fff6bdb49851222c47b250af1a91191a52d5688"
	if gotDigest != wantDigest {
		t.Fatalf("R900 production-LUT digest=%s, want %s", gotDigest, wantDigest)
	}
	t.Logf(
		"R900 production-LUT cases=%d ties=%d sign-ties=%d tie-mismatches=%d forbidden=%d sha256=%s first=%v",
		comparison.total, comparison.ties, comparison.signTies, comparison.tieMismatches, comparison.forbiddenMismatches, gotDigest, comparison.diagnostics,
	)
}
