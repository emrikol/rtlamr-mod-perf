package protocol

import (
	"bytes"
	"fmt"
	"sort"
	"testing"
)

type power16DecoderTestParser struct {
	cfg        PacketConfig
	compatible bool
	overlap    int
	decoder    *Decoder
	history    Power16History
	parseCalls int
	id         uint32
	emit       bool
}

func newPower16DecoderTestParser(chipLength int, compatible bool, overlap int) *power16DecoderTestParser {
	return &power16DecoderTestParser{
		cfg: PacketConfig{
			Protocol:        "power16-test",
			CenterFreq:      912600155,
			DataRate:        32768,
			ChipLength:      chipLength,
			PreambleSymbols: 32,
			PacketSymbols:   736,
			Preamble:        fixedR900PreambleASCII,
		},
		compatible: compatible,
		overlap:    overlap,
	}
}

func (p *power16DecoderTestParser) Parse(_ []Data, messages []Message) []Message {
	p.parseCalls++
	if p.emit {
		messages = append(messages, decoderTestMessage{id: p.id})
	}
	return messages
}

func (p *power16DecoderTestParser) SetDecoder(decoder *Decoder) { p.decoder = decoder }

func (p *power16DecoderTestParser) Cfg() PacketConfig { return p.cfg }

func (p *power16DecoderTestParser) Power16Compatible() bool { return p.compatible }

func (p *power16DecoderTestParser) SignalHistoryOverlap() int { return p.overlap }

func (p *power16DecoderTestParser) SetPower16History(history Power16History) {
	p.history = history
}

type power16DecoderUnmarkedParser struct {
	cfg     PacketConfig
	decoder *Decoder
}

func (p *power16DecoderUnmarkedParser) Parse(pkts []Data, messages []Message) []Message {
	return messages
}

func (p *power16DecoderUnmarkedParser) SetDecoder(decoder *Decoder) {
	p.decoder = decoder
}

func (p *power16DecoderUnmarkedParser) Cfg() PacketConfig {
	return p.cfg
}

// Deliberately do not embed the marked parser: promoted optional methods would
// make this fixture compatible.
type power16DecoderNoConsumerParser struct {
	cfg     PacketConfig
	decoder *Decoder
	overlap int
}

func (p *power16DecoderNoConsumerParser) Parse(_ []Data, messages []Message) []Message {
	return messages
}

func (p *power16DecoderNoConsumerParser) SetDecoder(decoder *Decoder) { p.decoder = decoder }

func (p *power16DecoderNoConsumerParser) Cfg() PacketConfig { return p.cfg }

func (*power16DecoderNoConsumerParser) Power16Compatible() bool { return true }

func (p *power16DecoderNoConsumerParser) SignalHistoryOverlap() int {
	return p.overlap
}

func power16DecoderTestPlatform() power16Platform {
	return power16Platform{
		implementation:  "test-exact-power16",
		midr:            "0x00000000410fd083",
		nativeAvailable: true,
		genuineA72:      true,
		asimd:           true,
		selfTestPassed:  true,
		run:             power16ReferenceBlock,
	}
}

func newPower16AutomaticTestDecoder(platform power16Platform) Decoder {
	return newDecoder(power16PolicyAutomatic, func() power16Platform { return platform })
}

func TestPower16DefaultPolicyDoesNotProbeOrSelect(t *testing.T) {
	probeCalls := 0
	decoder := newDecoder(power16PolicyDisabled, func() power16Platform {
		probeCalls++
		return power16DecoderTestPlatform()
	})
	parser := newPower16DecoderTestParser(72, true, 72*4)
	decoder.RegisterProtocol(parser)
	decoder.Allocate()

	status := decoder.DispatchStatus()
	if probeCalls != 0 {
		t.Fatalf("disabled policy probe calls=%d, want 0", probeCalls)
	}
	if status.Power16Active || status.PolicyAutomatic || status.FallbackReason != "policy-disabled" {
		t.Fatalf("disabled status=%+v", status)
	}
	if decoder.power16 != nil || decoder.demod == nil || len(decoder.Signal) == 0 {
		t.Fatal("disabled policy did not retain the float representation")
	}
	if parser.history != nil {
		t.Fatal("disabled policy attached integer history")
	}
}

func TestNewDecoderEnablesAutomaticPower16Policy(t *testing.T) {
	decoder := NewDecoder()
	decoder.RegisterProtocol(newPower16DecoderTestParser(72, true, 72*4))
	decoder.Allocate()
	status := decoder.DispatchStatus()
	if !status.PolicyAutomatic {
		t.Fatalf("NewDecoder status=%+v", status)
	}
}

