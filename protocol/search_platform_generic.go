//go:build !linux || !arm64 || !gc || purego || race
// +build !linux !arm64 !gc purego race

package protocol

func searchAlignedCandidates4Available() bool {
	return false
}

func searchAlignedCandidates32FixedAvailable() bool {
	return false
}

func searchAlignedCandidates32DualFixedAvailable() bool {
	return false
}

func searchAlignedCandidates4Platform(dst, packed []byte, symLenByte int, masks [4]byte) {
	panic("protocol: unavailable ARM64 preamble search called")
}

func searchAlignedCandidates4FixedPlatform(preamble, dst, packed []byte, symLenByte int, indices []int) ([]int, bool) {
	return indices[:0], false
}

func searchAlignedCandidates32Platform(dst, packed []byte, symLenByte int, masks []byte, indices []int) []int {
	panic("protocol: unavailable ARM64 preamble search called")
}

func searchAlignedCandidates32FixedPlatform(preamble, dst, packed []byte, symLenByte int, indices []int) ([]int, bool) {
	return indices[:0], false
}

func searchAlignedCandidates32DualFixedPlatform(packed []byte, symLenByte int, idmIndices, r900Indices []int) ([]int, []int, bool) {
	return idmIndices[:0], r900Indices[:0], false
}
