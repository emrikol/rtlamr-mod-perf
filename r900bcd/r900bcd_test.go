package r900bcd

import (
	"testing"

	"github.com/bemasher/rtlamr/protocol"
	"github.com/bemasher/rtlamr/r900"
)

type parserStub struct{}

func (parserStub) Parse(_ []protocol.Data, messages []protocol.Message) []protocol.Message {
	return append(messages, r900.R900{Consumption: 0x123})
}

func (parserStub) SetDecoder(*protocol.Decoder) {}

func (parserStub) Cfg() protocol.PacketConfig { return protocol.PacketConfig{} }

type power16HistoryStub struct{}

func (power16HistoryStub) Power16Window(_, length int) []uint16 {
	return make([]uint16, length)
}

type power16ParserStub struct {
	parserStub
	history protocol.Power16History
}

func (*power16ParserStub) Power16Compatible() bool { return true }

func (p *power16ParserStub) SetPower16History(history protocol.Power16History) {
	p.history = history
}

func TestParseConvertsOnlyWrappedParserMessages(t *testing.T) {
	parser := Parser{Parser: parserStub{}}
	existing := r900.R900{Consumption: 7}
	messages := parser.Parse(nil, []protocol.Message{existing})

	if len(messages) != 2 {
		t.Fatalf("message count = %d, want 2", len(messages))
	}
	if _, ok := messages[0].(r900.R900); !ok {
		t.Fatalf("existing message type = %T, want r900.R900", messages[0])
	}
	converted, ok := messages[1].(R900BCD)
	if !ok {
		t.Fatalf("wrapped message type = %T, want r900bcd.R900BCD", messages[1])
	}
	if converted.Consumption != 123 {
		t.Fatalf("converted consumption = %d, want 123", converted.Consumption)
	}
}

func TestPower16CapabilitiesForwardToWrappedParser(t *testing.T) {
	wrapped := new(power16ParserStub)
	parser := Parser{Parser: wrapped}
	if !parser.Power16Compatible() {
		t.Fatal("R900BCD did not forward Power16 compatibility")
	}

	history := power16HistoryStub{}
	parser.SetPower16History(history)
	if wrapped.history == nil {
		t.Fatal("R900BCD did not forward Power16 history")
	}
	parser.SetPower16History(nil)
	if wrapped.history != nil {
		t.Fatal("R900BCD did not forward Power16 history detachment")
	}
}

func TestPower16CompatibilityRejectsUnmarkedWrappedParser(t *testing.T) {
	parser := Parser{Parser: parserStub{}}
	if parser.Power16Compatible() {
		t.Fatal("R900BCD reported compatibility for an unmarked wrapped parser")
	}
	parser.SetPower16History(nil)
}

var _ protocol.Power16Compatible = (*power16ParserStub)(nil)
var _ protocol.Power16HistoryConsumer = (*power16ParserStub)(nil)
var _ protocol.Power16History = power16HistoryStub{}
