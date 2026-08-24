package protocol

import (
	"fmt"
	"math/rand"
	"testing"
)

func legacyNewData(data []byte) (d Data) {
	d.Bytes = append([]byte(nil), data...)
	for _, value := range data {
		d.Bits += fmt.Sprintf("%08b", value)
	}
	return d
}

func TestNewDataMatchesLegacy(t *testing.T) {
	rng := rand.New(rand.NewSource(0xda7a))
	for length := 0; length <= 128; length++ {
		input := make([]byte, length)
		rng.Read(input)
		got := NewData(input)
		want := legacyNewData(input)
		if got.Bits != want.Bits {
			t.Fatalf("length %d: bits differ", length)
		}
		if len(got.Bytes) != len(want.Bytes) {
			t.Fatalf("length %d: byte lengths differ", length)
		}
		for idx := range want.Bytes {
			if got.Bytes[idx] != want.Bytes[idx] {
				t.Fatalf("length %d: byte %d differs", length, idx)
			}
		}
	}
}

func benchmarkNewData(b *testing.B, decode func([]byte) Data) {
	input := make([]byte, 92)
	rand.New(rand.NewSource(0xda7a)).Read(input)
	b.ReportAllocs()
	b.SetBytes(int64(len(input)))
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		_ = decode(input)
	}
}

func BenchmarkNewDataLegacy(b *testing.B) {
	benchmarkNewData(b, legacyNewData)
}

func BenchmarkNewDataLinear(b *testing.B) {
	benchmarkNewData(b, NewData)
}
