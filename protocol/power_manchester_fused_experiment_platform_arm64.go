//go:build d3_power_neon && d4_fused_power && linux && arm64 && gc && !purego && !race

package protocol

const (
	fusedPowerManchesterChipLength = 72
	fusedPowerManchesterBlockSize  = 8192
	fusedPowerManchesterHistory    = fusedPowerManchesterChipLength * 2
	fusedPowerManchesterWindow     = fusedPowerManchesterHistory + fusedPowerManchesterBlockSize
)
