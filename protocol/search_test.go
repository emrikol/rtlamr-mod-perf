package protocol

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"unsafe"
)

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for idx := range a {
		if a[idx] != b[idx] {
			return false
		}
	}
	return true
}

func scalarPreambleSearch(signal, preamble []byte, blockSize, symbolLength int) []int {
	result := make([]int, 0)
	for qIdx := 0; qIdx < blockSize; qIdx++ {
		match := true
		for pIdx, pBit := range preamble {
			if signal[qIdx+pIdx*symbolLength] != pBit {
				match = false
				break
			}
		}
		if match {
			result = append(result, qIdx)
		}
	}
	return result
}

func newSearchDecoder(signal []byte, blockSize, symbolLength, preambleLength int) *Decoder {
	packed := make([]byte, (blockSize+preambleLength+7)>>3)
	packQuantized(packed, signal)
	return &Decoder{
		Cfg: PacketConfig{
			BlockSize:      blockSize,
			SymbolLength:   symbolLength,
			PreambleLength: preambleLength,
		},
		Quantized: signal,
		packed:    packed,
		sIdxA:     make([]int, 0, blockSize),
		sIdxB:     make([]int, 0, blockSize),
	}
}

func TestByteAlignedSearchMatchesScalarOracle(t *testing.T) {
	const (
		blockSize    = 8192
		symbolLength = 144
		preambleBits = 32
	)
	preambleLength := preambleBits * symbolLength
	signalLength := blockSize + preambleLength
	rng := rand.New(rand.NewSource(0x51a1))
	preamble := make([]byte, preambleBits)
	signal := make([]byte, signalLength)
	planted := []int{0, 1, 7, 8, blockSize - 1}

	for iteration := 0; iteration < 64; iteration++ {
		for idx := range preamble {
			preamble[idx] = byte(rng.Intn(2))
		}
		for idx := range signal {
			signal[idx] = byte(rng.Intn(2))
		}
		for _, qIdx := range planted {
			for pIdx, pBit := range preamble {
				signal[qIdx+pIdx*symbolLength] = pBit
			}
		}

		decoder := newSearchDecoder(signal, blockSize, symbolLength, preambleLength)
		got := append([]int(nil), decoder.searchPacked(preamble)...)
		want := scalarPreambleSearch(signal, preamble, blockSize, symbolLength)
		if !equalInts(got, want) {
			t.Fatalf("iteration %d: indices = %v, want %v", iteration, got, want)
		}
	}
}

func TestByteAlignedSearchAllMatching(t *testing.T) {
	const (
		blockSize    = 64
		symbolLength = 16
		preambleBits = 4
	)
	preambleLength := preambleBits * symbolLength
	preamble := make([]byte, preambleBits)
	signal := make([]byte, blockSize+preambleLength)
	decoder := newSearchDecoder(signal, blockSize, symbolLength, preambleLength)
	got := decoder.searchPacked(preamble)
	want := scalarPreambleSearch(signal, preamble, blockSize, symbolLength)
	if !equalInts(got, want) {
		t.Fatalf("indices = %v, want %v", got, want)
	}
}

func TestByteAlignedSearchShortPreambles(t *testing.T) {
	const (
		blockSize    = 64
		symbolLength = 16
	)
	rng := rand.New(rand.NewSource(0x51a3))
	for preambleBits := 1; preambleBits <= 3; preambleBits++ {
		preambleLength := preambleBits * symbolLength
		preamble := make([]byte, preambleBits)
		signal := make([]byte, blockSize+preambleLength)
		for idx := range preamble {
			preamble[idx] = byte(rng.Intn(2))
		}
		for idx := range signal {
			signal[idx] = byte(rng.Intn(2))
		}
		decoder := newSearchDecoder(signal, blockSize, symbolLength, preambleLength)
		got := decoder.searchPacked(preamble)
		want := scalarPreambleSearch(signal, preamble, blockSize, symbolLength)
		if !equalInts(got, want) {
			t.Fatalf("%d-bit preamble: indices = %v, want %v", preambleBits, got, want)
		}
	}
}