func TestPower16SelectionRequiresEveryAllocationGate(t *testing.T) {
	tests := []struct {
		name           string
		parser         Parser
		platform       power16Platform
		wantReason     string
		wantCompatible bool
		wantGeometry   bool
	}{
		{
			name:           "compatible",
			parser:         newPower16DecoderTestParser(72, true, 72*4),
			platform:       power16DecoderTestPlatform(),
			wantCompatible: true,
			wantGeometry:   true,
		},
		{
			name:         "marker-false",
			parser:       newPower16DecoderTestParser(72, false, 72*4),
			platform:     power16DecoderTestPlatform(),
			wantReason:   "parser-incompatible",
			wantGeometry: true,
		},
		{
			name:         "unmarked",
			parser:       &power16DecoderUnmarkedParser{cfg: newPower16DecoderTestParser(72, true, 0).cfg},
			platform:     power16DecoderTestPlatform(),
			wantReason:   "parser-incompatible",
			wantGeometry: true,
		},
		{
			name: "marker-only-compatible",
			parser: &power16DecoderNoConsumerParser{
				cfg: newPower16DecoderTestParser(72, true, 0).cfg,
			},
			platform:       power16DecoderTestPlatform(),
			wantCompatible: true,
			wantGeometry:   true,
		},
		{
			name: "history-without-consumer",
			parser: &power16DecoderNoConsumerParser{
				cfg:     newPower16DecoderTestParser(72, true, 0).cfg,
				overlap: 72 * 4,
			},
			platform:     power16DecoderTestPlatform(),
			wantReason:   "parser-incompatible",
			wantGeometry: true,
		},
		{
			name:           "geometry",
			parser:         newPower16DecoderTestParser(64, true, 64*4),
			platform:       power16DecoderTestPlatform(),
			wantReason:     "geometry-incompatible",
			wantCompatible: true,
		},
	}

	platformFailures := []struct {
		name   string
		mutate func(*power16Platform)
		reason string
	}{
		{name: "kill", mutate: func(p *power16Platform) { p.killSwitch = true }, reason: "kill-switch"},
		{name: "asimd", mutate: func(p *power16Platform) { p.asimd = false }, reason: "asimd-unavailable"},
		{name: "cpu", mutate: func(p *power16Platform) { p.genuineA72 = false }, reason: "unsupported-cpu"},
		{name: "self-test", mutate: func(p *power16Platform) { p.selfTestPassed = false }, reason: "self-test-failed"},
		{name: "availability", mutate: func(p *power16Platform) { p.nativeAvailable = false }, reason: "native-unavailable"},
		{name: "runner", mutate: func(p *power16Platform) { p.run = nil }, reason: "native-unavailable"},
	}
	for _, failure := range platformFailures {
		platform := power16DecoderTestPlatform()
		failure.mutate(&platform)
		tests = append(tests, struct {
			name           string
			parser         Parser
			platform       power16Platform
			wantReason     string
			wantCompatible bool
			wantGeometry   bool
		}{
			name:           failure.name,
			parser:         newPower16DecoderTestParser(72, true, 72*4),
			platform:       platform,
			wantReason:     failure.reason,
			wantCompatible: true,
			wantGeometry:   true,
		})
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decoder := newPower16AutomaticTestDecoder(test.platform)
			decoder.RegisterProtocol(test.parser)
			decoder.Allocate()
			status := decoder.DispatchStatus()
			if status.ParserCompatible != test.wantCompatible || status.GeometryCompatible != test.wantGeometry {
				t.Fatalf("status=%+v, want compatible=%t geometry=%t", status, test.wantCompatible, test.wantGeometry)
			}
			wantActive := test.wantReason == ""
			if status.Power16Active != wantActive || status.FallbackReason != test.wantReason {
				t.Fatalf("status=%+v, want active=%t reason=%q", status, wantActive, test.wantReason)
			}
			if wantActive {
				if decoder.power16 == nil || decoder.Signal != nil || decoder.signalBacking != nil || decoder.demod != nil {
					t.Fatal("active selection retained float ownership")
				}
			} else if decoder.power16 != nil || decoder.demod == nil || len(decoder.Signal) == 0 {
				t.Fatal("failed selection did not allocate float fallback")
			}
		})
	}
}

