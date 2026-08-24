//go:build d3_power_neon

package protocol

import "unsafe"

// powerIQUint16 converts interleaved unsigned IQ bytes to the exact scaled
// integer power described by D2:
//
//	a = 2*x - 255
//	P = (aI*aI + aQ*aQ) / 2
//
// Both centered values are odd, so the division is exact. P is in [1, 65025]
// and therefore fits uint16. The input may contain trailing bytes; output
// length determines the number of IQ pairs consumed. Input and output must not
// overlap: expanding one IQ pair from two bytes to one uint16 would otherwise
// overwrite unread input for some legal slice layouts.
func powerIQUint16(output []uint16, input []byte) {
	validatePowerIQUint16Buffers(output, input)
	if len(output) == 0 {
		return
	}

	bulk := powerIQUint16NEONPlatform(output, input)
	powerIQUint16Go(output[bulk:], input[bulk*2:])
}

// powerIQUint16Go is the portable control and tail implementation. Keep it
// separate from the test oracle: the oracle uses wider arithmetic and checks
// the exact-division and range invariants independently.
func powerIQUint16Go(output []uint16, input []byte) {
	validatePowerIQUint16Buffers(output, input)
	for idx := range output {
		i := int32(input[idx*2])*2 - 255
		q := int32(input[idx*2+1])*2 - 255
		output[idx] = uint16((i*i + q*q) >> 1)
	}
}

func validatePowerIQUint16Buffers(output []uint16, input []byte) {
	// Division avoids overflowing an intermediate len(output)*2.
	if len(output) > len(input)/2 {
		panic("protocol: integer-power input shorter than output")
	}
	if len(output) == 0 {
		return
	}

	outputStart := uintptr(unsafe.Pointer(&output[0]))
	outputEnd := outputStart + uintptr(len(output))*unsafe.Sizeof(output[0])
	inputStart := uintptr(unsafe.Pointer(&input[0]))
	inputEnd := inputStart + uintptr(len(output)*2)
	if outputStart < inputEnd && inputStart < outputEnd {
		panic("protocol: integer-power input and output overlap")
	}
}