func TestByteAlignedSearchMatchesRingWrappedSignal(t *testing.T) {
	cfg := testDecoderConfig()
	rng := rand.New(rand.NewSource(0x51a2))
	preamble := make([]byte, cfg.PreambleLength/cfg.SymbolLength)
	logical := make([]byte, cfg.BufferLength)
	for idx := range preamble {
		preamble[idx] = byte(rng.Intn(2))
	}
	for idx := range logical {
		logical[idx] = byte(rng.Intn(2))
	}
	for _, qIdx := range []int{0, 1, 7, 8, cfg.BlockSize - 1} {
		for pIdx, pBit := range preamble {
			logical[qIdx+pIdx*cfg.SymbolLength] = pBit
		}
	}
	want := scalarPreambleSearch(logical, preamble, cfg.BlockSize, cfg.SymbolLength)
	starts := []int{0, 8, cfg.BufferLength - 8, cfg.BlockSize, (cfg.BlockSize * 13) % cfg.BufferLength}

	for _, start := range starts {
		physical := make([]byte, cfg.BufferLength)
		for logicalIdx, value := range logical {
			physicalIdx := start + logicalIdx
			if physicalIdx >= len(physical) {
				physicalIdx -= len(physical)
			}
			physical[physicalIdx] = value
		}
		decoder := &Decoder{
			Cfg:            cfg,
			Quantized:      physical,
			quantizedStart: start,
			packed:         make([]byte, (cfg.BlockSize+cfg.PreambleLength+7)>>3),
			sIdxA:          make([]int, 0, cfg.BlockSize),
			sIdxB:          make([]int, 0, cfg.BlockSize),
		}
		packQuantizedRing(decoder.packed, decoder.Quantized, decoder.quantizedStart)
		got := decoder.searchPacked(preamble)
		if !equalInts(got, want) {
			t.Fatalf("ring start %d: indices = %v, want %v", start, got, want)
		}
	}
}

func TestSearchFallsBackForUnalignedSymbols(t *testing.T) {
	const (
		blockSize    = 64
		symbolLength = 10
		preambleBits = 4
	)
	preambleLength := preambleBits * symbolLength
	preamble := make([]byte, preambleBits)
	signal := make([]byte, blockSize+preambleLength+8)
	decoder := newSearchDecoder(signal, blockSize, symbolLength, preambleLength)
	got := decoder.searchPacked(preamble)
	want := scalarPreambleSearch(signal, preamble, blockSize, symbolLength)
	if !equalInts(got, want) {
		t.Fatalf("indices = %v, want %v", got, want)
	}
}

func TestSearchAlignedCandidates4MatchesGo(t *testing.T) {
	if !searchAlignedCandidates4Available() {
		t.Skip("Cortex-A72 r0p3 NEON search is unavailable")
	}
	const symLenByte = 18
	rng := rand.New(rand.NewSource(0xa72))
	for _, count := range []int{16, 32, 1024} {
		for _, offset := range []int{0, 1, 7, 15} {
			packedBacking := make([]byte, offset+count+symLenByte*3)
			packed := packedBacking[offset:]
			rng.Read(packed)
			for maskBits := 0; maskBits < 16; maskBits++ {
				var masks [4]byte
				for idx := range masks {
					if maskBits&(1<<uint(idx)) != 0 {
						masks[idx] = 0xff
					}
				}
				want := make([]byte, count)
				gotBacking := bytes.Repeat([]byte{0xa5}, offset+count+1)
				got := gotBacking[offset : offset+count]
				searchAlignedCandidates4Go(want, packed, symLenByte, masks)
				searchAlignedCandidates4Platform(got, packed, symLenByte, masks)
				if !bytes.Equal(got, want) {
					t.Fatalf("count=%d offset=%d masks=%#x: candidate masks differ", count, offset, masks)
				}
				if !bytes.Equal(gotBacking[:offset], bytes.Repeat([]byte{0xa5}, offset)) || gotBacking[len(gotBacking)-1] != 0xa5 {
					t.Fatalf("count=%d offset=%d masks=%#x: destination canary changed", count, offset, masks)
				}
			}
		}
	}
}

