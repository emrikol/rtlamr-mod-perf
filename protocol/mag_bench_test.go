package protocol

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"testing"
)

func magnitudeBenchmarkFixture() (input []byte, output []float64, lut MagLUT) {
	cfg := testDecoderConfig()
	rng := rand.New(rand.NewSource(0x1d00))
	input = make([]byte, cfg.BlockSize*2)
	rng.Read(input)
	output = make([]float64, cfg.BlockSize)
	lut = NewMagLUT()
	return
}

func magnitudeStreamingBenchmarkFixture() (inputs [2][]byte, outputs [][]float64, lut MagLUT) {
	cfg := testDecoderConfig()
	rng := rand.New(rand.NewSource(0x73747265616d6d67))
	for idx := range inputs {
		inputs[idx] = make([]byte, cfg.BlockSize*2)
		rng.Read(inputs[idx])
	}

	// Production alternates two receiver buffers and rotates magnitude output
	// across fourteen R900 history blocks. The overlap keeps every output start
	// at the same alignment while the roughly 950 KiB backing defeats the hot
	// single-buffer assumption of BenchmarkMagLUTSelected.
	overlap := cfg.SymbolLength * 2
	blockCount := (cfg.BufferLength + cfg.BlockSize - 1) / cfg.BlockSize
	stride := cfg.BlockSize + overlap
	backing := make([]float64, blockCount*stride)
	outputs = make([][]float64, blockCount)
	for block := range outputs {
		start := block*stride + overlap
		outputs[block] = backing[start : start+cfg.BlockSize]
	}
	return inputs, outputs, NewMagLUT()
}

func TestMagnitudeBenchmarkFixtureDigest(t *testing.T) {
	input, _, lut := magnitudeBenchmarkFixture()
	digest := sha256.New()
	digest.Write(input)
	var encoded [8]byte
	for _, value := range lut {
		binary.LittleEndian.PutUint64(encoded[:], math.Float64bits(value))
		digest.Write(encoded[:])
	}
	got := fmt.Sprintf("%x", digest.Sum(nil))
	const want = "9007325cd36d1f8eeea4a17d61be53cb4c74b18da8ad6151d40085d527fb2649"
	if got != want {
		t.Fatalf("fixture digest = %s, want %s", got, want)
	}
}

func TestMagnitudeStreamingBenchmarkFixtureDigest(t *testing.T) {
	inputs, outputs, _ := magnitudeStreamingBenchmarkFixture()
	digest := sha256.New()
	for _, input := range inputs {
		digest.Write(input)
	}
	var encoded [8]byte
	for _, output := range outputs {
		binary.LittleEndian.PutUint64(encoded[:], uint64(len(output)))
		digest.Write(encoded[:])
	}
	got := fmt.Sprintf("%x", digest.Sum(nil))
	const want = "0a557a148a268f911ab000b13b556a0730236e726ffdb2a0e77890ce90e3e726"
	if got != want {
		t.Fatalf("streaming fixture digest = %s, want %s", got, want)
	}
}

func TestMagLUTExecuteMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(0x1d00))
	input := make([]byte, 8192*2)
	rng.Read(input)
	got := make([]float64, len(input)/2)
	want := make([]float64, len(got))
	lut := NewMagLUT()

	lut.Execute(input, got)
	for idx := range want {
		want[idx] = lut[input[idx*2]] + lut[input[idx*2+1]]
	}
	for idx := range want {
		if got[idx] != want[idx] {
			t.Fatalf("magnitude %d = %g, want %g", idx, got[idx], want[idx])
		}
	}
}

func TestMagLUTExecuteExhaustive(t *testing.T) {
	const count = 256 * 256
	input := make([]byte, count*2)
	idx := 0
	for i := 0; i < 256; i++ {
		for q := 0; q < 256; q++ {
			input[idx*2] = byte(i)
			input[idx*2+1] = byte(q)
			idx++
		}
	}
	lut := NewMagLUT()
	want := make([]float64, count)
	got := make([]float64, count)
	magnitudeLUTGo(input, want, lut)
	lut.Execute(input, got)

	for idx := range want {
		if math.Float64bits(got[idx]) != math.Float64bits(want[idx]) {
			t.Fatalf("magnitude %d for I=%d Q=%d is %016x, want %016x", idx, input[idx*2], input[idx*2+1], math.Float64bits(got[idx]), math.Float64bits(want[idx]))
		}
	}
}

