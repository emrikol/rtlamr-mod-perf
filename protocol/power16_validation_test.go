//go:build d3_power_neon && d4_power16_fusion && d4_fused_power && d4_power16_complete

package protocol

import "testing"

func TestPower16FloatValidationControlRemainsDisabled(t *testing.T) {
	decoder := NewPower16FloatDecoderValidation()
	decoder.RegisterProtocol(newPower16DecoderTestParser(72, true, 72*4))
	decoder.Allocate()
	status := decoder.DispatchStatus()
	if status.PolicyAutomatic || status.Power16Active || status.FallbackReason != "policy-disabled" {
		t.Fatalf("float validation control status=%+v", status)
	}
}

func TestPower16ValidationReferenceRunnerIsNonAuthoritative(t *testing.T) {
	decoder := NewPower16AutomaticDecoderValidation(Power16ValidationReferenceRunner)
	decoder.RegisterProtocol(newPower16DecoderTestParser(72, true, 72*4))
	decoder.Allocate()
	status := decoder.DispatchStatus()
	if !status.PolicyAutomatic || !status.Power16Active || !status.ValidationOnly ||
		status.NativeAvailable || status.GenuineA72 || status.NativeBlocks != 0 || status.ValidationBlocks != 0 {
		t.Fatalf("reference allocation status=%+v", status)
	}
	if status.Implementation != "power16-reference-validation" {
		t.Fatalf("reference implementation=%q", status.Implementation)
	}
	decoder.Decode(make([]byte, decoder.Cfg.BlockSize2))
	status = decoder.DispatchStatus()
	if status.NativeBlocks != 0 || status.ValidationBlocks != 1 {
		t.Fatalf("reference block status=%+v", status)
	}
}

func TestPower16ValidationRealProbeNeverBypassesProductionGates(t *testing.T) {
	decoder := NewPower16AutomaticDecoderValidation(Power16ValidationRealProbe)
	decoder.RegisterProtocol(newPower16DecoderTestParser(72, true, 72*4))
	decoder.Allocate()
	status := decoder.DispatchStatus()
	if status.ValidationOnly || !status.PolicyAutomatic {
		t.Fatalf("real-probe status=%+v", status)
	}
	if status.Power16Active && (!status.NativeAvailable || !status.GenuineA72 || !status.ASIMD || !status.SelfTestPassed) {
		t.Fatalf("real probe bypassed a production gate: %+v", status)
	}
}

func TestPower16ValidationRejectsUnknownProbe(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("unknown validation probe did not panic")
		}
	}()
	_ = NewPower16AutomaticDecoderValidation(Power16ValidationProbe(255))
}
