//go:build d3_power_neon && linux && arm64 && gc && !purego && !race

package protocol

import (
	"crypto/sha256"
	"fmt"
	"math"
	"syscall"
	"testing"
	"unsafe"
)

var powerUint16FloatSink uint64

type powerUint16NEONLeaf func(output, input unsafe.Pointer, count int)

var powerUint16NEONVariants = []struct {
	name    string
	quantum int
	leaf    powerUint16NEONLeaf
}{
	{"LD2_H", 16, powerIQUint16NEONLeaf},
	{"LDP_UZP_H", 16, powerIQUint16NEONUZPLeaf},
	{"LD2_U8MUL", 16, powerIQUint16NEONU8MulLeaf},
	{"LD2_U8MUL_32", 32, powerIQUint16NEONU8Mul32Leaf},
}

func TestPowerIQUint16NEONVariantsExhaustive(t *testing.T) {
	const pairs = 256 * 256
	input := make([]byte, pairs*2)
	want := make([]uint16, pairs)
	idx := 0
	for i := 0; i < 256; i++ {
		for q := 0; q < 256; q++ {
			input[idx*2] = byte(i)
			input[idx*2+1] = byte(q)
			want[idx] = powerUint16Oracle(byte(i), byte(q))
			idx++
		}
	}
	for _, variant := range powerUint16NEONVariants {
		t.Run(variant.name, func(t *testing.T) {
			got := make([]uint16, pairs)
			variant.leaf(unsafe.Pointer(&got[0]), unsafe.Pointer(&input[0]), pairs)
			for idx := range want {
				if got[idx] != want[idx] {
					t.Fatalf("I=%d Q=%d: got %d, want %d", input[idx*2], input[idx*2+1], got[idx], want[idx])
				}
			}
		})
	}
}

func TestPowerIQUint16NEONVariantsByteAlignmentsCanariesAndInputImmutability(t *testing.T) {
	const count = 256
	for _, variant := range powerUint16NEONVariants {
		t.Run(variant.name, func(t *testing.T) {
			for inputOffset := 0; inputOffset < 32; inputOffset++ {
				for outputOffset := 0; outputOffset < 32; outputOffset++ {
					inputBacking := make([]byte, inputOffset+count*2+32)
					input := inputBacking[inputOffset : inputOffset+count*2]
					for idx := range input {
						input[idx] = byte(idx*109 + inputOffset*17 + outputOffset*29)
					}
					inputDigest := sha256.Sum256(inputBacking)

					const canary = byte(0xa5)
					outputBacking := make([]byte, outputOffset+count*2+32)
					for idx := range outputBacking {
						outputBacking[idx] = canary
					}
					output := (*[1 << 28]uint16)(unsafe.Pointer(&outputBacking[outputOffset]))[:count:count]
					variant.leaf(unsafe.Pointer(&output[0]), unsafe.Pointer(&input[0]), count)
					for idx := range output {
						want := powerUint16Oracle(input[idx*2], input[idx*2+1])
						if output[idx] != want {
							t.Fatalf("input=%d output=%d idx=%d: got %d, want %d", inputOffset, outputOffset, idx, output[idx], want)
						}
					}
					for idx, value := range outputBacking[:outputOffset] {
						if value != canary {
							t.Fatalf("input=%d output=%d prefix[%d]=%02x", inputOffset, outputOffset, idx, value)
						}
					}
					for idx, value := range outputBacking[outputOffset+count*2:] {
						if value != canary {
							t.Fatalf("input=%d output=%d suffix[%d]=%02x", inputOffset, outputOffset, idx, value)
						}
					}
					if got := sha256.Sum256(inputBacking); got != inputDigest {
						t.Fatalf("input=%d output=%d: input changed", inputOffset, outputOffset)
					}
				}
			}
		})
	}
}

