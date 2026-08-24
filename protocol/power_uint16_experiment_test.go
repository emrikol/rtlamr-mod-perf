//go:build d3_power_neon

package protocol

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"
	"unsafe"
)

const (
	powerUint16FixtureVersion = "d3-power-uint16-fixture-v1"
	powerUint16FixtureDigest  = "e05b3709f8a178ba20079366c8084a59c922b39cfe6c6d54a7dfe86509e77659"
)

var (
	powerUint16ValueSink  uint16
	powerUint16DigestSink [sha256.Size]byte
)

func powerUint16Oracle(i, q byte) uint16 {
	// Deliberately use int64 and explicit invariants rather than duplicating
	// the implementation's int32 shift expression.
	di := int64(i)*2 - 255
	dq := int64(q)*2 - 255
	numerator := di*di + dq*dq
	if numerator&1 != 0 {
		panic("integer-power numerator is not exactly divisible by two")
	}
	power := numerator / 2
	if power < 1 || power > 65025 {
		panic("integer power outside uint16 contract")
	}
	return uint16(power)
}

func TestPowerIQUint16Exhaustive(t *testing.T) {
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

	got := make([]uint16, pairs)
	powerIQUint16(got, input)
	for idx := range want {
		if got[idx] != want[idx] {
			t.Fatalf("pair I=%d Q=%d: got %d, want %d", input[idx*2], input[idx*2+1], got[idx], want[idx])
		}
	}
}

func TestPowerIQUint16RotatedIdentityAndUint16BoundsExhaustive(t *testing.T) {
	for i := 0; i < 256; i++ {
		for q := 0; q < 256; q++ {
			u := int64(i + q - 255)
			v := int64(i - q)
			if u < -255 || u > 255 || v < -255 || v > 255 {
				t.Fatalf("I=%d Q=%d: rotated coordinate out of bounds: u=%d v=%d", i, q, u, v)
			}
			uSquare := u * u
			vSquare := v * v
			if uSquare > 65025 || vSquare > 65025 {
				t.Fatalf("I=%d Q=%d: square out of uint16 bounds: u2=%d v2=%d", i, q, uSquare, vSquare)
			}
			rotated := uSquare + vSquare
			want := int64(powerUint16Oracle(byte(i), byte(q)))
			if rotated != want || rotated > 65025 {
				t.Fatalf("I=%d Q=%d: rotated=%d, oracle=%d", i, q, rotated, want)
			}
		}
	}
}

func TestPowerIQUint16ByteDifferenceIdentityExhaustive(t *testing.T) {
	for i := 0; i < 256; i++ {
		for q := 0; q < 256; q++ {
			qnot := 255 - q
			dv := int64(i - q)
			if dv < 0 {
				dv = -dv
			}
			du := int64(i - qnot)
			if du < 0 {
				du = -du
			}
			if du > 255 || dv > 255 {
				t.Fatalf("I=%d Q=%d: byte difference out of range: du=%d dv=%d", i, q, du, dv)
			}
			got := du*du + dv*dv
			want := int64(powerUint16Oracle(byte(i), byte(q)))
			if got != want || got > 65025 {
				t.Fatalf("I=%d Q=%d: byte-difference=%d, oracle=%d", i, q, got, want)
			}
		}
	}
}

func TestPowerIQUint16LengthsAlignmentsCanariesAndInputImmutability(t *testing.T) {
	lengths := []int{0, 1, 2, 7, 8, 15, 16, 17, 31, 32, 33, 63, 64, 65, 127, 128, 129, 8191, 8192, 8193}
	for _, length := range lengths {
		for inputOffset := 0; inputOffset < 32; inputOffset++ {
			for outputOffset := 0; outputOffset < 16; outputOffset++ {
				name := fmt.Sprintf("n=%d/input=%d/output-h=%d", length, inputOffset, outputOffset)
				t.Run(name, func(t *testing.T) {
					inputBacking := make([]byte, inputOffset+length*2+32)
					input := inputBacking[inputOffset : inputOffset+length*2]
					for idx := range input {
						input[idx] = byte(idx*73 + length*19 + inputOffset*11)
					}
					inputBefore := append([]byte(nil), inputBacking...)

					const canary uint16 = 0xa55a
					outputBacking := make([]uint16, outputOffset+length+16)
					for idx := range outputBacking {
						outputBacking[idx] = canary
					}
					output := outputBacking[outputOffset : outputOffset+length]
					powerIQUint16(output, input)

					for idx := range output {
						want := powerUint16Oracle(input[idx*2], input[idx*2+1])
						if output[idx] != want {
							t.Fatalf("output[%d]=%d, want %d", idx, output[idx], want)
						}
					}
					for idx := 0; idx < outputOffset; idx++ {
						if outputBacking[idx] != canary {
							t.Fatalf("prefix canary[%d]=%04x", idx, outputBacking[idx])
						}
					}
					for idx := outputOffset + length; idx < len(outputBacking); idx++ {
						if outputBacking[idx] != canary {
							t.Fatalf("suffix canary[%d]=%04x", idx, outputBacking[idx])
						}
					}
					if got := sha256.Sum256(inputBacking); got != sha256.Sum256(inputBefore) {
						t.Fatal("input backing changed")
					}
				})
			}
		}
	}
}