func TestPower16MixedParserSetFallsBackWithoutAttachingHistory(t *testing.T) {
	compatible := newPower16DecoderTestParser(72, true, 72*4)
	incompatible := newPower16DecoderTestParser(72, false, 0)
	decoder := newPower16AutomaticTestDecoder(power16DecoderTestPlatform())
	decoder.RegisterProtocol(compatible)
	decoder.RegisterProtocol(incompatible)
	decoder.Allocate()

	status := decoder.DispatchStatus()
	if status.Power16Active || status.ParserCompatible || status.FallbackReason != "parser-incompatible" {
		t.Fatalf("mixed-parser status=%+v", status)
	}
	if compatible.history != nil || incompatible.history != nil {
		t.Fatal("mixed-parser fallback attached integer history")
	}
}

func TestPower16ParserAndGeometryFailuresDoNotProbeNativeCode(t *testing.T) {
	tests := []struct {
		name   string
		parser Parser
	}{
		{name: "parser", parser: newPower16DecoderTestParser(72, false, 0)},
		{name: "geometry", parser: newPower16DecoderTestParser(64, true, 0)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			probeCalls := 0
			decoder := newDecoder(power16PolicyAutomatic, func() power16Platform {
				probeCalls++
				return power16DecoderTestPlatform()
			})
			decoder.RegisterProtocol(test.parser)
			decoder.Allocate()
			if probeCalls != 0 {
				t.Fatalf("probe calls=%d, want 0", probeCalls)
			}
		})
	}
}

func TestPower16ActivePathPreservesParserObjectOrder(t *testing.T) {
	first := newPower16DecoderTestParser(72, true, 0)
	first.id, first.emit = 1, true
	second := newPower16DecoderTestParser(72, true, 0)
	second.id, second.emit = 2, true
	decoder := newPower16AutomaticTestDecoder(power16DecoderTestPlatform())
	decoder.RegisterProtocol(first)
	decoder.RegisterProtocol(second)
	decoder.Allocate()

	messages := decoder.Decode(make([]byte, decoder.Cfg.BlockSize2))
	if len(messages) != 2 || messages[0].MeterID() != 1 || messages[1].MeterID() != 2 {
		t.Fatalf("message order=%v, want meter IDs [1 2]", messages)
	}
	if first.decoder != &decoder || second.decoder != &decoder {
		t.Fatal("active path replaced a registered parser's decoder")
	}
}

func TestPower16SegmentedHistoryDecisionsInputAndDispatch(t *testing.T) {
	parser := newPower16DecoderTestParser(72, true, 72*4)
	decoder := newPower16AutomaticTestDecoder(power16DecoderTestPlatform())
	decoder.RegisterProtocol(parser)
	decoder.Allocate()
	if parser.decoder != &decoder || parser.history != &decoder {
		t.Fatal("Power16 parser did not retain the exact registered decoder")
	}
	state := decoder.power16
	if state.historyOverlap != 72*4 {
		t.Fatalf("history overlap=%d, want %d", state.historyOverlap, 72*4)
	}

	logical := make([]uint16, decoder.Cfg.BufferLength)
	iterations := state.blockCount*2 + 3
	for iteration := 0; iteration < iterations; iteration++ {
		input := power16DecoderTestIQ(decoder.Cfg.BlockSize, uint64(0x160000+iteration))
		inputCopy := append([]byte(nil), input...)
		blockPower := make([]uint16, decoder.Cfg.BlockSize)
		power16IQReference(blockPower, input)
		filterWindow := make([]uint16, decoder.Cfg.SymbolLength+decoder.Cfg.BlockSize)
		copy(filterWindow, logical[len(logical)-decoder.Cfg.SymbolLength:])
		copy(filterWindow[decoder.Cfg.SymbolLength:], blockPower)
		wantDecisions := make([]byte, decoder.Cfg.BlockSize)
		power16ManchesterReference(wantDecisions, filterWindow, decoder.Cfg.ChipLength)

		if messages := decoder.Decode(input); len(messages) != 0 {
			t.Fatalf("iteration=%d messages=%d, want 0", iteration, len(messages))
		}
		if !bytes.Equal(input, inputCopy) {
			t.Fatalf("iteration=%d input mutated", iteration)
		}
		if !bytes.Equal(decoder.filterOutput, wantDecisions) {
			for idx := range wantDecisions {
				if decoder.filterOutput[idx] != wantDecisions[idx] {
					t.Fatalf("iteration=%d decision[%d]=%d, want %d", iteration, idx, decoder.filterOutput[idx], wantDecisions[idx])
				}
			}
		}

		copy(logical, logical[decoder.Cfg.BlockSize:])
		copy(logical[len(logical)-decoder.Cfg.BlockSize:], blockPower)
		windowStarts := map[int]bool{
			0: true,
			decoder.Cfg.BufferLength - state.historyOverlap: true,
		}
		for boundary := decoder.Cfg.BlockSize - state.gap; boundary < decoder.Cfg.BufferLength; boundary += decoder.Cfg.BlockSize {
			start := boundary - state.historyOverlap/2
			if start >= 0 && start+state.historyOverlap <= decoder.Cfg.BufferLength {
				windowStarts[start] = true
			}
		}
		ordered := make([]int, 0, len(windowStarts))
		for start := range windowStarts {
			ordered = append(ordered, start)
		}
		sort.Ints(ordered)
		for _, start := range ordered {
			got := decoder.Power16Window(start, state.historyOverlap)
			want := logical[start : start+state.historyOverlap]
			for idx := range want {
				if got[idx] != want[idx] {
					t.Fatalf("iteration=%d window=%d offset=%d got=%d want=%d", iteration, start, idx, got[idx], want[idx])
				}
			}
		}
	}
	status := decoder.DispatchStatus()
	if status.NativeBlocks != uint64(iterations) || !status.Power16Active || status.Implementation != "test-exact-power16" {
		t.Fatalf("dispatch status=%+v, want active blocks=%d", status, iterations)
	}
	if parser.parseCalls != iterations {
		t.Fatalf("parser calls=%d, want %d", parser.parseCalls, iterations)
	}
}