func TestMagLUTA72DirectExhaustive(t *testing.T) {
	if !magnitudeLUTA72Available() {
		t.Skip("exact Cortex-A72 magnitude kernels unavailable")
	}
	const count = 256 * 256
	input := make([]byte, count*2)
	idx := 0
	for i := 0; i < 256; i++ {
		for q := 0; q < 256; q++ {
			input[idx*2] = byte(i)
			input[idx*2+1] = byte(q)
			idx++
		}
	}
	lut := NewMagLUT()
	want := make([]float64, count)
	magnitudeLUTGo(input, want, lut)
	got := make([]float64, count)
	magnitudeLUTA72Platform(got, input, lut)
	for idx := range want {
		if math.Float64bits(got[idx]) != math.Float64bits(want[idx]) {
			t.Fatalf("magnitude %d for I=%d Q=%d is %016x, want %016x", idx, input[idx*2], input[idx*2+1], math.Float64bits(got[idx]), math.Float64bits(want[idx]))
		}
	}
}

func TestMagLUTA72ArbitraryTable(t *testing.T) {
	if !magnitudeLUTA72Available() {
		t.Skip("exact Cortex-A72 magnitude kernels unavailable")
	}
	const count = 8192
	rng := rand.New(rand.NewSource(0x6d61676e69747564))
	input := make([]byte, count*2)
	rng.Read(input)
	lut := make([]float64, 256)
	for idx := range lut {
		lut[idx] = math.Float64frombits(rng.Uint64())
	}
	lut[0] = math.Float64frombits(0)
	lut[1] = math.Float64frombits(1 << 63)
	lut[2] = math.Inf(1)
	lut[3] = math.Inf(-1)
	lut[4] = math.Float64frombits(0x7ff8000000000001)
	lut[5] = math.Float64frombits(0xfff80000000000a5)

	want := make([]float64, count)
	magnitudeLUTGo(input, want, lut)
	got := make([]float64, count)
	magnitudeLUTA72Platform(got, input, lut)
	for idx := range want {
		if math.Float64bits(got[idx]) != math.Float64bits(want[idx]) {
			t.Fatalf("magnitude %d is %016x, want %016x", idx, math.Float64bits(got[idx]), math.Float64bits(want[idx]))
		}
	}
}

func TestMagLUTExecuteLengthsAlignmentsAndCanaries(t *testing.T) {
	lut := NewMagLUT()
	lengths := []int{0, 1, 2, 7, 8, 9, 15, 16, 17, 31, 32, 40, 64, 120, 128, 136, 248, 256, 264, 504, 512, 520, 1016, 1024, 1032, 8191, 8192, 8193}
	for _, length := range lengths {
		for inputOffset := 0; inputOffset < 16; inputOffset++ {
			for outputOffset := 0; outputOffset < 8; outputOffset++ {
				name := fmt.Sprintf("n=%d/input=%d/output=%d", length, inputOffset, outputOffset)
				t.Run(name, func(t *testing.T) {
					inputBacking := make([]byte, inputOffset+length*2+4)
					input := inputBacking[inputOffset : inputOffset+length*2]
					for idx := range input {
						input[idx] = byte(idx*73 + length*19 + inputOffset)
					}
					const canary = 1.2345678901234567e99
					outputBacking := make([]float64, outputOffset+length+4)
					for idx := range outputBacking {
						outputBacking[idx] = canary
					}
					output := outputBacking[outputOffset : outputOffset+length]
					want := make([]float64, length)
					magnitudeLUTGo(input, want, lut)
					lut.Execute(input, output)
					for idx := range want {
						if math.Float64bits(output[idx]) != math.Float64bits(want[idx]) {
							t.Fatalf("magnitude %d is %016x, want %016x", idx, math.Float64bits(output[idx]), math.Float64bits(want[idx]))
						}
					}
					for idx := 0; idx < outputOffset; idx++ {
						if outputBacking[idx] != canary {
							t.Fatalf("prefix canary %d changed", idx)
						}
					}
					for idx := outputOffset + length; idx < len(outputBacking); idx++ {
						if outputBacking[idx] != canary {
							t.Fatalf("suffix canary %d changed", idx)
						}
					}
				})
			}
		}
	}
}

