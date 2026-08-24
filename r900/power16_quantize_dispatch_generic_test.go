//go:build !linux || !arm64 || !gc || purego || race

package r900

import "testing"

func TestR900Power16QuantizerGenericStatus(t *testing.T) {
	status := Power16QuantizerDispatchStatus()
	if status.Implementation != power16R900PortableImplementation || status.FallbackReason != "unsupported-platform" ||
		status.MIDR != "" || status.ASIMD || status.GenuineA72 || status.SelfTestPassed || status.KillSwitch ||
		status.KillSwitchName != "" || status.NativeAvailable {
		t.Fatalf("generic status: %+v", status)
	}
}