func TestSearchAlignedCandidates4Stress(t *testing.T) {
	if os.Getenv("RTLAMR_STRESS_NEON") == "" {
		t.Skip("set RTLAMR_STRESS_NEON=1 to run the extended assembly stress test")
	}
	if !searchAlignedCandidates4Available() {
		t.Skip("Cortex-A72 r0p3 NEON search is unavailable")
	}

	const (
		iterations = 200000
		maxCount   = 4096
		maxStride  = 64
		maxOffset  = 31
	)
	counts := [...]int{16, 32, 48, 64, 128, 256, 512, 1024, 2048, 4096}
	rng := rand.New(rand.NewSource(0x57e55a72))
	packedBacking := make([]byte, maxOffset+maxCount+maxStride*3)
	want := make([]byte, maxCount)
	gotBacking := make([]byte, maxOffset+maxCount+1)

	for iteration := 0; iteration < iterations; iteration++ {
		count := counts[rng.Intn(len(counts))]
		stride := rng.Intn(maxStride) + 1
		packedOffset := rng.Intn(maxOffset + 1)
		dstOffset := rng.Intn(maxOffset + 1)
		packed := packedBacking[packedOffset : packedOffset+count+stride*3]
		rng.Read(packed)
		maskBits := rng.Intn(16)
		var masks [4]byte
		for idx := range masks {
			if maskBits&(1<<uint(idx)) != 0 {
				masks[idx] = 0xff
			}
		}

		for idx := range gotBacking {
			gotBacking[idx] = 0xa5
		}
		got := gotBacking[dstOffset : dstOffset+count]
		searchAlignedCandidates4Go(want[:count], packed, stride, masks)
		searchAlignedCandidates4Platform(got, packed, stride, masks)
		if !bytes.Equal(got, want[:count]) {
			t.Fatalf("iteration=%d count=%d stride=%d packedOffset=%d dstOffset=%d masks=%#x: candidate masks differ", iteration, count, stride, packedOffset, dstOffset, masks)
		}
		canaryChanged := gotBacking[dstOffset+count] != 0xa5
		for idx := 0; idx < dstOffset; idx++ {
			canaryChanged = canaryChanged || gotBacking[idx] != 0xa5
		}
		if canaryChanged {
			t.Fatalf("iteration=%d: destination canary changed", iteration)
		}
	}
}

func TestSearchAlignedCandidates32MatchesGo(t *testing.T) {
	if !searchAlignedCandidates4Available() {
		t.Skip("Cortex-A72 r0p3 NEON search is unavailable")
	}
	const (
		count      = 1024
		symLenByte = 18
	)
	rng := rand.New(rand.NewSource(0x32a72))
	packed := make([]byte, count+symLenByte*31)
	masks := make([]byte, 32)
	rng.Read(packed)
	for idx := range masks {
		masks[idx] = byte(rng.Intn(2)) * 0xff
	}
	want := make([]byte, count)
	got := make([]byte, count)
	searchAlignedCandidates32Go(want, packed, symLenByte, masks)
	wantDecoder := Decoder{sIdxA: make([]int, 0, count*8)}
	wantIndices := wantDecoder.expandAlignedCandidates(want)
	gotIndices := searchAlignedCandidates32Platform(got, packed, symLenByte, masks, make([]int, 0, count*8))
	if !bytes.Equal(got, want) {
		t.Fatal("Cortex-A72 32-symbol candidate masks differ from Go reference")
	}
	if !equalInts(gotIndices, wantIndices) {
		t.Fatal("Cortex-A72 32-symbol indices differ from Go reference")
	}
}

