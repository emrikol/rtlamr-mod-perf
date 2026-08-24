//go:build linux && arm64 && gc && !purego && !race

package protocol

import (
	"bytes"
	"unsafe"
)

const power16FusedPackedImplementation = "u8mul32-power-manchester-packed-a72-v1"

func power16SelectedSelfTest() bool {
	return power16FusedPackedSelfTest()
}

func power16SelectRun(platform *power16Platform) {
	// Retain the accepted runner as the safe ABI gate required by the ordinary
	// selection contract; the allocated candidate state uses runPacked.
	platform.implementation = power16FusedPackedImplementation
	platform.run = power16FusedA72
	platform.runPacked = power16FusedPackedA72
}

func power16FusedPackedA72(decisions, packed []byte, window []uint16, input []byte) bool {
	if len(decisions) != power16BlockSize || len(packed) != power16BlockSize>>3 ||
		len(window) != power16Window || len(input) != power16BlockSize*2 {
		return false
	}
	decisionStart := uintptr(unsafe.Pointer(&decisions[0]))
	decisionEnd := decisionStart + uintptr(len(decisions))
	packedStart := uintptr(unsafe.Pointer(&packed[0]))
	packedEnd := packedStart + uintptr(len(packed))
	windowStart := uintptr(unsafe.Pointer(&window[0]))
	windowEnd := windowStart + uintptr(len(window))*unsafe.Sizeof(window[0])
	inputStart := uintptr(unsafe.Pointer(&input[0]))
	inputEnd := inputStart + uintptr(len(input))
	if power16RangesOverlap(decisionStart, decisionEnd, packedStart, packedEnd) ||
		power16RangesOverlap(decisionStart, decisionEnd, windowStart, windowEnd) ||
		power16RangesOverlap(decisionStart, decisionEnd, inputStart, inputEnd) ||
		power16RangesOverlap(packedStart, packedEnd, windowStart, windowEnd) ||
		power16RangesOverlap(packedStart, packedEnd, inputStart, inputEnd) ||
		power16RangesOverlap(windowStart, windowEnd, inputStart, inputEnd) {
		return false
	}
	fusedPowerManchesterPackedU8Mul32A72(
		unsafe.Pointer(&decisions[0]),
		unsafe.Pointer(&packed[0]),
		unsafe.Pointer(&window[0]),
		unsafe.Pointer(&input[0]),
	)
	return true
}

func power16FusedPackedSelfTest() bool {
	input := make([]byte, power16BlockSize*2)
	for idx := range input {
		input[idx] = byte(idx*73 + idx/31*19 + 7)
	}
	wantWindow := make([]uint16, power16Window)
	gotWindow := make([]uint16, power16Window)
	for idx := 0; idx < power16History; idx++ {
		value := uint16((idx*977+idx/7*313+11)%(255*255) + 1)
		wantWindow[idx] = value
		gotWindow[idx] = value
	}
	wantDecisions := make([]byte, power16BlockSize)
	power16ReferenceBlock(wantDecisions, wantWindow, input)
	wantPacked := make([]byte, power16BlockSize>>3)
	packQuantized(wantPacked, wantDecisions)
	gotDecisions := make([]byte, power16BlockSize)
	gotPacked := make([]byte, power16BlockSize>>3)
	fusedPowerManchesterPackedU8Mul32A72(
		unsafe.Pointer(&gotDecisions[0]),
		unsafe.Pointer(&gotPacked[0]),
		unsafe.Pointer(&gotWindow[0]),
		unsafe.Pointer(&input[0]),
	)
	return bytes.Equal(gotDecisions, wantDecisions) &&
		bytes.Equal(gotPacked, wantPacked) && power16Equal(gotWindow, wantWindow)
}

// fusedPowerManchesterPackedU8Mul32A72 has fixed production geometry and
// writes exactly 8,192 decision bytes, 1,024 packed decision bytes, and 8,192
// Power16 samples after the 144-sample history prefix.
//
//go:noescape
func fusedPowerManchesterPackedU8Mul32A72(decisions, packed, window, iq unsafe.Pointer)

//go:noescape
func packDecision16S6A72(output, decisions unsafe.Pointer)
