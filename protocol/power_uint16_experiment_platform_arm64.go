//go:build d3_power_neon && linux && arm64 && gc && !purego && !race

package protocol

import "unsafe"

func powerIQUint16NEONPlatform(output []uint16, input []byte) int {
	// The selected leaf consumes exactly 32 input bytes and emits exactly 32
	// output bytes per iteration. Validation has already proved sufficient,
	// non-overlapping buffers; the portable tail owns every residual sample.
	bulk := len(output) &^ 15
	if bulk == 0 {
		return 0
	}
	powerIQUint16NEONLeaf(
		unsafe.Pointer(&output[0]),
		unsafe.Pointer(&input[0]),
		bulk,
	)
	return bulk
}

// Every direct leaf below requires non-overlapping buffers and a positive
// count that is an exact multiple of its assembly quantum. The first three use
// a 16-output quantum; powerIQUint16NEONU8Mul32Leaf uses 32. They perform no
// validation and are exposed only to the native experiment tests/benchmarks.

//go:noescape
func powerIQUint16NEONLeaf(output, input unsafe.Pointer, count int)

//go:noescape
func powerIQUint16NEONUZPLeaf(output, input unsafe.Pointer, count int)

//go:noescape
func powerIQUint16NEONU8MulLeaf(output, input unsafe.Pointer, count int)

//go:noescape
func powerIQUint16NEONU8Mul32Leaf(output, input unsafe.Pointer, count int)
