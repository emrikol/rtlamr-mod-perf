//go:build !linux || !arm64 || !gc || purego || race

package protocol

func probePower16Platform() power16Platform {
	return power16Platform{
		implementation: power16FloatImplementation,
		fallbackReason: "unsupported-platform",
	}
}
