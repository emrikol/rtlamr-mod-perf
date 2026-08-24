//go:build linux && arm64 && gc && !purego && !race
// +build linux,arm64,gc,!purego,!race

package protocol

import (
	"bytes"
	"io/ioutil"
	"os"
	"strings"
	"unsafe"
)

const cortexA72R0P3MIDR = "0x00000000410fd083"

var searchAlignedCandidates32IDMPreamble = [...]byte{
	0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1, 0, 1,
	0, 0, 0, 1, 0, 1, 1, 0, 1, 0, 1, 0, 0, 0, 1, 1,
}

var searchAlignedCandidates32R900Preamble = [...]byte{
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	1, 1, 1, 0, 0, 1, 0, 1, 0, 1, 1, 0, 0, 1, 0, 0,
}

var searchAlignedCandidatesSCMPreamble = [...]byte{
	1, 1, 1, 1, 1, 0, 0, 1, 0, 1, 0, 1, 0, 0, 1, 1, 0, 0, 0, 0, 0,
}

var searchAlignedCandidatesSCMPlusPreamble = [...]byte{
	0, 0, 0, 1, 0, 1, 1, 0, 1, 0, 1, 0, 0, 0, 1, 1,
}

var searchAlignedCandidates4Enabled = detectCortexA72R0P3() && searchAlignedCandidates4SelfTest() && searchAlignedCandidates32SelfTest()
var searchAlignedCandidates32FixedEnabled = detectCortexA72R0P3() && searchAlignedCandidates32FixedSelfTest()
var searchAlignedCandidates32DualFixedEnabled = searchAlignedCandidates32FixedEnabled && searchAlignedCandidates32DualFixedSelfTest()
var searchAlignedCandidates4FixedEnabled = searchAlignedCandidates4Enabled && searchAlignedCandidates4FixedSelfTest()

func searchAlignedCandidates4Available() bool {
	return searchAlignedCandidates4Enabled
}

func searchAlignedCandidates32FixedAvailable() bool {
	return searchAlignedCandidates32FixedEnabled
}

func searchAlignedCandidates32DualFixedAvailable() bool {
	return searchAlignedCandidates32DualFixedEnabled
}

func searchAlignedCandidates4FixedAvailable() bool {
	return searchAlignedCandidates4FixedEnabled
}

func searchAlignedCandidates4Platform(dst, packed []byte, symLenByte int, masks [4]byte) {
	if len(dst) == 0 {
		return
	}
	if len(dst)&15 != 0 || symLenByte <= 0 || len(packed) < len(dst)+symLenByte*3 {
		panic("protocol: invalid ARM64 preamble-search buffers")
	}
	searchAlignedCandidates4A72(
		unsafe.Pointer(&dst[0]),
		unsafe.Pointer(&packed[0]),
		unsafe.Pointer(&masks[0]),
		symLenByte,
		len(dst),
	)
}

// searchAlignedCandidates4FixedPlatform selects the complete fixed SCM and
// SCM+ preambles after inexpensive length partitioning in the caller. Disabled
// protocols never reach this function because only registered preambles are
// searched.
func searchAlignedCandidates4FixedPlatform(preamble, dst, packed []byte, symLenByte int, indices []int) ([]int, bool) {
	count := len(dst)
	if !searchAlignedCandidates4FixedEnabled || (count != 512 && count != 1024) || symLenByte != 18 || cap(indices) < count*8 {
		return indices[:0], false
	}
	var masks [4]byte
	var tailKind uint8
	switch {
	case len(preamble) == len(searchAlignedCandidatesSCMPreamble) && bytes.Equal(preamble, searchAlignedCandidatesSCMPreamble[:]):
		tailKind = 1
	case len(preamble) == len(searchAlignedCandidatesSCMPlusPreamble) && bytes.Equal(preamble, searchAlignedCandidatesSCMPlusPreamble[:]):
		masks = [4]byte{0xff, 0xff, 0xff, 0}
		tailKind = 2
	default:
		return indices[:0], false
	}
	if len(packed) < count+symLenByte*(len(preamble)-1) {
		return indices[:0], false
	}
	searchAlignedCandidates4A72(
		unsafe.Pointer(&dst[0]),
		unsafe.Pointer(&packed[0]),
		unsafe.Pointer(&masks[0]),
		symLenByte,
		count,
	)
	indices = indices[:cap(indices)]
	var n int
	if tailKind == 1 {
		n = searchAlignedCandidatesSCMTailA72(
			unsafe.Pointer(&dst[0]),
			unsafe.Pointer(&packed[0]),
			unsafe.Pointer(&indices[0]),
			count,
		)
	} else {
		n = searchAlignedCandidatesSCMPlusTailA72(
			unsafe.Pointer(&dst[0]),
			unsafe.Pointer(&packed[0]),
			unsafe.Pointer(&indices[0]),
			count,
		)
	}
	return indices[:n], true
}

