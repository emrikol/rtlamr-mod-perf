//go:build d3_power_neon && d4_power16_fusion && d4_fused_power && d4_power16_complete

package protocol

// Power16ValidationProbe selects the platform probe used by the tagged replay
// and benchmark tools. The reference runner exists only to keep deterministic
// semantic tests executable away from an A72; authoritative tools must use
// Power16ValidationRealProbe.
type Power16ValidationProbe uint8

const (
	Power16ValidationRealProbe Power16ValidationProbe = iota
	Power16ValidationReferenceRunner
	Power16ValidationAcceptedA72
)

// NewPower16FloatDecoderValidation constructs the explicit float control used
// by complete-decoder replay and benchmarks after the public constructor has
// enabled automatic Power16 selection. It is available only under the
// complete validation build tags and cannot alter production policy.
func NewPower16FloatDecoderValidation() Decoder {
	return newDecoder(power16PolicyDisabled, probePower16Platform)
}

// NewPower16AutomaticDecoderValidation returns the ordinary Decoder with its
// private allocation-time Power16 policy enabled. It does not change the
// default policy used by NewDecoder.
func NewPower16AutomaticDecoderValidation(probe Power16ValidationProbe) Decoder {
	switch probe {
	case Power16ValidationRealProbe:
		return newDecoder(power16PolicyAutomatic, probePower16Platform)
	case Power16ValidationReferenceRunner:
		return newDecoder(power16PolicyAutomatic, func() power16Platform {
			return power16Platform{
				implementation: "power16-reference-validation",
				fallbackReason: "validation-only",
				asimd:          true,
				selfTestPassed: true,
				validationOnly: true,
				run:            power16ReferenceBlock,
			}
		})
	case Power16ValidationAcceptedA72:
		return newDecoder(power16PolicyAutomatic, probePower16AcceptedA72Validation)
	default:
		panic("protocol: invalid Power16 validation probe")
	}
}