func TestSearchAlignedCandidates32Stress(t *testing.T) {
	if os.Getenv("RTLAMR_STRESS_NEON") == "" {
		t.Skip("set RTLAMR_STRESS_NEON=1 to run the extended assembly stress test")
	}
	if !searchAlignedCandidates4Available() {
		t.Skip("Cortex-A72 r0p3 NEON search is unavailable")
	}

	const (
		iterations = 20000
		maxCount   = 4096
		maxStride  = 64
		maxOffset  = 31
	)
	counts := [...]int{16, 32, 48, 64, 128, 256, 512, 1024, 2048, 4096}
	rng := rand.New(rand.NewSource(0x3257e55a72))
	packedBacking := make([]byte, maxOffset+maxCount+maxStride*31)
	masksBacking := make([]byte, maxOffset+32)
	wantMasks := make([]byte, maxCount)
	gotMasksBacking := make([]byte, maxOffset+maxCount+1)
	wantDecoder := Decoder{sIdxA: make([]int, 0, maxCount*8)}
	gotIndicesBacking := make([]int, maxOffset+maxCount*8+1)

	for iteration := 0; iteration < iterations; iteration++ {
		count := counts[rng.Intn(len(counts))]
		stride := rng.Intn(maxStride) + 1
		packedOffset := rng.Intn(maxOffset + 1)
		masksOffset := rng.Intn(maxOffset + 1)
		dstOffset := rng.Intn(maxOffset + 1)
		indicesOffset := rng.Intn(maxOffset + 1)
		packed := packedBacking[packedOffset : packedOffset+count+stride*31]
		masks := masksBacking[masksOffset : masksOffset+32]
		rng.Read(packed)
		for idx := range masks {
			masks[idx] = byte(rng.Intn(2)) * 0xff
		}
		if iteration&15 == 0 {
			qByte := rng.Intn(count)
			phase := rng.Intn(8)
			bit := byte(0x80 >> uint(phase))
			for pIdx, mask := range masks {
				signal := &packed[qByte+pIdx*stride]
				if mask == 0 {
					*signal |= bit
				} else {
					*signal &^= bit
				}
			}
		}

		for idx := range gotMasksBacking {
			gotMasksBacking[idx] = 0xa5
		}
		for idx := range gotIndicesBacking {
			gotIndicesBacking[idx] = -1
		}
		gotMasks := gotMasksBacking[dstOffset : dstOffset+count]
		gotIndices := gotIndicesBacking[indicesOffset : indicesOffset : indicesOffset+count*8]
		searchAlignedCandidates32Go(wantMasks[:count], packed, stride, masks)
		wantDecoder.sIdxA = wantDecoder.sIdxA[:0]
		wantIndices := wantDecoder.expandAlignedCandidates(wantMasks[:count])
		gotIndices = searchAlignedCandidates32Platform(gotMasks, packed, stride, masks, gotIndices)
		if !bytes.Equal(gotMasks, wantMasks[:count]) || !equalInts(gotIndices, wantIndices) {
			t.Fatalf("iteration=%d count=%d stride=%d: Cortex-A72 result differs", iteration, count, stride)
		}
		masksCanaryChanged := gotMasksBacking[dstOffset+count] != 0xa5
		for idx := 0; idx < dstOffset; idx++ {
			masksCanaryChanged = masksCanaryChanged || gotMasksBacking[idx] != 0xa5
		}
		indicesCanaryChanged := gotIndicesBacking[indicesOffset+count*8] != -1
		for idx := 0; idx < indicesOffset; idx++ {
			indicesCanaryChanged = indicesCanaryChanged || gotIndicesBacking[idx] != -1
		}
		if masksCanaryChanged || indicesCanaryChanged {
			t.Fatalf("iteration=%d: destination canary changed", iteration)
		}
	}
}

