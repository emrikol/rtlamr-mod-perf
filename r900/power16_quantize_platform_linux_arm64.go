//go:build linux && arm64 && gc && !purego && !race

package r900

import (
	"os"
	"strings"
	"sync"
	"unsafe"
)

const power16R900A72MIDR = "0x00000000410fd083"

var (
	power16R900PlatformOnce     sync.Once
	power16R900SelectedPlatform power16R900Platform
)

func power16R900CurrentPlatform() *power16R900Platform {
	power16R900PlatformOnce.Do(power16R900ProbePlatform)
	return &power16R900SelectedPlatform
}

func power16R900ProbePlatform() {
	killSwitchName := ""
	for _, name := range []string{"RTLAMR_DISABLE_NEON", "RTLAMR_DISABLE_POWER16", "RTLAMR_DISABLE_R900_POWER16_SIMD"} {
		if os.Getenv(name) != "" {
			killSwitchName = name
			break
		}
	}
	platform := power16R900Platform{
		implementation: power16R900PortableImplementation,
		killSwitch:     killSwitchName != "",
		killSwitchName: killSwitchName,
	}
	value, err := os.ReadFile("/sys/devices/system/cpu/cpu0/regs/identification/midr_el1")
	if err == nil {
		platform.midr = strings.TrimSpace(string(value))
	}
	platform = power16R900SelectPlatform(platform, platform.midr, power16R900DetectASIMD(), power16R900SelfTest)
	power16R900SelectedPlatform = platform
}

func power16R900SelectPlatform(platform power16R900Platform, midr string, asimd bool, selfTest func() bool) power16R900Platform {
	platform.midr = midr
	platform.genuineA72 = midr == power16R900A72MIDR
	platform.asimd = asimd
	switch {
	case platform.killSwitch:
		platform.fallbackReason = "kill-switch"
	case !platform.genuineA72:
		platform.fallbackReason = "unsupported-cpu"
	case !platform.asimd:
		platform.fallbackReason = "asimd-unavailable"
	default:
		platform.selfTestPassed = selfTest()
		if !platform.selfTestPassed {
			platform.fallbackReason = "self-test-failed"
			break
		}
		platform.nativeAvailable = true
		platform.implementation = power16R900SIMDImplementation
		platform.run = func(signal []uint16) byte {
			return byte(quantizePower16L72A72(unsafe.Pointer(&signal[0])))
		}
	}
	return platform
}

func power16R900DetectASIMD() bool {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 2 && (fields[0] == "Features" || fields[0] == "flags") {
			for _, feature := range fields[2:] {
				if feature == "asimd" {
					return true
				}
			}
		}
	}
	return false
}

func power16R900SelfTest() bool {
	const length = power16R900SIMDChipLength * 4
	backing := make([]uint16, length+8)
	for residue := 0; residue < 8; residue++ {
		window := backing[residue : residue+length]
		state := uint64(0x523353454c465431 + residue)
		for index := range window {
			state = state*6364136223846793005 + 1442695040888963407
			window[index] = uint16(state % 65026)
		}
		want := quantizePower16WindowGo(window, power16R900SIMDChipLength)
		got := byte(quantizePower16L72A72(unsafe.Pointer(&window[0])))
		if got != want {
			return false
		}
	}
	return true
}

//go:noescape
func quantizePower16L72A72(signal unsafe.Pointer) uint32

func power16R900ResetPlatformForTest() {
	power16R900PlatformOnce = sync.Once{}
	power16R900SelectedPlatform = power16R900Platform{}
	power16R900NativeCalls.Store(0)
	power16R900PortableCalls.Store(0)
}
