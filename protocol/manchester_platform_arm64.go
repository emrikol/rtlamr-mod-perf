//go:build linux && arm64 && gc && !purego && !race
// +build linux,arm64,gc,!purego,!race

package protocol

import (
	"math"
	"unsafe"
)

const (
	filterManchesterA72ChipLength = 72
	filterManchesterA72OutputSize = 8192
	filterManchesterA72InputSize  = filterManchesterA72OutputSize + 2*filterManchesterA72ChipLength
)

var filterManchesterA72Enabled = detectCortexA72R0P3() && filterManchesterA72SelfTest()

func filterManchesterA72Available() bool {
	return filterManchesterA72Enabled
}

// filterManchesterA72Platform selects the fixed production leaf only after
// proving its complete geometry and that output writes cannot alter input
// values that the portable sequential loop would subsequently read.
func filterManchesterA72Platform(input []float64, output []byte, chipLength int) bool {
	if !filterManchesterA72Enabled ||
		chipLength != filterManchesterA72ChipLength ||
		len(output) != filterManchesterA72OutputSize ||
		len(input) < filterManchesterA72InputSize {
		return false
	}

	inputStart := uintptr(unsafe.Pointer(&input[0]))
	inputEnd := inputStart + uintptr(filterManchesterA72InputSize)*unsafe.Sizeof(input[0])
	outputStart := uintptr(unsafe.Pointer(&output[0]))
	outputEnd := outputStart + uintptr(len(output))*unsafe.Sizeof(output[0])
	if outputStart < inputEnd && inputStart < outputEnd {
		return false
	}

	filterManchesterA72TBXSSRABalanced(unsafe.Pointer(&output[0]), unsafe.Pointer(&input[0]))
	return true
}

func filterManchesterA72SelfTest() bool {
	testCases := [2][]float64{
		make([]float64, filterManchesterA72InputSize),
		make([]float64, filterManchesterA72InputSize),
	}

	lut := NewMagLUT()
	for idx := range testCases[0] {
		i := byte(idx*73 + idx/17*29 + 11)
		q := byte(idx*37 + idx/31*19 + 7)
		testCases[0][idx] = lut[i] + lut[q]
	}

	patterns := [...]uint64{
		0x0000000000000000, 0x8000000000000000,
		0x3ff0000000000000, 0xbff0000000000000,
		0x0010000000000000, 0x8010000000000000,
		0x0000000000000001, 0x8000000000000001,
		0x7ff0000000000000, 0xfff0000000000000,
		0x7ff8000000000001, 0xfff8000000000042,
		0x7ff0000000000001, 0xfff0000000001234,
		0x400921fb54442d18, 0xc005bf0a8b145769,
	}
	for idx := range testCases[1] {
		testCases[1][idx] = math.Float64frombits(patterns[(idx*13+idx/23)&(len(patterns)-1)])
	}

	for _, input := range testCases {
		want := make([]byte, filterManchesterA72OutputSize)
		got := make([]byte, filterManchesterA72OutputSize)
		filterManchesterA72Reference(input, want, filterManchesterA72ChipLength)
		filterManchesterA72TBXSSRABalanced(unsafe.Pointer(&got[0]), unsafe.Pointer(&input[0]))
		for idx := range want {
			if got[idx] != want[idx] {
				return false
			}
		}
	}
	return true
}

// Keep the self-test independent of platform dispatch so initialization can
// compare the native leaf with the unchanged literal recurrence.
func filterManchesterA72Reference(input []float64, output []byte, chipLength int) {
	var lower, upper float64
	for idx := 0; idx < chipLength; idx++ {
		lower += input[idx]
		upper += input[idx+chipLength]
	}
	for idx := range output {
		f := lower - upper
		output[idx] = 1 - byte(math.Float64bits(f)>>63)
		lower += input[idx+chipLength] - input[idx]
		upper += input[idx+2*chipLength] - input[idx+chipLength]
	}
}

//go:noescape
func filterManchesterA72TBXSSRABalanced(output, input unsafe.Pointer)
