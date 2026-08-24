//go:build !linux || !arm64 || !gc || purego || race
// +build !linux !arm64 !gc purego race

package protocol

func filterManchesterA72Available() bool {
	return false
}

func filterManchesterA72Platform(input []float64, output []byte, chipLength int) bool {
	return false
}
