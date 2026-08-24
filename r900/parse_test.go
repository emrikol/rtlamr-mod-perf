package r900

import (
	"fmt"
	"math/rand"
	"strconv"
	"testing"

	"github.com/bemasher/rtlamr/protocol"
)

func legacyR900FromSymbols(symbols *[21]byte) (message R900) {
	var bits string
	for _, symbol := range symbols {
		bits += fmt.Sprintf("%05b", symbol)
	}
	id, _ := strconv.ParseUint(bits[:32], 2, 32)
	unkn1, _ := strconv.ParseUint(bits[32:40], 2, 8)
	nouse, _ := strconv.ParseUint(bits[40:46], 2, 6)
	backflow, _ := strconv.ParseUint(bits[46:48], 2, 2)
	consumption, _ := strconv.ParseUint(bits[48:72], 2, 24)
	unkn3, _ := strconv.ParseUint(bits[72:74], 2, 2)
	leak, _ := strconv.ParseUint(bits[74:78], 2, 4)
	leaknow, _ := strconv.ParseUint(bits[78:80], 2, 2)

	message.ID = uint32(id)
	message.Unkn1 = uint8(unkn1)
	message.NoUse = uint8(nouse)
	message.BackFlow = uint8(backflow)
	message.Consumption = uint32(consumption)
	message.Unkn3 = uint8(unkn3)
	message.Leak = uint8(leak)
	message.LeakNow = uint8(leaknow)
	copy(message.checksum[:], symbols[16:])
	return message
}

func TestR900FromSymbolsMatchesLegacyStringDecoder(t *testing.T) {
	var fixtures [][21]byte
	fixtures = append(fixtures, [21]byte{})
	var maximum [21]byte
	for idx := range maximum {
		maximum[idx] = 31
	}
	fixtures = append(fixtures, maximum)
	for position := range maximum {
		var single [21]byte
		single[position] = 31
		fixtures = append(fixtures, single)
	}

	rng := rand.New(rand.NewSource(0x900dec0de))
	for iteration := 0; iteration < 4096; iteration++ {
		var symbols [21]byte
		for idx := range symbols {
			symbols[idx] = byte(rng.Intn(32))
		}
		fixtures = append(fixtures, symbols)
	}

	for index := range fixtures {
		got := r900FromSymbols(&fixtures[index])
		want := legacyR900FromSymbols(&fixtures[index])
		if got != want {
			t.Fatalf("fixture %d: decoded message %+v, want %+v", index, got, want)
		}
	}
}

func r900TestPayloadSymbols(message R900) [21]byte {
	var symbols [21]byte
	high := uint64(message.ID)<<28 |
		uint64(message.Unkn1)<<20 |
		uint64(message.NoUse&0x3f)<<14 |
		uint64(message.BackFlow&0x03)<<12 |
		uint64(message.Consumption>>12)&0x0fff
	low := uint32(message.Consumption&0x0fff)<<8 |
		uint32(message.Unkn3&0x03)<<6 |
		uint32(message.Leak&0x0f)<<2 |
		uint32(message.LeakNow&0x03)
	for idx := 11; idx >= 0; idx-- {
		symbols[idx] = byte(high & 0x1f)
		high >>= 5
	}
	for idx := 15; idx >= 12; idx-- {
		symbols[idx] = byte(low & 0x1f)
		low >>= 5
	}
	return symbols
}

func r900TestAddParity(parser *Parser, symbols *[21]byte) {
	var matrix [5][6]byte
	var encoded [31]byte
	copy(encoded[:16], symbols[:16])
	target := parser.field.Syndrome(encoded[:], 5, 29)
	for row := 0; row < 5; row++ {
		x := parser.field.Exp(29 + row)
		for column := 0; column < 5; column++ {
			coefficient := byte(1)
			for exponent := 0; exponent < 4-column; exponent++ {
				coefficient = parser.field.Mul(coefficient, x)
			}
			matrix[row][column] = coefficient
		}
		matrix[row][5] = target[row]
	}

	for column := 0; column < 5; column++ {
		pivot := column
		for pivot < 5 && matrix[pivot][column] == 0 {
			pivot++
		}
		if pivot == 5 {
			panic("singular R900 parity matrix")
		}
		matrix[column], matrix[pivot] = matrix[pivot], matrix[column]
		inverse := parser.field.Inv(matrix[column][column])
		for idx := column; idx < 6; idx++ {
			matrix[column][idx] = parser.field.Mul(matrix[column][idx], inverse)
		}
		for row := 0; row < 5; row++ {
			if row == column {
				continue
			}
			factor := matrix[row][column]
			for idx := column; idx < 6; idx++ {
				matrix[row][idx] ^= parser.field.Mul(factor, matrix[column][idx])
			}
		}
	}
	for idx := 0; idx < 5; idx++ {
		symbols[16+idx] = matrix[idx][5]
	}
}

func r900TestRenderDigit(history []uint16, offset, chipLength int, digit byte) {
	patterns := [3][4]uint16{
		{100, 100, 0, 0},
		{100, 0, 100, 0},
		{100, 0, 0, 100},
	}
	pattern := patterns[digit%3]
	if digit < 3 {
		for idx := range pattern {
			pattern[idx] = 100 - pattern[idx]
		}
	}
	for segment, value := range pattern {
		for idx := 0; idx < chipLength; idx++ {
			history[offset+segment*chipLength+idx] = value
		}
	}
}

func r900TestRenderSymbol(history []uint16, offset, chipLength int, symbol byte) {
	r900TestRenderDigit(history, offset, chipLength, symbol/6)
	r900TestRenderDigit(history, offset+chipLength*4, chipLength, symbol%6)
}

func TestParseCandidateIndicesDecodesSyntheticPower16Message(t *testing.T) {
	const chipLength = 72
	parser := NewParser(chipLength).(*Parser)
	decoder := protocol.NewDecoder()
	decoder.RegisterProtocol(parser)
	decoder.Allocate()

	want := R900{
		ID:          0x1234abcd,
		Unkn1:       0x5a,
		NoUse:       0x21,
		BackFlow:    2,
		Consumption: 0xabc123,
		Unkn3:       1,
		Leak:        9,
		LeakNow:     3,
	}
	symbols := r900TestPayloadSymbols(want)
	r900TestAddParity(parser, &symbols)
	copy(want.checksum[:], symbols[16:])

	history := make(power16HistoryFixture, decoder.Cfg.BufferLength)
	payloadOffset := decoder.Cfg.PreambleLength - decoder.Cfg.SymbolLength
	for idx, symbol := range symbols {
		r900TestRenderSymbol(history, payloadOffset+idx*chipLength*8, chipLength, symbol)
	}
	parser.SetPower16History(history)

	messages := parser.ParseCandidateIndices([]int{0}, nil)
	if len(messages) != 1 {
		t.Fatalf("decoded %d messages, want 1", len(messages))
	}
	got, ok := messages[0].(R900)
	if !ok {
		t.Fatalf("decoded message type %T, want R900", messages[0])
	}
	if got != want {
		t.Fatalf("decoded message = %+v, want %+v", got, want)
	}

	r900TestRenderSymbol(history, payloadOffset, chipLength, symbols[0]^1)
	if messages := parser.ParseCandidateIndices([]int{0}, nil); len(messages) != 0 {
		t.Fatalf("decoded %d messages after payload corruption, want 0", len(messages))
	}
}