func TestMagLUTExecutePreservesAliasedLUTSemantics(t *testing.T) {
	input := []byte{1, 2, 3, 4, 5, 6, 7, 8, 1, 2, 3, 4, 5, 6, 7, 8}
	wantLUT := NewMagLUT()
	gotLUT := append(MagLUT(nil), wantLUT...)
	magnitudeLUTGo(input, wantLUT[1:9], wantLUT)
	gotLUT.Execute(input, gotLUT[1:9])
	for idx := range wantLUT {
		if math.Float64bits(gotLUT[idx]) != math.Float64bits(wantLUT[idx]) {
			t.Fatalf("LUT value %d is %016x, want %016x", idx, math.Float64bits(gotLUT[idx]), math.Float64bits(wantLUT[idx]))
		}
	}
}

func BenchmarkMagLUTExecute(b *testing.B) {
	input, output, lut := magnitudeBenchmarkFixture()

	b.Run("Go", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(input)))
		for idx := 0; idx < b.N; idx++ {
			magnitudeLUTGo(input, output, lut)
		}
	})
	b.Run("Dispatch", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(input)))
		for idx := 0; idx < b.N; idx++ {
			lut.Execute(input, output)
		}
	})
	if magnitudeLUTA72Available() {
		b.Run("A72", func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(input)))
			for idx := 0; idx < b.N; idx++ {
				magnitudeLUTA72Platform(output, input, lut)
			}
		})
	}
}

// magnitudeBenchmarkVariant is overridden with -X so the ABBA runner can use
// one stable benchmark name while comparing separate binaries.
var magnitudeBenchmarkVariant = "go"

func BenchmarkMagLUTSelected(b *testing.B) {
	input, output, lut := magnitudeBenchmarkFixture()

	var execute func()
	switch magnitudeBenchmarkVariant {
	case "go":
		execute = func() { magnitudeLUTGo(input, output, lut) }
	case "dispatch":
		execute = func() { lut.Execute(input, output) }
	case "a72":
		execute = func() { magnitudeLUTA72Platform(output, input, lut) }
	default:
		b.Fatalf("unknown magnitude benchmark variant %q", magnitudeBenchmarkVariant)
	}

	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		execute()
	}
}

// BenchmarkMagLUTStreamingSelected isolates the magnitude kernel while using
// production-sized rotating history. It catches cache and store-ownership
// regressions hidden by the deliberately hot single-buffer microbenchmark.
func BenchmarkMagLUTStreamingSelected(b *testing.B) {
	inputs, outputs, lut := magnitudeStreamingBenchmarkFixture()

	var execute func([]byte, []float64)
	switch magnitudeBenchmarkVariant {
	case "go":
		execute = func(input []byte, output []float64) { magnitudeLUTGo(input, output, lut) }
	case "dispatch":
		execute = func(input []byte, output []float64) { lut.Execute(input, output) }
	case "a72":
		if !magnitudeLUTA72Available() {
			b.Fatal("exact Cortex-A72 magnitude kernel unavailable")
		}
		execute = func(input []byte, output []float64) { magnitudeLUTA72Platform(output, input, lut) }
	default:
		b.Fatalf("unknown magnitude benchmark variant %q", magnitudeBenchmarkVariant)
	}

	b.ReportAllocs()
	b.SetBytes(int64(len(inputs[0])))
	b.ResetTimer()
	block := 0
	for idx := 0; idx < b.N; idx++ {
		execute(inputs[idx&1], outputs[block])
		block++
		if block == len(outputs) {
			block = 0
		}
	}
}
