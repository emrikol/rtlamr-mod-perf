//go:build linux && arm64 && gc && !purego && !race

package protocol

import (
	"testing"
	"unsafe"
)

func TestPower16PlatformKillSwitches(t *testing.T) {
	tests := []struct {
		name string
		env  string
	}{
		{name: "broad", env: "RTLAMR_DISABLE_NEON"},
		{name: "focused", env: "RTLAMR_DISABLE_POWER16"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("RTLAMR_DISABLE_NEON", "")
			t.Setenv("RTLAMR_DISABLE_POWER16", "")
			t.Setenv(test.env, "1")
			platform := probePower16Platform()
			if platform.nativeAvailable || !platform.killSwitch || platform.selfTestPassed || platform.run != nil {
				t.Fatalf("kill-switch platform=%+v", platform)
			}
			if platform.fallbackReason != "kill-switch" {
				t.Fatalf("fallback reason=%q, want kill-switch", platform.fallbackReason)
			}
		})
	}
}

func TestPower16PlatformAvailabilityImpliesEveryGate(t *testing.T) {
	t.Setenv("RTLAMR_DISABLE_NEON", "")
	t.Setenv("RTLAMR_DISABLE_POWER16", "")
	platform := probePower16Platform()
	if platform.nativeAvailable {
		if !platform.genuineA72 || !platform.asimd || !platform.selfTestPassed || platform.killSwitch ||
			platform.run == nil || platform.runPacked == nil {
			t.Fatalf("available platform omitted a gate: %+v", platform)
		}
		if platform.midr != cortexA72R0P3MIDR {
			t.Fatalf("available MIDR=%q, want %q", platform.midr, cortexA72R0P3MIDR)
		}
		if platform.implementation != power16FusedPackedImplementation {
			t.Fatalf("available implementation=%q, want %q", platform.implementation, power16FusedPackedImplementation)
		}
	}
	if !platform.genuineA72 && platform.selfTestPassed {
		t.Fatal("unsupported CPU ran the production startup self-test")
	}
}

func TestPower16FusedSelfTestOnASIMD(t *testing.T) {
	if !power16DetectASIMD() {
		t.Skip("ASIMD unavailable")
	}
	if !power16SelectedSelfTest() {
		t.Fatal("selected Power16 startup self-test failed")
	}
}

func TestPower16FusedWrapperRejectsGeometryAndOverlap(t *testing.T) {
	decisions := make([]byte, power16BlockSize)
	window := make([]uint16, power16Window)
	input := make([]byte, power16BlockSize*2)
	if power16FusedA72(decisions[:len(decisions)-1], window, input) {
		t.Fatal("short decisions reached fused leaf")
	}
	if power16FusedA72(decisions, window[:len(window)-1], input) {
		t.Fatal("short window reached fused leaf")
	}
	if power16FusedA72(decisions, window, input[:len(input)-1]) {
		t.Fatal("short input reached fused leaf")
	}

	windowBytes := (*[power16Window * 2]byte)(unsafe.Pointer(&window[0]))[:]
	if power16FusedA72(windowBytes[:power16BlockSize], window, input) {
		t.Fatal("decision/window overlap reached fused leaf")
	}
	if power16FusedA72(decisions, window, windowBytes[:power16BlockSize*2]) {
		t.Fatal("input/window overlap reached fused leaf")
	}
}