func TestPowerIQUint16NEONByteAlignments(t *testing.T) {
	const count = 257
	for inputOffset := 0; inputOffset < 32; inputOffset++ {
		for outputOffset := 0; outputOffset < 32; outputOffset++ {
			inputBacking := make([]byte, inputOffset+count*2+32)
			input := inputBacking[inputOffset : inputOffset+count*2]
			for idx := range input {
				input[idx] = byte(idx*109 + inputOffset*17 + outputOffset*29)
			}
			inputDigest := sha256.Sum256(inputBacking)

			const canary = byte(0xa5)
			outputBacking := make([]byte, outputOffset+count*2+32)
			for idx := range outputBacking {
				outputBacking[idx] = canary
			}
			output := (*[1 << 28]uint16)(unsafe.Pointer(&outputBacking[outputOffset]))[:count:count]
			powerIQUint16(output, input)
			for idx := range output {
				want := powerUint16Oracle(input[idx*2], input[idx*2+1])
				if output[idx] != want {
					t.Fatalf("input=%d output=%d idx=%d: got %d, want %d", inputOffset, outputOffset, idx, output[idx], want)
				}
			}
			for idx, value := range outputBacking[:outputOffset] {
				if value != canary {
					t.Fatalf("input=%d output=%d prefix[%d]=%02x", inputOffset, outputOffset, idx, value)
				}
			}
			for idx, value := range outputBacking[outputOffset+count*2:] {
				if value != canary {
					t.Fatalf("input=%d output=%d suffix[%d]=%02x", inputOffset, outputOffset, idx, value)
				}
			}
			if got := sha256.Sum256(inputBacking); got != inputDigest {
				t.Fatalf("input=%d output=%d: input changed", inputOffset, outputOffset)
			}
		}
	}
}

func TestPowerIQUint16NEONDoesNotCrossGuardPages(t *testing.T) {
	for _, variant := range powerUint16NEONVariants {
		for _, count := range []int{variant.quantum, 8192} {
			t.Run(fmt.Sprintf("%s/n=%d", variant.name, count), func(t *testing.T) {
				inputMapping, input := guardTerminatedBytes(t, count*2)
				outputMapping, outputBytes := guardTerminatedBytes(t, count*2)
				defer syscall.Munmap(inputMapping)
				defer syscall.Munmap(outputMapping)
				for idx := range input {
					input[idx] = byte(idx*73 + count*19)
				}
				output := (*[1 << 28]uint16)(unsafe.Pointer(&outputBytes[0]))[:count:count]
				variant.leaf(unsafe.Pointer(&output[0]), unsafe.Pointer(&input[0]), count)
				for idx := range output {
					want := powerUint16Oracle(input[idx*2], input[idx*2+1])
					if output[idx] != want {
						t.Fatalf("idx=%d: got %d, want %d", idx, output[idx], want)
					}
				}
			})
		}
	}
}

func benchmarkPowerIQUint16NEONLeaf(b *testing.B, leaf powerUint16NEONLeaf) {
	input, _, digest := powerUint16Fixture()
	if digest != powerUint16FixtureDigest {
		b.Fatalf("fixture digest=%s, want %s", digest, powerUint16FixtureDigest)
	}
	output := make([]uint16, len(input)/2)
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		leaf(unsafe.Pointer(&output[0]), unsafe.Pointer(&input[0]), len(output))
	}
	b.StopTimer()
	powerUint16ValueSink = output[(b.N+len(output)-1)%len(output)]
	powerUint16DigestSink = sha256.Sum256(uint16Bytes(output))
}

func BenchmarkPowerIQUint16NEONVariants(b *testing.B) {
	for _, variant := range powerUint16NEONVariants {
		b.Run(variant.name, func(b *testing.B) {
			benchmarkPowerIQUint16NEONLeaf(b, variant.leaf)
		})
	}
}

func BenchmarkPowerIQUint16NEON(b *testing.B) {
	benchmarkPowerIQUint16NEONLeaf(b, powerIQUint16NEONLeaf)
}

// powerUint16BenchmarkVariant is overridden with -X so one stable benchmark
// name can compare separately linked binaries through the ABBA runner. Valid
// selectors are dispatch, go, ld2-h, ldp-uzp-h, ld2-u8mul,
// ld2-u8mul-32, and float-lut.
var powerUint16BenchmarkVariant = "dispatch"

