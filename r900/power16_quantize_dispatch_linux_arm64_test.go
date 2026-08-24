//go:build linux && arm64 && gc && !purego && !race

package r900

import (
	"os"
	"testing"
)

func TestR900Power16QuantizerAutomaticA72Gate(t *testing.T) {
	if os.Getenv("RTLAMR_R900_REQUIRE_A72") == "" {
		t.Skip("set RTLAMR_R900_REQUIRE_A72=1 for the authoritative Cortex-A72 dispatch gate")
	}
	for _, name := range []string{"RTLAMR_DISABLE_NEON", "RTLAMR_DISABLE_POWER16", "RTLAMR_DISABLE_R900_POWER16_SIMD"} {
		t.Setenv(name, "")
	}
	power16R900ResetPlatformForTest()
	t.Cleanup(power16R900ResetPlatformForTest)

	_ = NewParser(power16R900SIMDChipLength)
	status := Power16QuantizerDispatchStatus()
	if status.Implementation != power16R900SIMDImplementation || status.MIDR != power16R900A72MIDR ||
		!status.ASIMD || !status.GenuineA72 || !status.SelfTestPassed || status.KillSwitch || !status.NativeAvailable {
		t.Fatalf("unexpected A72 dispatch status: %+v", status)
	}

	window := r900Power16FillChipConstants([4]uint16{65025, 0, 0, 65025})
	if got, want := quantizePower16Window(window, power16R900SIMDChipLength), quantizePower16WindowGo(window, power16R900SIMDChipLength); got != want {
		t.Fatalf("native dispatch got=%d want=%d", got, want)
	}
	status = Power16QuantizerDispatchStatus()
	if status.NativeCalls != 1 || status.PortableCalls != 0 {
		t.Fatalf("native dispatch counters native=%d portable=%d", status.NativeCalls, status.PortableCalls)
	}
}

func TestR900Power16QuantizerSelectionIsEagerAtParserConstruction(t *testing.T) {
	for _, name := range []string{"RTLAMR_DISABLE_NEON", "RTLAMR_DISABLE_POWER16", "RTLAMR_DISABLE_R900_POWER16_SIMD"} {
		t.Setenv(name, "")
	}
	power16R900ResetPlatformForTest()
	t.Cleanup(power16R900ResetPlatformForTest)

	_ = NewParser(power16R900SIMDChipLength)
	for _, name := range []string{"RTLAMR_DISABLE_NEON", "RTLAMR_DISABLE_POWER16", "RTLAMR_DISABLE_R900_POWER16_SIMD"} {
		t.Setenv(name, "1")
	}
	status := Power16QuantizerDispatchStatus()
	if status.KillSwitch || status.FallbackReason == "kill-switch" {
		t.Fatalf("selector was sampled lazily after parser construction: %+v", status)
	}
	if status.NativeCalls != 0 || status.PortableCalls != 0 {
		t.Fatalf("parser construction executed quantizer calls: %+v", status)
	}
}

func TestR900Power16QuantizerGateTable(t *testing.T) {
	testCases := []struct {
		name           string
		midr           string
		asimd          bool
		kill           string
		selfTest       bool
		implementation string
		fallback       string
		native         bool
	}{
		{name: "supported", midr: power16R900A72MIDR, asimd: true, selfTest: true, implementation: power16R900SIMDImplementation, native: true},
		{name: "unsupported-cpu", midr: "0x00000000410fd0b0", asimd: true, selfTest: true, implementation: power16R900PortableImplementation, fallback: "unsupported-cpu"},
		{name: "missing-asimd", midr: power16R900A72MIDR, selfTest: true, implementation: power16R900PortableImplementation, fallback: "asimd-unavailable"},
		{name: "self-test-failed", midr: power16R900A72MIDR, asimd: true, implementation: power16R900PortableImplementation, fallback: "self-test-failed"},
		{name: "kill-switch", midr: power16R900A72MIDR, asimd: true, kill: "RTLAMR_DISABLE_R900_POWER16_SIMD", selfTest: true, implementation: power16R900PortableImplementation, fallback: "kill-switch"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			platform := power16R900Platform{
				implementation: power16R900PortableImplementation,
				killSwitch:     testCase.kill != "",
				killSwitchName: testCase.kill,
			}
			selfTestCalls := 0
			platform = power16R900SelectPlatform(platform, testCase.midr, testCase.asimd, func() bool {
				selfTestCalls++
				return testCase.selfTest
			})
			if platform.implementation != testCase.implementation || platform.fallbackReason != testCase.fallback ||
				platform.nativeAvailable != testCase.native || platform.killSwitchName != testCase.kill {
				t.Fatalf("platform=%+v", platform)
			}
			wantSelfTestCalls := 0
			if testCase.midr == power16R900A72MIDR && testCase.asimd && testCase.kill == "" {
				wantSelfTestCalls = 1
			}
			if selfTestCalls != wantSelfTestCalls {
				t.Fatalf("self-test calls=%d want=%d", selfTestCalls, wantSelfTestCalls)
			}
		})
	}
}

func TestR900Power16QuantizerKillSwitches(t *testing.T) {
	for _, name := range []string{"RTLAMR_DISABLE_NEON", "RTLAMR_DISABLE_POWER16", "RTLAMR_DISABLE_R900_POWER16_SIMD"} {
		t.Run(name, func(t *testing.T) {
			for _, clear := range []string{"RTLAMR_DISABLE_NEON", "RTLAMR_DISABLE_POWER16", "RTLAMR_DISABLE_R900_POWER16_SIMD"} {
				t.Setenv(clear, "")
			}
			t.Setenv(name, "1")
			power16R900ResetPlatformForTest()
			t.Cleanup(power16R900ResetPlatformForTest)

			status := Power16QuantizerDispatchStatus()
			if status.NativeAvailable || !status.KillSwitch || status.KillSwitchName != name || status.Implementation != power16R900PortableImplementation || status.FallbackReason != "kill-switch" {
				t.Fatalf("kill-switch status: %+v", status)
			}
			window := r900Power16FillChipConstants([4]uint16{65025, 0, 0, 65025})
			if got, want := quantizePower16Window(window, power16R900SIMDChipLength), quantizePower16WindowGo(window, power16R900SIMDChipLength); got != want {
				t.Fatalf("kill-switch fallback got=%d want=%d", got, want)
			}
			status = Power16QuantizerDispatchStatus()
			if status.NativeCalls != 0 || status.PortableCalls != 1 {
				t.Fatalf("kill-switch counters native=%d portable=%d", status.NativeCalls, status.PortableCalls)
			}
		})
	}
}
