//go:build !linux || !arm64 || !gc || purego || race
// +build !linux !arm64 !gc purego race

package protocol

func magnitudeLUTA72Available() bool {
	return false
}

func magnitudeLUTA72Platform(output []float64, input []byte, lut []float64) {
	panic("protocol: unavailable ARM64 magnitude kernel called")
}
