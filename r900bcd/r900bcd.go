// RTLAMR - An rtl-sdr receiver for smart meters operating in the 900MHz ISM band.
// Copyright (C) 2014 Douglas Hall
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package r900bcd

import (
	"strconv"

	"github.com/bemasher/rtlamr/protocol"
	"github.com/bemasher/rtlamr/r900"
)

func init() {
	protocol.RegisterParser("r900bcd", NewParser)
}

type Parser struct {
	protocol.Parser
}

func NewParser(ChipLength int) protocol.Parser {
	return Parser{r900.NewParser(ChipLength)}
}

func (p Parser) SignalHistoryOverlap() int {
	return p.Parser.(interface{ SignalHistoryOverlap() int }).SignalHistoryOverlap()
}

func (p Parser) Power16Compatible() bool {
	compatible, ok := p.Parser.(protocol.Power16Compatible)
	return ok && compatible.Power16Compatible()
}

func (p Parser) SetPower16History(history protocol.Power16History) {
	consumer, ok := p.Parser.(protocol.Power16HistoryConsumer)
	if !ok {
		if history == nil {
			return
		}
		panic("r900bcd: wrapped parser does not consume Power16 history")
	}
	consumer.SetPower16History(history)
}

type R900BCD struct {
	r900.R900
}

func (r R900BCD) MsgType() string {
	return "R900BCD"
}

// Parse messages using r900 parser and convert consumption from BCD to int.
func (p Parser) Parse(pkts []protocol.Data, messages []protocol.Message) []protocol.Message {
	start := len(messages)
	messages = p.Parser.Parse(pkts, messages)
	for idx := start; idx < len(messages); idx++ {
		r900bcd := R900BCD{messages[idx].(r900.R900)}
		hex := strconv.FormatUint(uint64(r900bcd.Consumption), 16)
		consumption, _ := strconv.ParseUint(hex, 10, 32)
		r900bcd.Consumption = uint32(consumption)
		messages[idx] = r900bcd
	}

	return messages
}

var _ protocol.Power16Compatible = Parser{}
var _ protocol.Power16HistoryConsumer = Parser{}
