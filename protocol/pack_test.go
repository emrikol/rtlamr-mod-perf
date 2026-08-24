package protocol

import (
	"bytes"
	"math/rand"
	"testing"
)

func legacyPackQuantized(dst, src []byte) {
	for byteIdx := range dst {
		var packed byte
		for _, bit := range src[byteIdx<<3 : (byteIdx+1)<<3] {
			packed = (packed << 1) | bit
		}
		dst[byteIdx] = packed
	}
}

func TestPackQuantizedMatchesLegacy(t *testing.T) {
	rng := rand.New(rand.NewSource(0x900))
	for iteration := 0; iteration < 64; iteration++ {
		byteLength := 1 + rng.Intn(2048)
		src := make([]byte, byteLength<<3)
		for idx := range src {
			src[idx] = byte(rng.Intn(2))
		}

		got := make([]byte, byteLength)
		want := make([]byte, byteLength)
		packQuantized(got, src)
		legacyPackQuantized(want, src)
		if !bytes.Equal(got, want) {
			t.Fatalf("iteration %d: packed output differs", iteration)
		}
	}
}

func TestPackQuantizedAllBytePatterns(t *testing.T) {
	src := make([]byte, 8)
	dst := make([]byte, 1)
	for pattern := 0; pattern < 256; pattern++ {
		for idx := range src {
			src[idx] = byte(pattern>>uint(7-idx)) & 1
		}
		packQuantized(dst, src)
		if dst[0] != byte(pattern) {
			t.Fatalf("pattern %#02x packed as %#02x", pattern, dst[0])
		}
	}
}

func BenchmarkPackQuantizedLegacy(b *testing.B) {
	benchmarkPackQuantized(b, legacyPackQuantized)
}

func BenchmarkPackQuantized(b *testing.B) {
	benchmarkPackQuantized(b, packQuantized)
}

func benchmarkPackQuantized(b *testing.B, pack func([]byte, []byte)) {
	const byteLength = (8192 + 4608) >> 3
	src := make([]byte, byteLength<<3)
	dst := make([]byte, byteLength)
	for idx := range src {
		src[idx] = byte(idx & 1)
	}

	b.ReportAllocs()
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		pack(dst, src)
	}
}
