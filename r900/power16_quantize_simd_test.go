package r900

import (
	"fmt"
	"reflect"
	"testing"
)

const r900Power16TestChipLength = 72

type r900Power16OracleResult struct {
	values [3]int64
	symbol byte
}

// r900Power16Oracle is deliberately independent of production accumulation,
// int32 arithmetic, power16Abs, and quantizePower16Symbol.
func r900Power16Oracle(power []uint16) r900Power16OracleResult {
	if len(power) < r900Power16TestChipLength*4 {
		panic("short independent R900 Power16 oracle input")
	}
	var chip [4]int64
	for segment := range chip {
		for _, value := range power[segment*r900Power16TestChipLength : (segment+1)*r900Power16TestChipLength] {
			chip[segment] += int64(value)
		}
	}
	result := r900Power16OracleResult{values: [3]int64{
		chip[0] + chip[1] - chip[2] - chip[3],
		chip[0] - chip[1] + chip[2] - chip[3],
		chip[0] - chip[1] - chip[2] + chip[3],
	}}
	selected := result.values[0]
	maximum := r900Power16OracleAbs(selected)
	for candidate := 1; candidate < len(result.values); candidate++ {
		magnitude := r900Power16OracleAbs(result.values[candidate])
		if magnitude > maximum {
			maximum = magnitude
			selected = result.values[candidate]
			result.symbol = byte(candidate)
		}
	}
	if selected > 0 {
		result.symbol += 3
	}
	return result
}

func r900Power16OracleAbs(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func r900Power16FillChipConstants(values [4]uint16) []uint16 {
	power := make([]uint16, r900Power16TestChipLength*4)
	for segment, value := range values {
		for index := 0; index < r900Power16TestChipLength; index++ {
			power[segment*r900Power16TestChipLength+index] = value
		}
	}
	return power
}

func TestR900Power16SIMDExactBoundsSignsAndTiePrecedence(t *testing.T) {
	testCases := []struct {
		name   string
		chips  [4]uint16
		symbol byte
	}{
		{name: "all-zero", chips: [4]uint16{}, symbol: 0},
		{name: "all-maximum", chips: [4]uint16{65025, 65025, 65025, 65025}, symbol: 0},
		{name: "positive-v0-bound", chips: [4]uint16{65025, 65025, 0, 0}, symbol: 3},
		{name: "negative-v0-bound", chips: [4]uint16{0, 0, 65025, 65025}, symbol: 0},
		{name: "positive-v0-v1-tie", chips: [4]uint16{13, 9, 9, 5}, symbol: 3},
		{name: "negative-v0-v1-tie", chips: [4]uint16{5, 9, 9, 13}, symbol: 0},
		{name: "opposite-sign-v0-positive-v1-negative-tie", chips: [4]uint16{10, 13, 5, 10}, symbol: 3},
		{name: "opposite-sign-v0-negative-v1-positive-tie", chips: [4]uint16{10, 5, 13, 10}, symbol: 0},
		{name: "positive-v1-v2-tie", chips: [4]uint16{13, 5, 9, 9}, symbol: 4},
		{name: "negative-v1-v2-tie", chips: [4]uint16{5, 13, 9, 9}, symbol: 1},
		{name: "opposite-sign-v1-positive-v2-negative-tie", chips: [4]uint16{10, 10, 13, 5}, symbol: 4},
		{name: "opposite-sign-v1-negative-v2-positive-tie", chips: [4]uint16{10, 10, 5, 13}, symbol: 1},
		{name: "positive-v2", chips: [4]uint16{65025, 0, 0, 65025}, symbol: 5},
		{name: "negative-v2", chips: [4]uint16{0, 65025, 65025, 0}, symbol: 2},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			power := r900Power16FillChipConstants(testCase.chips)
			oracle := r900Power16Oracle(power)
			if oracle.symbol != testCase.symbol {
				t.Fatalf("invalid test oracle: got=%d want=%d values=%v", oracle.symbol, testCase.symbol, oracle.values)
			}
			if got := quantizePower16Window(power, r900Power16TestChipLength); got != oracle.symbol {
				t.Fatalf("symbol=%d want=%d values=%v", got, oracle.symbol, oracle.values)
			}
		})
	}
}

func TestR900Power16SIMDPropertyAgainstIndependentOracle(t *testing.T) {
	const cases = 50000
	state := uint64(0x5233504f57455231)
	power := make([]uint16, r900Power16TestChipLength*4)
	for testCase := 0; testCase < cases; testCase++ {
		for index := range power {
			state = state*6364136223846793005 + 1442695040888963407
			switch testCase & 3 {
			case 0:
				power[index] = uint16(state % 65026)
			case 1:
				boundary := [...]uint16{0, 1, 2, 13, 65023, 65024, 65025}
				power[index] = boundary[(state>>61)%uint64(len(boundary))]
			case 2:
				power[index] = uint16((uint64(index)*257 + state>>48) % 65026)
			default:
				power[index] = uint16((testCase + index*97) % 65026)
			}
		}
		want := r900Power16Oracle(power)
		if got := quantizePower16Window(power, r900Power16TestChipLength); got != want.symbol {
			t.Fatalf("case=%d got=%d want=%d values=%v", testCase, got, want.symbol, want.values)
		}
	}
}

