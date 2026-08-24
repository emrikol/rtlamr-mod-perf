//go:build linux && arm64 && gc && !purego && !race
// +build linux,arm64,gc,!purego,!race

package protocol

import (
	"math"
	"unsafe"
)

var magnitudeLUTA72Enabled = detectCortexA72R0P3() && magnitudeLUTA72SelfTest()

func magnitudeLUTA72Available() bool {
	return magnitudeLUTA72Enabled
}

func magnitudeLUTA72Platform(output []float64, input []byte, lut []float64) {
	if len(output) == 0 {
		return
	}
	if len(output)&7 != 0 || len(input) < len(output)*2 || len(lut) < 256 {
		panic("protocol: invalid ARM64 magnitude buffers")
	}
	// MagLUT is an exported slice type, so callers can legally use part of the
	// lookup table as output. Preserve the portable loop's sequential aliasing
	// behavior instead of letting the unrolled kernel overwrite future loads.
	outputStart := uintptr(unsafe.Pointer(&output[0]))
	outputEnd := outputStart + uintptr(len(output))*unsafe.Sizeof(output[0])
	lutStart := uintptr(unsafe.Pointer(&lut[0]))
	lutEnd := lutStart + 256*unsafe.Sizeof(lut[0])
	if outputStart < lutEnd && lutStart < outputEnd {
		magnitudeLUTGo(input, output, lut)
		return
	}
	magnitudeLUTFloat64A72(
		unsafe.Pointer(&output[0]),
		unsafe.Pointer(&input[0]),
		unsafe.Pointer(&lut[0]),
		len(output),
	)
}

func magnitudeLUTA72SelfTest() bool {
	const count = 256 * 256
	input := make([]byte, count*2)
	idx := 0
	for i := 0; i < 256; i++ {
		for q := 0; q < 256; q++ {
			input[idx*2] = byte(i)
			input[idx*2+1] = byte(q)
			idx++
		}
	}

	lut := NewMagLUT()
	want := make([]float64, count)
	got := make([]float64, count)
	magnitudeLUTGo(input, want, lut)
	magnitudeLUTFloat64A72(
		unsafe.Pointer(&got[0]),
		unsafe.Pointer(&input[0]),
		unsafe.Pointer(&lut[0]),
		count,
	)
	for idx := range want {
		if math.Float64bits(got[idx]) != math.Float64bits(want[idx]) {
			return false
		}
	}
	return true
}

//go:noescape
func magnitudeLUTFloat64A72(output, input, lut unsafe.Pointer, count int)
