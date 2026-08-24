//go:build linux && arm64 && gc && !purego && !race
// +build linux,arm64,gc,!purego,!race

package protocol

import (
	"bytes"
	"math"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"unsafe"
)

func unalignedFloat64s(count, offset int) ([]byte, []float64) {
	backing := make([]byte, offset+count*8+16)
	data := backing[offset : offset+count*8]
	return backing, (*[filterManchesterA72InputSize]float64)(unsafe.Pointer(&data[0]))[:count:count]
}

type manchesterA72Leaf func(output, input unsafe.Pointer)

func runManchesterA72Leaf(leaf manchesterA72Leaf, input []float64, output []byte) {
	leaf(unsafe.Pointer(&output[0]), unsafe.Pointer(&input[0]))
}

func runManchesterA72Direct(input []float64, output []byte) {
	runManchesterA72Leaf(filterManchesterA72TBXSSRABalanced, input, output)
}

func compareManchesterOutput(t *testing.T, got, want []byte) {
	t.Helper()
	for idx := range want {
		if got[idx] != want[idx] {
			t.Fatalf("decision %d = %d, want %d", idx, got[idx], want[idx])
		}
	}
}

// These direct tests intentionally bypass production dispatch. The leaf uses
// baseline Armv8-A scalar and Advanced SIMD instructions, so an auxiliary
// Linux/ARM64 ASIMD host can validate correctness without being treated as an
// A72 timing authority.
func TestManchesterA72DirectProductionLUT(t *testing.T) {
	input := benchmarkManchesterA72Input()
	want := make([]byte, filterManchesterA72OutputSize)
	got := make([]byte, filterManchesterA72OutputSize)
	filterManchesterLiteralTest(input, want, filterManchesterA72ChipLength)
	runManchesterA72Direct(input, got)
	compareManchesterOutput(t, got, want)
}

func TestManchesterA72DirectBitsAlignmentsAndCanaries(t *testing.T) {
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
	for inputOffset := 0; inputOffset < 16; inputOffset++ {
		inputBacking, input := unalignedFloat64s(filterManchesterA72InputSize, inputOffset)
		for idx := range input {
			input[idx] = math.Float64frombits(patterns[(idx*13+idx/23+inputOffset)&(len(patterns)-1)])
		}
		inputBefore := append([]byte(nil), inputBacking...)
		want := make([]byte, filterManchesterA72OutputSize)
		filterManchesterLiteralTest(input, want, filterManchesterA72ChipLength)

		for outputOffset := 0; outputOffset < 16; outputOffset++ {
			backing := make([]byte, outputOffset+filterManchesterA72OutputSize+16)
			for idx := range backing {
				backing[idx] = 0xa5
			}
			got := backing[outputOffset : outputOffset+filterManchesterA72OutputSize]
			runManchesterA72Direct(input, got)
			compareManchesterOutput(t, got, want)
			for idx, value := range backing[:outputOffset] {
				if value != 0xa5 {
					t.Fatalf("input offset %d output offset %d: prefix canary %d changed", inputOffset, outputOffset, idx)
				}
			}
			for idx, value := range backing[outputOffset+filterManchesterA72OutputSize:] {
				if value != 0xa5 {
					t.Fatalf("input offset %d output offset %d: suffix canary %d changed", inputOffset, outputOffset, idx)
				}
			}
		}
		if !bytes.Equal(inputBacking, inputBefore) {
			t.Fatalf("input offset %d: read-only input changed", inputOffset)
		}
	}
}

func TestManchesterA72DirectRandomStress(t *testing.T) {
	iterations := 200
	if testing.Short() {
		iterations = 8
	}
	if os.Getenv("RTLAMR_STRESS_NEON") != "" {
		iterations = 2000
	}
	input := make([]float64, filterManchesterA72InputSize)
	want := make([]byte, filterManchesterA72OutputSize)
	got := make([]byte, filterManchesterA72OutputSize)
	state := uint64(0x9e3779b97f4a7c15)
	for iteration := 0; iteration < iterations; iteration++ {
		for idx := range input {
			state ^= state << 13
			state ^= state >> 7
			state ^= state << 17
			input[idx] = math.Float64frombits(state)
		}
		filterManchesterLiteralTest(input, want, filterManchesterA72ChipLength)
		runManchesterA72Direct(input, got)
		for idx := range want {
			if got[idx] != want[idx] {
				t.Fatalf("iteration %d decision %d = %d, want %d", iteration, idx, got[idx], want[idx])
			}
		}
	}
}

func TestManchesterA72DirectAllPackedDecisionBytes(t *testing.T) {
	for decisionBits := 0; decisionBits < 256; decisionBits++ {
		input := make([]float64, filterManchesterA72InputSize)
		upperAt := func(idx int) float64 {
			if uint(decisionBits)&(uint(1)<<uint(idx)) != 0 {
				return 1
			}
			return -1
		}

		// Keep lower at zero. input[80] contributes to the initial upper sum
		// without entering any of the first eight rolling updates. Values at
		// input[144:151] force every possible first packed decision byte.
		input[80] = upperAt(0)
		for idx := 0; idx < 7; idx++ {
			input[2*filterManchesterA72ChipLength+idx] = upperAt(idx+1) - upperAt(idx)
		}

		want := make([]byte, filterManchesterA72OutputSize)
		got := make([]byte, filterManchesterA72OutputSize)
		filterManchesterLiteralTest(input, want, filterManchesterA72ChipLength)
		for idx := 0; idx < 8; idx++ {
			expected := byte(1 - ((uint(decisionBits) >> uint(idx)) & 1))
			if want[idx] != expected {
				t.Fatalf("pattern %#02x oracle decision %d = %d, want %d", decisionBits, idx, want[idx], expected)
			}
		}
		runManchesterA72Direct(input, got)
		compareManchesterOutput(t, got, want)
	}
}

