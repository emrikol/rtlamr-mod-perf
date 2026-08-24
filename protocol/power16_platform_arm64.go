//go:build linux && arm64 && gc && !purego && !race

package protocol

import (
	"bytes"
	"os"
	"strings"
	"unsafe"
)

const power16FusedImplementation = "u8mul32-power-manchester-a72-v1"

func probePower16Platform() power16Platform {
	platform := power16Platform{
		implementation: power16FusedImplementation,
		midr:           power16ReadMIDR(),
		asimd:          power16DetectASIMD(),
		killSwitch:     os.Getenv("RTLAMR_DISABLE_NEON") != "" || os.Getenv("RTLAMR_DISABLE_POWER16") != "",
	}
	platform.genuineA72 = platform.midr == cortexA72R0P3MIDR
	switch {
	case platform.killSwitch:
		platform.fallbackReason = "kill-switch"
		return platform
	case !platform.asimd:
		platform.fallbackReason = "asimd-unavailable"
		return platform
	case !platform.genuineA72:
		platform.fallbackReason = "unsupported-cpu"
		return platform
	}

	platform.selfTestPassed = power16SelectedSelfTest()
	if !platform.selfTestPassed {
		platform.fallbackReason = "self-test-failed"
		return platform
	}
	platform.nativeAvailable = true
	power16SelectRun(&platform)
	return platform
}

func power16FusedA72(decisions []byte, window []uint16, input []byte) bool {
	if len(decisions) != power16BlockSize || len(window) != power16Window || len(input) != power16BlockSize*2 {
		return false
	}
	decisionStart := uintptr(unsafe.Pointer(&decisions[0]))
	decisionEnd := decisionStart + uintptr(len(decisions))
	windowStart := uintptr(unsafe.Pointer(&window[0]))
	windowEnd := windowStart + uintptr(len(window))*unsafe.Sizeof(window[0])
	inputStart := uintptr(unsafe.Pointer(&input[0]))
	inputEnd := inputStart + uintptr(len(input))
	if power16RangesOverlap(decisionStart, decisionEnd, windowStart, windowEnd) ||
		power16RangesOverlap(decisionStart, decisionEnd, inputStart, inputEnd) ||
		power16RangesOverlap(windowStart, windowEnd, inputStart, inputEnd) {
		return false
	}
	fusedPowerManchesterU8Mul32A72(
		unsafe.Pointer(&decisions[0]),
		unsafe.Pointer(&window[0]),
		unsafe.Pointer(&input[0]),
	)
	return true
}

func power16RangesOverlap(leftStart, leftEnd, rightStart, rightEnd uintptr) bool {
	return leftStart < rightEnd && rightStart < leftEnd
}

func power16ReadMIDR() string {
	midr, err := os.ReadFile("/sys/devices/system/cpu/cpu0/regs/identification/midr_el1")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(midr))
}

func power16DetectASIMD() bool {
	cpuinfo, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(cpuinfo), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || strings.TrimSuffix(fields[0], ":") != "Features" {
			continue
		}
		for _, feature := range fields[1:] {
			if feature == "asimd" {
				return true
			}
		}
	}
	return false
}

func power16FusedSelfTest() bool {
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
	gotDecisions := make([]byte, power16BlockSize)
	fusedPowerManchesterU8Mul32A72(
		unsafe.Pointer(&gotDecisions[0]),
		unsafe.Pointer(&gotWindow[0]),
		unsafe.Pointer(&input[0]),
	)
	return bytes.Equal(gotDecisions, wantDecisions) && power16Equal(gotWindow, wantWindow)
}

func power16Equal(left, right []uint16) bool {
	if len(left) != len(right) {
		return false
	}
	for idx := range left {
		if left[idx] != right[idx] {
			return false
		}
	}
	return true
}

// fusedPowerManchesterU8Mul32A72 consumes exactly 16,384 interleaved IQ
// bytes, stores 8,192 powers after 144 history powers, and emits 8,192
// decisions.
//
//go:noescape
func fusedPowerManchesterU8Mul32A72(decisions, window, iq unsafe.Pointer)