func TestPower16DecodeIgnoresTrailingInputAndRejectsShortInput(t *testing.T) {
	decoder := newPower16AutomaticTestDecoder(power16DecoderTestPlatform())
	decoder.RegisterProtocol(newPower16DecoderTestParser(72, true, 0))
	decoder.Allocate()
	input := power16DecoderTestIQ(decoder.Cfg.BlockSize, 0x1600)
	input = append(input, bytes.Repeat([]byte{0xa5}, 31)...)
	decoder.Decode(input)

	defer func() {
		if recover() == nil {
			t.Fatal("short Power16 input did not panic")
		}
	}()
	decoder.Decode(input[:decoder.Cfg.BlockSize2-1])
}

func TestPower16ActiveRunnerRejectionIsNotSilent(t *testing.T) {
	platform := power16DecoderTestPlatform()
	platform.run = func([]byte, []uint16, []byte) bool { return false }
	decoder := newPower16AutomaticTestDecoder(platform)
	decoder.RegisterProtocol(newPower16DecoderTestParser(72, true, 0))
	decoder.Allocate()
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("active runner rejection did not panic")
		}
	}()
	decoder.Decode(make([]byte, decoder.Cfg.BlockSize2))
}

func TestPower16ReallocateDetachesHistoryOnFloatFallback(t *testing.T) {
	parser := newPower16DecoderTestParser(72, true, 72*4)
	decoder := newPower16AutomaticTestDecoder(power16DecoderTestPlatform())
	decoder.RegisterProtocol(parser)
	decoder.Allocate()
	if parser.history == nil {
		t.Fatal("initial Power16 history was not attached")
	}
	decoder.power16Policy = power16PolicyDisabled
	decoder.Allocate()
	if parser.history != nil || decoder.power16 != nil || decoder.demod == nil {
		t.Fatal("float reallocation did not detach Power16 ownership")
	}
}

func power16DecoderTestIQ(count int, seed uint64) []byte {
	input := make([]byte, count*2)
	state := seed
	for idx := range input {
		state = state*6364136223846793005 + 1442695040888963407
		input[idx] = byte(state >> 56)
	}
	return input
}

func TestPower16HistoryWindowRejectsInvalidRanges(t *testing.T) {
	decoder := newPower16AutomaticTestDecoder(power16DecoderTestPlatform())
	decoder.RegisterProtocol(newPower16DecoderTestParser(72, true, 72*4))
	decoder.Allocate()
	tests := []struct{ idx, length int }{
		{-1, 1},
		{0, -1},
		{0, decoder.power16.historyOverlap + 1},
		{decoder.Cfg.BufferLength, 0},
		{decoder.Cfg.BufferLength, 1},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("%d-%d", test.idx, test.length), func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("invalid history range did not panic")
				}
			}()
			decoder.Power16Window(test.idx, test.length)
		})
	}
}

var _ Parser = (*power16DecoderTestParser)(nil)
var _ Power16Compatible = (*power16DecoderTestParser)(nil)
var _ Power16HistoryConsumer = (*power16DecoderTestParser)(nil)
var _ Parser = (*power16DecoderNoConsumerParser)(nil)