func TestManchesterA72DirectDoesNotCrossGuardPages(t *testing.T) {
	inputMapping, inputBytes := guardTerminatedBytes(t, filterManchesterA72InputSize*8)
	defer syscall.Munmap(inputMapping)
	outputMapping, output := guardTerminatedBytes(t, filterManchesterA72OutputSize)
	defer syscall.Munmap(outputMapping)
	input := (*[filterManchesterA72InputSize]float64)(unsafe.Pointer(&inputBytes[0]))[:]
	for idx := range input {
		input[idx] = math.Float64frombits(uint64(idx)*0x9e3779b97f4a7c15 + 0x7ff0000000000001)
	}
	want := make([]byte, filterManchesterA72OutputSize)
	filterManchesterLiteralTest(input, want, filterManchesterA72ChipLength)
	for idx := range output {
		output[idx] = 0xa5
	}
	runManchesterA72Direct(input, output)
	compareManchesterOutput(t, output, want)
}

func TestManchesterA72SelfTest(t *testing.T) {
	if !filterManchesterA72SelfTest() {
		t.Fatal("production Manchester leaf failed startup self-test")
	}
}

func TestManchesterA72ProductionWrapper(t *testing.T) {
	old := filterManchesterA72Enabled
	filterManchesterA72Enabled = true
	defer func() { filterManchesterA72Enabled = old }()

	input := benchmarkManchesterA72Input()
	want := make([]byte, filterManchesterA72OutputSize)
	got := make([]byte, filterManchesterA72OutputSize)
	filterManchesterLiteralTest(input, want, filterManchesterA72ChipLength)
	Decoder{Cfg: PacketConfig{ChipLength: filterManchesterA72ChipLength}}.Filter(input, got)
	compareManchesterOutput(t, got, want)
}

func TestManchesterA72PlatformSafetyGates(t *testing.T) {
	old := filterManchesterA72Enabled
	filterManchesterA72Enabled = true
	defer func() { filterManchesterA72Enabled = old }()

	input := make([]float64, filterManchesterA72InputSize)
	output := make([]byte, filterManchesterA72OutputSize)
	if filterManchesterA72Platform(input, output[:len(output)-1], filterManchesterA72ChipLength) {
		t.Fatal("accepted short output")
	}
	if filterManchesterA72Platform(input[:len(input)-1], output, filterManchesterA72ChipLength) {
		t.Fatal("accepted short input")
	}
	if filterManchesterA72Platform(input, output, filterManchesterA72ChipLength-1) {
		t.Fatal("accepted non-production chip length")
	}

	backing := make([]byte, filterManchesterA72InputSize*8+32)
	overlapInput := (*[filterManchesterA72InputSize]float64)(unsafe.Pointer(&backing[0]))[:]
	overlapOutput := backing[16 : 16+filterManchesterA72OutputSize]
	if filterManchesterA72Platform(overlapInput, overlapOutput, filterManchesterA72ChipLength) {
		t.Fatal("accepted overlapping input and output")
	}
}

func TestManchesterA72OverlapUsesLiteralFallback(t *testing.T) {
	old := filterManchesterA72Enabled
	filterManchesterA72Enabled = true
	defer func() { filterManchesterA72Enabled = old }()

	makeCase := func() ([]byte, []float64, []byte) {
		backing := make([]byte, filterManchesterA72InputSize*8+32)
		input := (*[filterManchesterA72InputSize]float64)(unsafe.Pointer(&backing[0]))[:]
		for idx := range input {
			input[idx] = float64((idx*73+11)&255) / 127.5
		}
		return backing, input, backing[16 : 16+filterManchesterA72OutputSize]
	}
	wantBacking, wantInput, wantOutput := makeCase()
	gotBacking, gotInput, gotOutput := makeCase()
	filterManchesterLiteralTest(wantInput, wantOutput, filterManchesterA72ChipLength)
	Decoder{Cfg: PacketConfig{ChipLength: filterManchesterA72ChipLength}}.Filter(gotInput, gotOutput)
	if !bytes.Equal(gotBacking, wantBacking) {
		t.Fatal("overlapping fallback differs from literal sequential recurrence")
	}
}

func TestManchesterA72AvailabilityMatchesGate(t *testing.T) {
	if os.Getenv("RTLAMR_DISABLE_NEON") != "" {
		if filterManchesterA72Available() {
			t.Fatal("Manchester A72 leaf available with kill switch set")
		}
		return
	}
	if got, want := filterManchesterA72Available(), detectCortexA72R0P3(); got != want {
		t.Fatalf("availability = %v, CPU gate = %v", got, want)
	}
}

func TestManchesterA72KillSwitch(t *testing.T) {
	if os.Getenv("RTLAMR_MANCHESTER_KILL_SWITCH_HELPER") == "1" {
		if filterManchesterA72Available() {
			t.Fatal("Manchester A72 leaf available in kill-switch child")
		}
		input := benchmarkManchesterA72Input()
		want := make([]byte, filterManchesterA72OutputSize)
		got := make([]byte, filterManchesterA72OutputSize)
		filterManchesterLiteralTest(input, want, filterManchesterA72ChipLength)
		Decoder{Cfg: PacketConfig{ChipLength: filterManchesterA72ChipLength}}.Filter(input, got)
		compareManchesterOutput(t, got, want)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestManchesterA72KillSwitch$")
	cmd.Env = append(os.Environ(), "RTLAMR_DISABLE_NEON=1", "RTLAMR_MANCHESTER_KILL_SWITCH_HELPER=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kill-switch child failed: %v\n%s", err, output)
	}
}