func TestSearchAlignedCandidates32FixedProductionPreambles(t *testing.T) {
	if !searchAlignedCandidates32FixedAvailable() {
		t.Skip("Cortex-A72 r0p3 fixed 32-symbol search is unavailable")
	}
	const (
		count      = 1024
		symLenByte = 18
	)
	testCases := [...]struct {
		name string
		bits string
	}{
		{name: "IDM", bits: "01010101010101010001011010100011"},
		{name: "R900", bits: "00000000000000001110010101100100"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			preamble := make([]byte, len(testCase.bits))
			masks := make([]byte, len(testCase.bits))
			for idx, bit := range []byte(testCase.bits) {
				preamble[idx] = bit - '0'
				masks[idx] = (preamble[idx] ^ 1) * 0xff
			}
			for residue := 0; residue < 64; residue++ {
				packed := bytesAtCacheLineResidue(count+symLenByte*31, residue)
				rng := rand.New(rand.NewSource(0x32a7200 + int64(residue)))
				rng.Read(packed)
				for _, candidate := range [...]struct{ qByte, phase int }{{0, 0}, {63, 7}, {1023, 4}} {
					candidateBit := byte(0x80 >> uint(candidate.phase))
					for symbol, preambleBit := range preamble {
						signal := &packed[candidate.qByte+symbol*symLenByte]
						if preambleBit == 1 {
							*signal |= candidateBit
						} else {
							*signal &^= candidateBit
						}
					}
				}

				wantMasks := make([]byte, count)
				searchAlignedCandidates32Go(wantMasks, packed, symLenByte, masks)
				wantDecoder := Decoder{sIdxA: make([]int, 0, count*8)}
				wantIndices := wantDecoder.expandAlignedCandidates(wantMasks)

				scratchBacking := bytes.Repeat([]byte{0xa5}, count+2)
				scratch := scratchBacking[1 : count+1]
				indicesBacking := make([]int, count*8+2)
				for idx := range indicesBacking {
					indicesBacking[idx] = -1
				}
				indices := indicesBacking[1 : 1 : count*8+1]
				gotIndices, ok := searchAlignedCandidates32FixedPlatform(preamble, scratch, packed, symLenByte, indices)
				if !ok {
					t.Fatalf("residue=%d: fixed path was not selected", residue)
				}
				if !equalInts(gotIndices, wantIndices) {
					t.Fatalf("residue=%d: fixed indices differ from Go reference", residue)
				}
				if scratchBacking[0] != 0xa5 || scratchBacking[len(scratchBacking)-1] != 0xa5 || indicesBacking[0] != -1 || indicesBacking[len(indicesBacking)-1] != -1 {
					t.Fatalf("residue=%d: output canary changed", residue)
				}
			}
		})
	}

	unknown := make([]byte, 32)
	if _, ok := searchAlignedCandidates32FixedPlatform(unknown, make([]byte, count), make([]byte, count+symLenByte*31), symLenByte, make([]int, 0, count*8)); ok {
		t.Fatal("unknown preamble selected a fixed path")
	}
}

func TestSearchAlignedCandidates32FixedStress(t *testing.T) {
	if os.Getenv("RTLAMR_STRESS_NEON") == "" {
		t.Skip("set RTLAMR_STRESS_NEON=1 to run the extended fixed-preamble stress test")
	}
	if !searchAlignedCandidates32FixedAvailable() {
		t.Skip("Cortex-A72 r0p3 fixed 32-symbol search is unavailable")
	}
	const (
		iterations = 20000
		count      = 1024
		symLenByte = 18
	)
	testCases := [...]struct {
		name string
		bits string
	}{
		{name: "IDM", bits: "01010101010101010001011010100011"},
		{name: "R900", bits: "00000000000000001110010101100100"},
	}
	for caseIndex, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			preamble := make([]byte, len(testCase.bits))
			masks := make([]byte, len(testCase.bits))
			for idx, bit := range []byte(testCase.bits) {
				preamble[idx] = bit - '0'
				masks[idx] = (preamble[idx] ^ 1) * 0xff
			}
			rng := rand.New(rand.NewSource(0x3257e55a72 + int64(caseIndex)))
			wantMasks := make([]byte, count)
			wantDecoder := Decoder{sIdxA: make([]int, 0, count*8)}
			scratch := make([]byte, count)
			indices := make([]int, 0, count*8)
			for iteration := 0; iteration < iterations; iteration++ {
				packed := bytesAtCacheLineResidue(count+symLenByte*31, iteration&63)
				rng.Read(packed)
				if iteration&3 == 0 {
					qByte := rng.Intn(count)
					phase := rng.Intn(8)
					candidateBit := byte(0x80 >> uint(phase))
					for symbol, preambleBit := range preamble {
						signal := &packed[qByte+symbol*symLenByte]
						if preambleBit == 1 {
							*signal |= candidateBit
						} else {
							*signal &^= candidateBit
						}
					}
				}
				searchAlignedCandidates32Go(wantMasks, packed, symLenByte, masks)
				wantDecoder.sIdxA = wantDecoder.sIdxA[:0]
				wantIndices := wantDecoder.expandAlignedCandidates(wantMasks)
				gotIndices, ok := searchAlignedCandidates32FixedPlatform(preamble, scratch, packed, symLenByte, indices[:0])
				if !ok || !equalInts(gotIndices, wantIndices) {
					t.Fatalf("iteration=%d residue=%d: fixed result differs", iteration, iteration&63)
				}
				indices = gotIndices[:0]
			}
		})
	}
}

