//go:build linux && arm64 && gc && !purego && !race
// +build linux,arm64,gc,!purego,!race

package protocol

import (
	"bytes"
	"os"
	"strconv"
	"syscall"
	"testing"
	"unsafe"
)

// TestSearchAlignedCandidates4A72DoesNotCrossGuardPages proves that the
// generic production geometry and both fixed-preamble specializations stop at
// the exact caller-proved input and output boundaries.
func TestSearchAlignedCandidates4A72DoesNotCrossGuardPages(t *testing.T) {
	if !searchAlignedCandidates4Available() {
		t.Skip("Cortex-A72 r0p3 NEON search is unavailable")
	}
	const (
		count  = 1024
		stride = 18
	)
	packedMapping, packed := guardTerminatedBytes(t, count+stride*3)
	defer syscall.Munmap(packedMapping)
	dstMapping, dst := guardTerminatedBytes(t, count)
	defer syscall.Munmap(dstMapping)
	for idx := range packed {
		packed[idx] = byte(idx*73 + idx/17*29 + 11)
	}

	testCases := [...]struct {
		name  string
		masks [4]byte
	}{
		{name: "SCM", masks: [4]byte{}},
		{name: "SCMPlus", masks: [4]byte{0xff, 0xff, 0xff, 0}},
		{name: "Generic", masks: [4]byte{0xff, 0, 0xff, 0}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			want := make([]byte, count)
			searchAlignedCandidates4Go(want, packed, stride, testCase.masks)
			searchAlignedCandidates4Platform(dst, packed, stride, testCase.masks)
			if !bytes.Equal(dst, want) {
				t.Fatal("guard-page result differs from Go oracle")
			}
		})
	}
}

// TestSearchAlignedCandidates32FixedA72DoesNotCrossGuardPages places the exact
// production input and scratch extents immediately before inaccessible pages.

func TestSearchAlignedCandidates4FixedTailDirectMatchesGo(t *testing.T) {
	if !searchAlignedCandidates4Available() {
		t.Skip("Cortex-A72 r0p3 search is unavailable")
	}
	testCases := [...]struct {
		name     string
		preamble []byte
		masks    [4]byte
		tail     func(masks, packed, indices unsafe.Pointer, count int) int
	}{
		{name: "SCM", preamble: searchAlignedCandidatesSCMPreamble[:], tail: searchAlignedCandidatesSCMTailA72},
		{name: "SCMPlus", preamble: searchAlignedCandidatesSCMPlusPreamble[:], masks: [4]byte{0xff, 0xff, 0xff, 0}, tail: searchAlignedCandidatesSCMPlusTailA72},
	}
	for _, count := range [...]int{512, 1024} {
		for caseIndex, testCase := range testCases {
			t.Run(testCase.name+"/count="+strconv.Itoa(count), func(t *testing.T) {
				packed := make([]byte, count+18*(len(testCase.preamble)-1))
				for idx := range packed {
					packed[idx] = byte(idx*73 + idx/17*29 + caseIndex*41 + 11)
				}
				for _, candidate := range [...]struct{ qByte, phase int }{{0, 0}, {count - 1, 7}} {
					bit := byte(0x80 >> uint(candidate.phase))
					for symbol, preambleBit := range testCase.preamble {
						signal := &packed[candidate.qByte+symbol*18]
						if preambleBit == 1 {
							*signal |= bit
						} else {
							*signal &^= bit
						}
					}
				}
				prefixMasks := make([]byte, count)
				searchAlignedCandidates4Go(prefixMasks, packed, 18, testCase.masks)
				wantDecoder := Decoder{packed: packed, sIdxA: make([]int, 0, count*8)}
				want := wantDecoder.finishAlignedCandidates(testCase.preamble, 18, prefixMasks)
				got := make([]int, count*8)
				n := testCase.tail(unsafe.Pointer(&prefixMasks[0]), unsafe.Pointer(&packed[0]), unsafe.Pointer(&got[0]), count)
				if n != len(want) {
					t.Fatalf("indices=%d, want=%d", n, len(want))
				}
				for idx := range want {
					if got[idx] != want[idx] {
						t.Fatalf("index %d=%d, want=%d", idx, got[idx], want[idx])
					}
				}
			})
		}
	}
}

