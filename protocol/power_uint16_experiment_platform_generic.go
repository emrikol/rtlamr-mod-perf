//go:build d3_power_neon && (!linux || !arm64 || !gc || purego || race)

package protocol

func powerIQUint16NEONPlatform(output []uint16, input []byte) int {
	return 0
}
