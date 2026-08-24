package protocol

import (
	"math"
	"math/rand"
	"testing"
)

func filterManchesterLiteralTest(input []float64, output []byte, chipLength int) {
	var lower, upper float64
	for idx := 0; idx < chipLength; idx++ {
		lower += input[idx]
		upper += input[idx+chipLength]
	}
	for idx := range output {
		f := lower - upper
		output[idx] = 1 - byte(math.Float64bits(f)>>63)
		lower += input[idx+chipLength] - input[idx]
		upper += input[idx+2*chipLength] - input[idx+chipLength]
	}
}

func TestManchesterFilterMatchesLiteralRecurrence(t *testing.T) {
	testCases := [...]struct {
		name       string
		chipLength int
		outputSize int
	}{
		{name: "empty", chipLength: 1, outputSize: 0},
		{name: "single", chipLength: 1, outputSize: 1},
		{name: "short odd", chipLength: 7, outputSize: 31},
		{name: "non-production 72", chipLength: 72, outputSize: 8191},
		{name: "production", chipLength: 72, outputSize: 8192},
		{name: "long chip", chipLength: 128, outputSize: 257},
	}
	patterns := [...]uint64{
		0x0000000000000000, 0x8000000000000000,
		0x3ff0000000000000, 0xbff0000000000000,
		0x0010000000000000, 0x8010000000000000,
		0x0000000000000001, 0x8000000000000001,
		0x7ff0000000000000, 0xfff0000000000000,
		0x7ff8000000000001, 0xfff8000000000042,
		0x7ff0000000000001, 0xfff0000000001234,
		0x400921fb54442d18, 0xc005bf0a8b145769,
	}

	for caseIndex, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			input := make([]float64, testCase.outputSize+2*testCase.chipLength)
			for idx := range input {
				input[idx] = math.Float64frombits(patterns[(idx*13+idx/23+caseIndex)&(len(patterns)-1)])
			}
			want := make([]byte, testCase.outputSize)
			got := make([]byte, testCase.outputSize)
			filterManchesterLiteralTest(input, want, testCase.chipLength)
			Decoder{Cfg: PacketConfig{ChipLength: testCase.chipLength}}.Filter(input, got)
			for idx := range want {
				if got[idx] != want[idx] {
					t.Fatalf("decision %d = %d, want %d", idx, got[idx], want[idx])
				}
			}
		})
	}
}

func TestManchesterFilterProductionRandomStress(t *testing.T) {
	const (
		chipLength = 72
		outputSize = 8192
	)
	rng := rand.New(rand.NewSource(0x4d414e4348455354))
	input := make([]float64, outputSize+2*chipLength)
	want := make([]byte, outputSize)
	got := make([]byte, outputSize)
	iterations := 32
	if testing.Short() {
		iterations = 4
	}
	for iteration := 0; iteration < iterations; iteration++ {
		for idx := range input {
			input[idx] = math.Float64frombits(rng.Uint64())
		}
		filterManchesterLiteralTest(input, want, chipLength)
		Decoder{Cfg: PacketConfig{ChipLength: chipLength}}.Filter(input, got)
		for idx := range want {
			if got[idx] != want[idx] {
				t.Fatalf("iteration %d decision %d = %d, want %d", iteration, idx, got[idx], want[idx])
			}
		}
	}
}