func benchmarkPreambleSearch(b *testing.B, fast bool) {
	const (
		blockSize    = 8192
		symbolLength = 144
		preambleBits = 32
	)
	preambleLength := preambleBits * symbolLength
	rng := rand.New(rand.NewSource(0x51a1))
	preamble := make([]byte, preambleBits)
	signal := make([]byte, blockSize+preambleLength)
	for idx := range preamble {
		preamble[idx] = byte(rng.Intn(2))
	}
	for idx := range signal {
		signal[idx] = byte(rng.Intn(2))
	}
	decoder := newSearchDecoder(signal, blockSize, symbolLength, preambleLength)

	b.ReportAllocs()
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		if fast {
			decoder.searchPackedByteAligned(preamble)
		} else {
			decoder.searchPackedLegacy(preamble)
		}
	}
}

func BenchmarkPreambleSearchLegacy(b *testing.B) {
	benchmarkPreambleSearch(b, false)
}

func BenchmarkPreambleSearchByteAligned(b *testing.B) {
	benchmarkPreambleSearch(b, true)
}

func BenchmarkSearchAlignedCandidates4(b *testing.B) {
	const (
		count      = 1024
		symLenByte = 18
	)
	rng := rand.New(rand.NewSource(0xa72))
	packed := make([]byte, count+symLenByte*3)
	rng.Read(packed)
	dst := make([]byte, count)
	testCases := [...]struct {
		name  string
		masks [4]byte
	}{
		{name: "Generic", masks: [4]byte{0xff, 0xff, 0, 0xff}},
		{name: "SCM", masks: [4]byte{}},
		{name: "SCMPlus", masks: [4]byte{0xff, 0xff, 0xff, 0}},
	}
	for _, testCase := range testCases {
		b.Run(testCase.name, func(b *testing.B) {
			b.Run("Go", func(b *testing.B) {
				b.ReportAllocs()
				for idx := 0; idx < b.N; idx++ {
					searchAlignedCandidates4Go(dst, packed, symLenByte, testCase.masks)
				}
			})

			b.Run("A72NEON", func(b *testing.B) {
				if !searchAlignedCandidates4Available() {
					b.Skip("Cortex-A72 r0p3 NEON search is unavailable")
				}
				b.ReportAllocs()
				for idx := 0; idx < b.N; idx++ {
					searchAlignedCandidates4Platform(dst, packed, symLenByte, testCase.masks)
				}
			})
		})
	}
}

func BenchmarkSearchAlignedCandidates4Alignment(b *testing.B) {
	if !searchAlignedCandidates4Available() {
		b.Skip("Cortex-A72 r0p3 NEON search is unavailable")
	}
	const (
		count      = 1024
		symLenByte = 18
	)
	testCases := [...]struct {
		name  string
		masks [4]byte
	}{
		{name: "SCM", masks: [4]byte{}},
		{name: "SCMPlus", masks: [4]byte{0xff, 0xff, 0xff, 0}},
	}
	for _, testCase := range testCases {
		for _, axis := range []string{"Packed", "Destination"} {
			for residue := 0; residue < 64; residue++ {
				name := fmt.Sprintf("%s/%s/%02d", testCase.name, axis, residue)
				b.Run(name, func(b *testing.B) {
					b.StopTimer()
					packedResidue, dstResidue := 0, 0
					if axis == "Packed" {
						packedResidue = residue
					} else {
						dstResidue = residue
					}
					packed := bytesAtCacheLineResidue(count+symLenByte*3, packedResidue)
					dst := bytesAtCacheLineResidue(count, dstResidue)
					rng := rand.New(rand.NewSource(0xa72))
					rng.Read(packed)
					if int(uintptr(unsafe.Pointer(&packed[0]))&63) != packedResidue || int(uintptr(unsafe.Pointer(&dst[0]))&63) != dstResidue {
						b.Fatal("failed to construct requested cache-line residue")
					}
					b.ReportAllocs()
					b.StartTimer()
					for idx := 0; idx < b.N; idx++ {
						searchAlignedCandidates4Platform(dst, packed, symLenByte, testCase.masks)
					}
				})
			}
		}
	}
}