func BenchmarkPowerIQUint16Selected(b *testing.B) {
	input, _, digest := powerUint16Fixture()
	if digest != powerUint16FixtureDigest {
		b.Fatalf("fixture digest=%s, want %s", digest, powerUint16FixtureDigest)
	}

	var execute func()
	var retainResult func()
	switch powerUint16BenchmarkVariant {
	case "dispatch":
		output := make([]uint16, len(input)/2)
		execute = func() { powerIQUint16(output, input) }
		retainResult = func() {
			powerUint16ValueSink = output[(b.N+len(output)-1)%len(output)]
			powerUint16DigestSink = sha256.Sum256(uint16Bytes(output))
		}
	case "go":
		output := make([]uint16, len(input)/2)
		execute = func() { powerIQUint16Go(output, input) }
		retainResult = func() {
			powerUint16ValueSink = output[(b.N+len(output)-1)%len(output)]
			powerUint16DigestSink = sha256.Sum256(uint16Bytes(output))
		}
	case "ld2-h":
		output := make([]uint16, len(input)/2)
		execute = func() { powerIQUint16NEONLeaf(unsafe.Pointer(&output[0]), unsafe.Pointer(&input[0]), len(output)) }
		retainResult = func() {
			powerUint16ValueSink = output[(b.N+len(output)-1)%len(output)]
			powerUint16DigestSink = sha256.Sum256(uint16Bytes(output))
		}
	case "ldp-uzp-h":
		output := make([]uint16, len(input)/2)
		execute = func() { powerIQUint16NEONUZPLeaf(unsafe.Pointer(&output[0]), unsafe.Pointer(&input[0]), len(output)) }
		retainResult = func() {
			powerUint16ValueSink = output[(b.N+len(output)-1)%len(output)]
			powerUint16DigestSink = sha256.Sum256(uint16Bytes(output))
		}
	case "ld2-u8mul":
		output := make([]uint16, len(input)/2)
		execute = func() { powerIQUint16NEONU8MulLeaf(unsafe.Pointer(&output[0]), unsafe.Pointer(&input[0]), len(output)) }
		retainResult = func() {
			powerUint16ValueSink = output[(b.N+len(output)-1)%len(output)]
			powerUint16DigestSink = sha256.Sum256(uint16Bytes(output))
		}
	case "ld2-u8mul-32":
		output := make([]uint16, len(input)/2)
		execute = func() {
			powerIQUint16NEONU8Mul32Leaf(unsafe.Pointer(&output[0]), unsafe.Pointer(&input[0]), len(output))
		}
		retainResult = func() {
			powerUint16ValueSink = output[(b.N+len(output)-1)%len(output)]
			powerUint16DigestSink = sha256.Sum256(uint16Bytes(output))
		}
	case "float-lut":
		output := make([]float64, len(input)/2)
		lut := NewMagLUT()
		execute = func() {
			magnitudeLUTFloat64A72(unsafe.Pointer(&output[0]), unsafe.Pointer(&input[0]), unsafe.Pointer(&lut[0]), len(output))
		}
		retainResult = func() {
			powerUint16FloatSink = math.Float64bits(output[(b.N+len(output)-1)%len(output)])
		}
	default:
		b.Fatalf("unknown integer-power benchmark variant %q", powerUint16BenchmarkVariant)
	}

	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		execute()
	}
	b.StopTimer()
	retainResult()
}

// BenchmarkPowerIQUint16FloatLUTControl runs the accepted exact float64 leaf
// from current HEAD over the identical D3 IQ fixture. It remains as a named
// control in addition to the float-lut stable-selector arm above.
func BenchmarkPowerIQUint16FloatLUTControl(b *testing.B) {
	input, _, digest := powerUint16Fixture()
	if digest != powerUint16FixtureDigest {
		b.Fatalf("fixture digest=%s, want %s", digest, powerUint16FixtureDigest)
	}
	output := make([]float64, len(input)/2)
	lut := NewMagLUT()
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		magnitudeLUTFloat64A72(unsafe.Pointer(&output[0]), unsafe.Pointer(&input[0]), unsafe.Pointer(&lut[0]), len(output))
	}
	b.StopTimer()
	powerUint16FloatSink = math.Float64bits(output[(b.N+len(output)-1)%len(output)])
}
