//go:build d3_power_neon && d4_power16_fusion && d4_fused_power && d4_power16_complete && linux && arm64 && gc && !purego && !race

package protocol

// probePower16AcceptedA72Validation exposes the deployed D4 leaf only to the
// complete-decoder validation harness. Production selection remains S6. The
// ordinary A72/ASIMD/kill gates must first accept S6, then the retained D4 leaf
// passes its own startup equivalence test before it can be timed as a control.
func probePower16AcceptedA72Validation() power16Platform {
	platform := probePower16Platform()
	if !platform.nativeAvailable || !platform.genuineA72 || !platform.asimd ||
		platform.killSwitch || !platform.selfTestPassed {
		return platform
	}
	platform.implementation = power16FusedImplementation
	platform.run = power16FusedA72
	platform.runPacked = nil
	platform.selfTestPassed = power16FusedSelfTest()
	if !platform.selfTestPassed {
		platform.nativeAvailable = false
		platform.fallbackReason = "accepted-d4-self-test-failed"
	}
	return platform
}