func TestR900Power16SIMDAlignmentCanariesAndReadOnlyInput(t *testing.T) {
	const guard = 32
	state := uint64(0x5233414c49474e31)
	for residue := 0; residue < 32; residue++ {
		backing := make([]uint16, guard+residue+r900Power16TestChipLength*4+guard)
		for index := range backing {
			state = state*2862933555777941757 + 3037000493
			backing[index] = uint16(state % 65026)
		}
		before := append([]uint16(nil), backing...)
		window := backing[guard+residue : guard+residue+r900Power16TestChipLength*4]
		want := r900Power16Oracle(window)
		if got := quantizePower16Window(window, r900Power16TestChipLength); got != want.symbol {
			t.Fatalf("residue=%d got=%d want=%d values=%v", residue, got, want.symbol, want.values)
		}
		if !reflect.DeepEqual(backing, before) {
			t.Fatalf("residue=%d candidate modified input or canaries", residue)
		}
	}
}

type r900Power16TrackingHistory struct {
	values []uint16
	calls  [][2]int
}

func (history *r900Power16TrackingHistory) Power16Window(index, length int) []uint16 {
	history.calls = append(history.calls, [2]int{index, length})
	return history.values[index : index+length]
}

func TestR900Power16SIMDParserWindowAndBoundaryContract(t *testing.T) {
	const blockSize = 8192
	history := &r900Power16TrackingHistory{values: make([]uint16, blockSize+r900Power16TestChipLength*4)}
	state := uint64(0x523352494e473031)
	for index := range history.values {
		state = state*6364136223846793005 + 1
		history.values[index] = uint16(state % 65026)
	}
	parser := NewParser(r900Power16TestChipLength).(*Parser)
	parser.SetPower16History(history)
	indices := []int{0, 1, r900Power16TestChipLength - 1, blockSize - r900Power16TestChipLength*4, blockSize - 1}
	for _, index := range indices {
		want := r900Power16Oracle(history.values[index:])
		if got := parser.quantizeSignalAt(index); got != want.symbol {
			t.Fatalf("index=%d got=%d want=%d values=%v", index, got, want.symbol, want.values)
		}
	}
	wantCalls := make([][2]int, len(indices))
	for index, logical := range indices {
		wantCalls[index] = [2]int{logical, r900Power16TestChipLength * 4}
	}
	if !reflect.DeepEqual(history.calls, wantCalls) {
		t.Fatalf("Power16Window calls=%v want=%v", history.calls, wantCalls)
	}
}

func TestR900Power16SIMDFallbackGeometry(t *testing.T) {
	for _, chipLength := range []int{1, 2, 8, 16, 32, 64, 71, 73, 96} {
		power := make([]uint16, chipLength*4)
		for index := range power {
			power[index] = uint16((index*257 + chipLength*17) % 65026)
		}
		got := quantizePower16Window(power, chipLength)
		want := quantizePower16WindowGo(power, chipLength)
		if got != want {
			t.Fatalf("chip=%d fallback=%d want=%d", chipLength, got, want)
		}
	}
}

func BenchmarkR900Power16L72SIMD(b *testing.B) {
	fixture := r900Power16BenchmarkFixture()
	b.Run("go", func(b *testing.B) {
		var sink byte
		b.ReportAllocs()
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			window := fixture[(index%42)*r900Power16TestChipLength*4:]
			sink ^= quantizePower16WindowGo(window, r900Power16TestChipLength)
		}
		r900Power16BenchmarkSink = sink
	})
	b.Run("selected", func(b *testing.B) {
		var sink byte
		b.ReportAllocs()
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			window := fixture[(index%42)*r900Power16TestChipLength*4:]
			sink ^= quantizePower16Window(window, r900Power16TestChipLength)
		}
		r900Power16BenchmarkSink = sink
	})
}

var r900Power16BenchmarkSink byte

func r900Power16BenchmarkFixture() []uint16 {
	fixture := make([]uint16, 42*r900Power16TestChipLength*4)
	state := uint64(0x523342454e434831)
	for index := range fixture {
		state = state*6364136223846793005 + 1442695040888963407
		fixture[index] = uint16(state % 65026)
	}
	return fixture
}

func Example_quantizePower16Window() {
	power := r900Power16FillChipConstants([4]uint16{65025, 0, 0, 65025})
	fmt.Println(quantizePower16Window(power, r900Power16TestChipLength))
	// Output: 5
}