func searchAlignedCandidates32Platform(dst, packed []byte, symLenByte int, masks []byte, indices []int) []int {
	if len(dst) == 0 {
		return indices[:0]
	}
	if len(dst)&15 != 0 || len(masks) != 32 || symLenByte <= 0 || len(packed) < len(dst)+symLenByte*31 || cap(indices) < len(dst)*8 {
		panic("protocol: invalid ARM64 32-symbol search buffers")
	}
	indices = indices[:cap(indices)]
	n := searchAlignedCandidates32A72(
		unsafe.Pointer(&dst[0]),
		unsafe.Pointer(&packed[0]),
		unsafe.Pointer(&masks[0]),
		unsafe.Pointer(&indices[0]),
		symLenByte,
		len(dst),
	)
	return indices[:n]
}

// searchAlignedCandidates32FixedPlatform selects the two fixed 32-symbol
// preambles used by the fixed IDM/R900 geometry. These leaves return ordered indices directly and
// use dst only as private scratch for the rare surviving 64-byte group; unlike
// the arbitrary-mask fallback, dst is not a complete candidate-mask result.
func searchAlignedCandidates32FixedPlatform(preamble, dst, packed []byte, symLenByte int, indices []int) ([]int, bool) {
	const count = 1024
	if !searchAlignedCandidates32FixedEnabled || len(dst) != count || symLenByte != 18 || len(packed) < count+18*31 || cap(indices) < count*8 {
		return indices[:0], false
	}
	indices = indices[:cap(indices)]
	var n int
	switch {
	case bytes.Equal(preamble, searchAlignedCandidates32IDMPreamble[:]):
		n = searchAlignedCandidates32IDMA72(
			unsafe.Pointer(&dst[0]),
			unsafe.Pointer(&packed[0]),
			unsafe.Pointer(&dst[0]),
			unsafe.Pointer(&indices[0]),
			symLenByte,
			count,
		)
	case bytes.Equal(preamble, searchAlignedCandidates32R900Preamble[:]):
		n = searchAlignedCandidates32R900A72(
			unsafe.Pointer(&dst[0]),
			unsafe.Pointer(&packed[0]),
			unsafe.Pointer(&dst[0]),
			unsafe.Pointer(&indices[0]),
			symLenByte,
			count,
		)
	default:
		return indices[:0], false
	}
	return indices[:n], true
}

// searchAlignedCandidates32DualFixedPlatform searches the fixed IDM and R900
// families together. Their first eight symbols form two disjoint masks, so the
// native leaf retains one protocol tag while filtering a shared candidate
// union. Callers use this only when both preamble families are registered.
func searchAlignedCandidates32DualFixedPlatform(packed []byte, symLenByte int, idmIndices, r900Indices []int) ([]int, []int, bool) {
	const count = 1024
	if !searchAlignedCandidates32DualFixedEnabled || symLenByte != 18 || len(packed) < count+18*31 || cap(idmIndices) < count*8 || cap(r900Indices) < count*8 {
		return idmIndices[:0], r900Indices[:0], false
	}
	idmIndices = idmIndices[:cap(idmIndices)]
	r900Indices = r900Indices[:cap(r900Indices)]
	idmN, r900N := searchAlignedCandidates32IDMR900A72(
		unsafe.Pointer(&packed[0]),
		unsafe.Pointer(&idmIndices[0]),
		unsafe.Pointer(&r900Indices[0]),
		count,
	)
	return idmIndices[:idmN], r900Indices[:r900N], true
}

func detectCortexA72R0P3() bool {
	if os.Getenv("RTLAMR_DISABLE_NEON") != "" {
		return false
	}
	midr, err := ioutil.ReadFile("/sys/devices/system/cpu/cpu0/regs/identification/midr_el1")
	if err != nil || strings.TrimSpace(string(midr)) != cortexA72R0P3MIDR {
		return false
	}
	cpuinfo, err := ioutil.ReadFile("/proc/cpuinfo")
	return err == nil && strings.Contains(string(cpuinfo), "Features\t: fp asimd")
}