func TestSearchAlignedCandidates4FixedA72MatchesGo(t *testing.T) {
	if !searchAlignedCandidates4FixedAvailable() {
		t.Skip("Cortex-A72 r0p3 fixed SCM search is unavailable")
	}
	iterations := 2000
	if os.Getenv("RTLAMR_STRESS_NEON") != "" {
		iterations = 20000
	}
	testCases := [...]struct {
		preamble []byte
		masks    [4]byte
	}{
		{preamble: searchAlignedCandidatesSCMPreamble[:]},
		{
			preamble: searchAlignedCandidatesSCMPlusPreamble[:],
			masks:    [4]byte{0xff, 0xff, 0xff, 0},
		},
	}
	state := uint64(0x9e3779b97f4a7c15)
	for iteration := 0; iteration < iterations; iteration++ {
		testCase := testCases[iteration&1]
		count := 512
		if iteration&2 != 0 {
			count = 1024
		}
		packed := bytesAtCacheLineResidue(count+18*(len(testCase.preamble)-1), iteration&63)
		for idx := range packed {
			state ^= state << 13
			state ^= state >> 7
			state ^= state << 17
			packed[idx] = byte(state)
		}
		for _, candidate := range [...]struct{ qByte, phase int }{
			{qByte: 0, phase: iteration & 7},
			{qByte: count - 1, phase: 7 - (iteration & 7)},
		} {
			bit := byte(0x80 >> uint(candidate.phase))
			for symbol, preambleBit := range testCase.preamble {
				signal := &packed[candidate.qByte+symbol*18]
				if preambleBit == 1 {
					*signal |= bit
				} else {
					*signal &^= bit
				}
			}
		}
		wantMasks := make([]byte, count)
		searchAlignedCandidates4Go(wantMasks, packed, 18, testCase.masks)
		wantDecoder := Decoder{packed: packed, sIdxA: make([]int, 0, count*8)}
		want := wantDecoder.finishAlignedCandidates(testCase.preamble, 18, wantMasks)
		scratch := bytesAtCacheLineResidue(count, (iteration*17)&63)
		got, ok := searchAlignedCandidates4FixedPlatform(testCase.preamble, scratch, packed, 18, make([]int, 0, count*8))
		if !ok || !equalInts(got, want) {
			t.Fatalf("iteration=%d count=%d result differs", iteration, count)
		}
	}
}

func TestSearchAlignedCandidates4FixedA72DoesNotCrossGuardPages(t *testing.T) {
	if !searchAlignedCandidates4FixedAvailable() {
		t.Skip("Cortex-A72 r0p3 fixed SCM search is unavailable")
	}
	testCases := [...]struct {
		name     string
		preamble []byte
	}{
		{name: "SCM", preamble: searchAlignedCandidatesSCMPreamble[:]},
		{name: "SCMPlus", preamble: searchAlignedCandidatesSCMPlusPreamble[:]},
	}
	for _, count := range [...]int{512, 1024} {
		for _, testCase := range testCases {
			t.Run(testCase.name+"/count="+strconv.Itoa(count), func(t *testing.T) {
				const stride = 18
				packedMapping, packed := guardTerminatedBytes(t, count+stride*(len(testCase.preamble)-1))
				defer syscall.Munmap(packedMapping)
				scratchMapping, scratch := guardTerminatedBytes(t, count)
				defer syscall.Munmap(scratchMapping)
				indexMapping, indexBytes := guardTerminatedBytes(t, count*8*8)
				defer syscall.Munmap(indexMapping)
				for idx := range packed {
					packed[idx] = byte(idx*73 + idx/17*29 + 11)
				}
				candidateBit := byte(0x01)
				for symbol, preambleBit := range testCase.preamble {
					signal := &packed[count-1+symbol*stride]
					if preambleBit == 1 {
						*signal |= candidateBit
					} else {
						*signal &^= candidateBit
					}
				}
				var masks [4]byte
				for idx := 0; idx < 4; idx++ {
					masks[idx] = (testCase.preamble[idx] ^ 1) * 0xff
				}
				wantMasks := make([]byte, count)
				searchAlignedCandidates4Go(wantMasks, packed, stride, masks)
				wantDecoder := Decoder{packed: packed, sIdxA: make([]int, 0, count*8)}
				want := wantDecoder.finishAlignedCandidates(testCase.preamble, stride, wantMasks)
				indices := (*[1024 * 8]int)(unsafe.Pointer(&indexBytes[0]))[: 0 : count*8]
				got, ok := searchAlignedCandidates4FixedPlatform(testCase.preamble, scratch, packed, stride, indices)
				if !ok || !equalInts(got, want) {
					t.Fatal("guard-page result differs from Go oracle")
				}
			})
		}
	}
}

func TestSearchAlignedCandidates32FixedA72DoesNotCrossGuardPages(t *testing.T) {
	if !searchAlignedCandidates32FixedAvailable() {
		t.Skip("Cortex-A72 r0p3 fixed 32-symbol search is unavailable")
	}
	const (
		count  = 1024
		stride = 18
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
			packedMapping, packed := guardTerminatedBytes(t, count+stride*31)
			defer syscall.Munmap(packedMapping)
			scratchMapping, scratch := guardTerminatedBytes(t, count)
			defer syscall.Munmap(scratchMapping)
			for idx := range packed {
				packed[idx] = byte(idx*73 + idx/17*29 + 11)
			}
			preamble := make([]byte, len(testCase.bits))
			masks := make([]byte, len(testCase.bits))
			for idx, bit := range []byte(testCase.bits) {
				preamble[idx] = bit - '0'
				masks[idx] = (preamble[idx] ^ 1) * 0xff
			}
			candidateBit := byte(0x01)
			for symbol, preambleBit := range preamble {
				signal := &packed[count-1+symbol*stride]
				if preambleBit == 1 {
					*signal |= candidateBit
				} else {
					*signal &^= candidateBit
				}
			}

			wantMasks := make([]byte, count)
			searchAlignedCandidates32Go(wantMasks, packed, stride, masks)
			wantDecoder := Decoder{sIdxA: make([]int, 0, count*8)}
			wantIndices := wantDecoder.expandAlignedCandidates(wantMasks)
			gotIndices, ok := searchAlignedCandidates32FixedPlatform(preamble, scratch, packed, stride, make([]int, 0, count*8))
			if !ok || !equalInts(gotIndices, wantIndices) {
				t.Fatal("guard-page result differs from Go oracle")
			}
		})
	}
}
