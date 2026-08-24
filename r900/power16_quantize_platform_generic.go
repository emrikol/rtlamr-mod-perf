//go:build !linux || !arm64 || !gc || purego || race

package r900

func power16R900CurrentPlatform() power16R900Platform {
	return power16R900Platform{
		implementation: power16R900PortableImplementation,
		fallbackReason: "unsupported-platform",
	}
}