func searchAlignedCandidates4SelfTest() bool {
	cases := [...]struct {
		count      int
		symLenByte int
		masks      [4]byte
	}{
		{count: 32, symLenByte: 7, masks: [4]byte{0xff, 0, 0xff, 0}},
		{count: 1024, symLenByte: 18, masks: [4]byte{}},
		{count: 1024, symLenByte: 18, masks: [4]byte{0xff, 0xff, 0xff, 0}},
	}
	for _, testCase := range cases {
		packed := make([]byte, testCase.count+testCase.symLenByte*3)
		for idx := range packed {
			packed[idx] = byte(idx*73 + 19)
		}
		want := make([]byte, testCase.count)
		got := make([]byte, testCase.count)
		searchAlignedCandidates4Go(want, packed, testCase.symLenByte, testCase.masks)
		searchAlignedCandidates4A72(
			unsafe.Pointer(&got[0]),
			unsafe.Pointer(&packed[0]),
			unsafe.Pointer(&testCase.masks[0]),
			testCase.symLenByte,
			testCase.count,
		)
		if !bytes.Equal(got, want) {
			return false
		}
	}
	return true
}

func searchAlignedCandidates4FixedSelfTest() bool {
	testCases := [...]struct {
		preamble []byte
		masks    [4]byte
		tail     func(masks, packed, indices unsafe.Pointer, count int) int
	}{
		{
			preamble: searchAlignedCandidatesSCMPreamble[:],
			tail:     searchAlignedCandidatesSCMTailA72,
		},
		{
			preamble: searchAlignedCandidatesSCMPlusPreamble[:],
			masks:    [4]byte{0xff, 0xff, 0xff, 0},
			tail:     searchAlignedCandidatesSCMPlusTailA72,
		},
	}
	for _, count := range [...]int{512, 1024} {
		for caseIndex, testCase := range testCases {
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
			n := testCase.tail(
				unsafe.Pointer(&prefixMasks[0]),
				unsafe.Pointer(&packed[0]),
				unsafe.Pointer(&got[0]),
				count,
			)
			if n != len(want) {
				return false
			}
			for idx := range want {
				if got[idx] != want[idx] {
					return false
				}
			}
		}
	}
	return true
}

func searchAlignedCandidates32SelfTest() bool {
	const (
		count      = 32
		symLenByte = 18
	)
	packed := make([]byte, count+symLenByte*31)
	for idx := range packed {
		packed[idx] = byte(idx*73 + 19)
	}
	masks := make([]byte, 32)
	for idx := range masks {
		masks[idx] = byte((idx&1)^1) * 0xff
		if masks[idx] == 0 {
			packed[idx*symLenByte] |= 0x80
			packed[idx*symLenByte+15] |= 0x01
		} else {
			packed[idx*symLenByte] &^= 0x80
			packed[idx*symLenByte+15] &^= 0x01
		}
	}
	want := make([]byte, count)
	got := make([]byte, count)
	searchAlignedCandidates32Go(want, packed, symLenByte, masks)
	wantDecoder := Decoder{sIdxA: make([]int, 0, count*8)}
	wantIndices := wantDecoder.expandAlignedCandidates(want)
	gotIndices := make([]int, count*8)
	n := searchAlignedCandidates32A72(
		unsafe.Pointer(&got[0]),
		unsafe.Pointer(&packed[0]),
		unsafe.Pointer(&masks[0]),
		unsafe.Pointer(&gotIndices[0]),
		symLenByte,
		count,
	)
	if !bytes.Equal(got, want) || n != len(wantIndices) {
		return false
	}
	for idx := range wantIndices {
		if gotIndices[idx] != wantIndices[idx] {
			return false
		}
	}
	return true
}

