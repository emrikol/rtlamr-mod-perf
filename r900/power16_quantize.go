package r900

import "sync/atomic"

const (
	power16R900SIMDChipLength         = 72
	power16R900SIMDImplementation     = "power16-r900-l72-a72-v1"
	power16R900PortableImplementation = "go-power16-r900"
)

type power16R900Platform struct {
	implementation  string
	fallbackReason  string
	midr            string
	asimd           bool
	genuineA72      bool
	selfTestPassed  bool
	killSwitch      bool
	killSwitchName  string
	nativeAvailable bool
	run             func([]uint16) byte
}

// Power16QuantizerStatus reports the immutable process-wide R900 Power16
// backend selection and cumulative caller-visible dispatch counts.
type Power16QuantizerStatus struct {
	Implementation  string `json:"implementation"`
	FallbackReason  string `json:"fallback_reason,omitempty"`
	MIDR            string `json:"midr,omitempty"`
	ASIMD           bool   `json:"asimd"`
	GenuineA72      bool   `json:"genuine_a72"`
	SelfTestPassed  bool   `json:"self_test_passed"`
	KillSwitch      bool   `json:"kill_switch"`
	KillSwitchName  string `json:"kill_switch_name,omitempty"`
	NativeAvailable bool   `json:"native_available"`
	NativeCalls     uint64 `json:"native_calls"`
	PortableCalls   uint64 `json:"portable_calls"`
}

var (
	power16R900NativeCalls   atomic.Uint64
	power16R900PortableCalls atomic.Uint64
)

func Power16QuantizerDispatchStatus() Power16QuantizerStatus {
	platform := power16R900CurrentPlatform()
	return Power16QuantizerStatus{
		Implementation:  platform.implementation,
		FallbackReason:  platform.fallbackReason,
		MIDR:            platform.midr,
		ASIMD:           platform.asimd,
		GenuineA72:      platform.genuineA72,
		SelfTestPassed:  platform.selfTestPassed,
		KillSwitch:      platform.killSwitch,
		KillSwitchName:  platform.killSwitchName,
		NativeAvailable: platform.nativeAvailable,
		NativeCalls:     power16R900NativeCalls.Load(),
		PortableCalls:   power16R900PortableCalls.Load(),
	}
}

func power16R900ResetCountersForTest() {
	power16R900NativeCalls.Store(0)
	power16R900PortableCalls.Store(0)
}

func quantizePower16WindowGo(signal []uint16, chipLength int) byte {
	var chip [4]int32
	for segment := range chip {
		start := segment * chipLength
		for offset := 0; offset < chipLength; offset++ {
			chip[segment] += int32(signal[start+offset])
		}
	}

	v0 := chip[0] + chip[1] - chip[2] - chip[3]
	v1 := chip[0] - chip[1] + chip[2] - chip[3]
	v2 := chip[0] - chip[1] - chip[2] + chip[3]
	return quantizePower16Symbol(v0, v1, v2)
}

func (p *Parser) quantizePower16At(idx int) byte {
	chipLength := p.cfg.ChipLength
	signal := p.powerHistory.Power16Window(idx, chipLength*4)
	return quantizePower16Window(signal, chipLength)
}

func quantizePower16Window(signal []uint16, chipLength int) byte {
	platform := power16R900CurrentPlatform()
	if chipLength == power16R900SIMDChipLength && len(signal) >= power16R900SIMDChipLength*4 && platform.nativeAvailable {
		power16R900NativeCalls.Add(1)
		return platform.run(signal)
	}
	power16R900PortableCalls.Add(1)
	return quantizePower16WindowGo(signal, chipLength)
}