func TestPowerIQUint16PanicBeforeWrite(t *testing.T) {
	for _, outputLength := range []int{1, 2, 15, 16, 17, 8192} {
		input := make([]byte, outputLength*2-1)
		output := make([]uint16, outputLength)
		for idx := range output {
			output[idx] = 0xa55a
		}
		before := append([]uint16(nil), output...)
		if !panicsPowerUint16(func() { powerIQUint16(output, input) }) {
			t.Fatalf("n=%d: short input did not panic", outputLength)
		}
		for idx := range output {
			if output[idx] != before[idx] {
				t.Fatalf("n=%d: output[%d] changed before panic", outputLength, idx)
			}
		}
	}
	powerIQUint16(nil, nil)
}

func TestPowerIQUint16RejectsOverlapBeforeWrite(t *testing.T) {
	tests := []struct {
		name             string
		outputStartWords int
		inputStartBytes  int
	}{
		{"same-start", 2, 4},
		{"output-inside-input", 1, 0},
		{"input-inside-output", 0, 2},
	}
	implementations := []struct {
		name string
		fn   func([]uint16, []byte)
	}{
		{"dispatch", powerIQUint16},
		{"go", powerIQUint16Go},
	}

	const count = 16
	for _, implementation := range implementations {
		for _, test := range tests {
			t.Run(implementation.name+"/"+test.name, func(t *testing.T) {
				backing := make([]uint16, count*3)
				backingBytes := (*[1 << 28]byte)(unsafe.Pointer(&backing[0]))[: len(backing)*2 : len(backing)*2]
				input := backingBytes[test.inputStartBytes : test.inputStartBytes+count*2]
				for idx := range input {
					input[idx] = byte(idx*73 + test.inputStartBytes*19)
				}
				output := backing[test.outputStartWords : test.outputStartWords+count]
				before := append([]byte(nil), backingBytes...)

				if !panicsPowerUint16(func() { implementation.fn(output, input) }) {
					t.Fatal("overlapping buffers did not panic")
				}
				if got := sha256.Sum256(backingBytes); got != sha256.Sum256(before) {
					t.Fatal("backing changed before overlap panic")
				}
			})
		}
	}
}

func TestPowerIQUint16CriticalPairs(t *testing.T) {
	tests := []struct {
		i, q byte
		want uint16
	}{
		{0, 0, 65025},
		{255, 255, 65025},
		{0, 255, 65025},
		{127, 127, 1},
		{128, 128, 1},
		{127, 128, 1},
		{64, 192, 16385},
	}
	for _, test := range tests {
		if got := powerUint16Oracle(test.i, test.q); got != test.want {
			t.Fatalf("oracle(%d,%d)=%d, want %d", test.i, test.q, got, test.want)
		}
		input := []byte{test.i, test.q}
		got := []uint16{0xffff}
		powerIQUint16(got, input)
		if got[0] != test.want {
			t.Fatalf("power(%d,%d)=%d, want %d", test.i, test.q, got[0], test.want)
		}
	}
}

func powerUint16Fixture() ([]byte, []uint16, string) {
	const count = 8192
	input := make([]byte, count*2)
	state := uint64(0x64332d706f776572)
	for idx := range input {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		input[idx] = byte(state >> 56)
	}
	output := make([]uint16, count)
	for idx := range output {
		output[idx] = powerUint16Oracle(input[idx*2], input[idx*2+1])
	}

	h := sha256.New()
	h.Write([]byte(powerUint16FixtureVersion))
	h.Write(input)
	var encoded [2]byte
	for _, value := range output {
		binary.LittleEndian.PutUint16(encoded[:], value)
		h.Write(encoded[:])
	}
	return input, output, fmt.Sprintf("%x", h.Sum(nil))
}

func TestPowerIQUint16FixtureDigest(t *testing.T) {
	_, output, got := powerUint16Fixture()
	if got != powerUint16FixtureDigest {
		t.Fatalf("fixture digest=%s, want %s", got, powerUint16FixtureDigest)
	}
	powerUint16ValueSink = output[len(output)-1]
	powerUint16DigestSink = sha256.Sum256(uint16Bytes(output))
}

func BenchmarkPowerIQUint16Go(b *testing.B) {
	input, _, digest := powerUint16Fixture()
	if digest != powerUint16FixtureDigest {
		b.Fatalf("fixture digest=%s, want %s", digest, powerUint16FixtureDigest)
	}
	output := make([]uint16, len(input)/2)
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		powerIQUint16Go(output, input)
	}
	b.StopTimer()
	powerUint16ValueSink = output[(b.N+len(output)-1)%len(output)]
	powerUint16DigestSink = sha256.Sum256(uint16Bytes(output))
}

func uint16Bytes(values []uint16) []byte {
	encoded := make([]byte, len(values)*2)
	for idx, value := range values {
		binary.LittleEndian.PutUint16(encoded[idx*2:], value)
	}
	return encoded
}

func panicsPowerUint16(fn func()) (panicked bool) {
	defer func() { panicked = recover() != nil }()
	fn()
	return false
}