func searchAlignedCandidates32FixedSelfTest() bool {
	const (
		count      = 1024
		symLenByte = 18
	)
	testCases := [...]struct {
		preamble []byte
		leaf     func(dst, packed, masks, indices unsafe.Pointer, symLenByte, count int) int
	}{
		{preamble: searchAlignedCandidates32IDMPreamble[:], leaf: searchAlignedCandidates32IDMA72},
		{preamble: searchAlignedCandidates32R900Preamble[:], leaf: searchAlignedCandidates32R900A72},
	}
	for caseIndex, testCase := range testCases {
		packed := make([]byte, count+symLenByte*31)
		for idx := range packed {
			packed[idx] = byte(idx*73 + caseIndex*41 + 19)
		}
		for _, candidate := range [...]struct{ qByte, phase int }{{0, 0}, {63, 7}, {1023, 4}} {
			bit := byte(0x80 >> uint(candidate.phase))
			for symbol, preambleBit := range testCase.preamble {
				signal := &packed[candidate.qByte+symbol*symLenByte]
				if preambleBit == 1 {
					*signal |= bit
				} else {
					*signal &^= bit
				}
			}
		}
		masks := make([]byte, 32)
		for idx, preambleBit := range testCase.preamble {
			masks[idx] = (preambleBit ^ 1) * 0xff
		}
		wantMasks := make([]byte, count)
		searchAlignedCandidates32Go(wantMasks, packed, symLenByte, masks)
		wantDecoder := Decoder{sIdxA: make([]int, 0, count*8)}
		wantIndices := wantDecoder.expandAlignedCandidates(wantMasks)
		scratch := make([]byte, count)
		gotIndices := make([]int, count*8)
		n := testCase.leaf(
			unsafe.Pointer(&scratch[0]),
			unsafe.Pointer(&packed[0]),
			unsafe.Pointer(&scratch[0]),
			unsafe.Pointer(&gotIndices[0]),
			symLenByte,
			count,
		)
		if n != len(wantIndices) {
			return false
		}
		for idx := range wantIndices {
			if gotIndices[idx] != wantIndices[idx] {
				return false
			}
		}
	}
	return true
}

func searchAlignedCandidates32DualFixedSelfTest() bool {
	const (
		count      = 1024
		symLenByte = 18
	)
	packed := make([]byte, count+symLenByte*31)
	for idx := range packed {
		packed[idx] = byte(idx*73 + idx/17*29 + 11)
	}
	for _, testCase := range [...]struct {
		preamble []byte
		qByte    int
		phase    int
	}{
		{preamble: searchAlignedCandidates32IDMPreamble[:], qByte: 0, phase: 0},
		{preamble: searchAlignedCandidates32IDMPreamble[:], qByte: 63, phase: 7},
		{preamble: searchAlignedCandidates32R900Preamble[:], qByte: 1023, phase: 4},
		{preamble: searchAlignedCandidates32R900Preamble[:], qByte: 511, phase: 3},
	} {
		bit := byte(0x80 >> uint(testCase.phase))
		for symbol, preambleBit := range testCase.preamble {
			signal := &packed[testCase.qByte+symbol*symLenByte]
			if preambleBit == 1 {
				*signal |= bit
			} else {
				*signal &^= bit
			}
		}
	}
	scratch := make([]byte, count)
	wantIDM := make([]int, count*8)
	wantR900 := make([]int, count*8)
	gotIDM := make([]int, count*8)
	gotR900 := make([]int, count*8)
	wantIDMN := searchAlignedCandidates32IDMA72(unsafe.Pointer(&scratch[0]), unsafe.Pointer(&packed[0]), unsafe.Pointer(&scratch[0]), unsafe.Pointer(&wantIDM[0]), symLenByte, count)
	wantR900N := searchAlignedCandidates32R900A72(unsafe.Pointer(&scratch[0]), unsafe.Pointer(&packed[0]), unsafe.Pointer(&scratch[0]), unsafe.Pointer(&wantR900[0]), symLenByte, count)
	gotIDMN, gotR900N := searchAlignedCandidates32IDMR900A72(unsafe.Pointer(&packed[0]), unsafe.Pointer(&gotIDM[0]), unsafe.Pointer(&gotR900[0]), count)
	if gotIDMN != wantIDMN || gotR900N != wantR900N {
		return false
	}
	for idx := 0; idx < wantIDMN; idx++ {
		if gotIDM[idx] != wantIDM[idx] {
			return false
		}
	}
	for idx := 0; idx < wantR900N; idx++ {
		if gotR900[idx] != wantR900[idx] {
			return false
		}
	}
	return true
}

//go:noescape
func searchAlignedCandidates4A72(dst, packed, masks unsafe.Pointer, symLenByte, count int)

//go:noescape
func searchAlignedCandidatesSCMTailA72(masks, packed, indices unsafe.Pointer, count int) int

//go:noescape
func searchAlignedCandidatesSCMPlusTailA72(masks, packed, indices unsafe.Pointer, count int) int

//go:noescape
func searchAlignedCandidates32A72(dst, packed, masks, indices unsafe.Pointer, symLenByte, count int) int

//go:noescape
func searchAlignedCandidates32IDMA72(dst, packed, masks, indices unsafe.Pointer, symLenByte, count int) int

//go:noescape
func searchAlignedCandidates32R900A72(dst, packed, masks, indices unsafe.Pointer, symLenByte, count int) int

//go:noescape
func searchAlignedCandidates32IDMR900A72(packed, idmIndices, r900Indices unsafe.Pointer, count int) (idmN, r900N int)
