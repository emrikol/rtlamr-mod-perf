//go:build d3_power_neon && d4_power16_fusion && d4_fused_power && d4_power16_complete && (!linux || !arm64 || !gc || purego || race)

package protocol

func probePower16AcceptedA72Validation() power16Platform {
	return probePower16Platform()
}