func BenchmarkSearchAlignedCandidates4Mixed(b *testing.B) {
	if !searchAlignedCandidates4Available() {
		b.Skip("Cortex-A72 r0p3 NEON search is unavailable")
	}
	const (
		count      = 1024
		symLenByte = 18
	)
	packed := bytesAtCacheLineResidue(count+symLenByte*3, 0)
	dst := bytesAtCacheLineResidue(count, 0)
	rng := rand.New(rand.NewSource(0xa72))
	rng.Read(packed)
	masks := [...][4]byte{
		{},
		{0xff, 0xff, 0xff, 0},
		{0xff, 0xff, 0, 0xff},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for idx := 0; idx < b.N; idx++ {
		for _, mask := range masks {
			searchAlignedCandidates4Platform(dst, packed, symLenByte, mask)
		}
	}
}

func BenchmarkSearchAlignedCandidates4Batched(b *testing.B) {
	if !searchAlignedCandidates4Available() {
		b.Skip("Cortex-A72 r0p3 NEON search is unavailable")
	}
	const (
		count      = 1024
		symLenByte = 18
	)
	packed := bytesAtCacheLineResidue(count+symLenByte*3, 0)
	dst := bytesAtCacheLineResidue(count, 0)
	rng := rand.New(rand.NewSource(0xa72))
	rng.Read(packed)
	masks := [...][4]byte{
		{},
		{0xff, 0xff, 0xff, 0},
		{0xff, 0xff, 0, 0xff},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for _, mask := range masks {
		for idx := 0; idx < b.N; idx++ {
			searchAlignedCandidates4Platform(dst, packed, symLenByte, mask)
		}
	}
}

func bytesAtCacheLineResidue(length, residue int) []byte {
	backing := make([]byte, length+63)
	baseResidue := int(uintptr(unsafe.Pointer(&backing[0])) & 63)
	offset := (residue - baseResidue + 64) & 63
	return backing[offset : offset+length]
}

func BenchmarkSearchAlignedCandidates32(b *testing.B) {
	const count = 1024
	packed, masks, symLenByte := search32BenchmarkFixture()
	dst := make([]byte, count)

	b.Run("Go", func(b *testing.B) {
		b.ReportAllocs()
		for idx := 0; idx < b.N; idx++ {
			searchAlignedCandidates32Go(dst, packed, symLenByte, masks)
		}
	})

	b.Run("A72NEON", func(b *testing.B) {
		if !searchAlignedCandidates4Available() {
			b.Skip("Cortex-A72 r0p3 NEON search is unavailable")
		}
		indices := make([]int, 0, count*8)
		b.ReportAllocs()
		for idx := 0; idx < b.N; idx++ {
			indices = searchAlignedCandidates32Platform(dst, packed, symLenByte, masks, indices[:0])
		}
		benchmarkSearchIndices = len(indices)
	})
}

func BenchmarkSearchAlignedCandidates32FixedProduction(b *testing.B) {
	const (
		count      = 1024
		symLenByte = 18
	)
	testCases := [...]struct {
		name string
		bits string
	}{
		{name: "IDM", bits: "01010101010101010001011010100011"},
		{name: "R900", bits: "00000000000000001110010101100100"},
	}
	for caseIndex, testCase := range testCases {
		b.Run(testCase.name, func(b *testing.B) {
			preamble := make([]byte, len(testCase.bits))
			masks := make([]byte, len(testCase.bits))
			for idx, bit := range []byte(testCase.bits) {
				preamble[idx] = bit - '0'
				masks[idx] = (preamble[idx] ^ 1) * 0xff
			}
			packed := bytesAtCacheLineResidue(count+symLenByte*31, 0)
			rand.New(rand.NewSource(0x32a72 + int64(caseIndex))).Read(packed)
			scratch := make([]byte, count)
			indices := make([]int, 0, count*8)
			dispatchDecoder := Decoder{
				Cfg: PacketConfig{
					BlockSize:    count * 8,
					SymbolLength: symLenByte * 8,
				},
				packed: packed,
				sIdxA:  make([]int, 0, count*8),
			}

			b.Run("ProductionDispatch", func(b *testing.B) {
				b.ReportAllocs()
				for idx := 0; idx < b.N; idx++ {
					indices = dispatchDecoder.searchPackedByteAligned(preamble)
				}
				benchmarkSearchIndices = len(indices)
			})

			b.Run("GoComplete", func(b *testing.B) {
				decoder := Decoder{sIdxA: make([]int, 0, count*8)}
				b.ReportAllocs()
				for idx := 0; idx < b.N; idx++ {
					searchAlignedCandidates32Go(scratch, packed, symLenByte, masks)
					decoder.sIdxA = decoder.sIdxA[:0]
					indices = decoder.expandAlignedCandidates(scratch)
				}
				benchmarkSearchIndices = len(indices)
			})

			b.Run("GenericA72", func(b *testing.B) {
				if !searchAlignedCandidates4Available() {
					b.Skip("Cortex-A72 r0p3 NEON search is unavailable")
				}
				b.ReportAllocs()
				for idx := 0; idx < b.N; idx++ {
					indices = searchAlignedCandidates32Platform(scratch, packed, symLenByte, masks, indices[:0])
				}
				benchmarkSearchIndices = len(indices)
			})

			b.Run("FixedA72", func(b *testing.B) {
				if !searchAlignedCandidates32FixedAvailable() {
					b.Skip("Cortex-A72 r0p3 fixed 32-symbol search is unavailable")
				}
				b.ReportAllocs()
				for idx := 0; idx < b.N; idx++ {
					var ok bool
					indices, ok = searchAlignedCandidates32FixedPlatform(preamble, scratch, packed, symLenByte, indices[:0])
					if !ok {
						b.Fatal("fixed path was not selected")
					}
				}
				benchmarkSearchIndices = len(indices)
			})
		})
	}
}

func search32BenchmarkFixture() (packed, masks []byte, symLenByte int) {
	const (
		count = 1024
		seed  = 0x32a72
	)
	symLenByte = 18
	rng := rand.New(rand.NewSource(seed))
	packed = make([]byte, count+symLenByte*31)
	masks = make([]byte, 32)
	rng.Read(packed)
	for idx := range masks {
		masks[idx] = byte(rng.Intn(2)) * 0xff
	}
	return packed, masks, symLenByte
}

func TestSearch32BenchmarkFixtureDigest(t *testing.T) {
	packed, masks, _ := search32BenchmarkFixture()
	digest := sha256.New()
	digest.Write(packed)
	digest.Write(masks)
	got := fmt.Sprintf("%x", digest.Sum(nil))
	const want = "697785d4459b2885fb0d7d25ccdc95b53a980dec37650c217aba0bf9bf338b5b"
	if got != want {
		t.Fatalf("fixture digest = %s, want %s", got, want)
	}
}

func BenchmarkExpandAlignedCandidates(b *testing.B) {
	const count = 1024
	candidates := make([]byte, count)
	decoder := Decoder{sIdxA: make([]int, 0, count*8)}
	b.ReportAllocs()
	for idx := 0; idx < b.N; idx++ {
		decoder.sIdxA = decoder.sIdxA[:0]
		decoder.expandAlignedCandidates(candidates)
	}
}

var benchmarkSearchIndices int
