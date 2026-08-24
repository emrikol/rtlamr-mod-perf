package protocol

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bemasher/rtlamr/csv"
)

const (
	TimeFormat = "2006-01-02T15:04:05.000"
)

var (
	parserMutex sync.Mutex
	parsers     = make(map[string]NewParserFunc)
)

type NewParserFunc func(symbolLength int) Parser

// Given a name and a parser, register a parser for use.
// Later used by underscore importing each parser package:
//
// import _ "github.com/bemasher/rtlamr/scm"
func RegisterParser(name string, parserFn NewParserFunc) {
	parserMutex.Lock()
	defer parserMutex.Unlock()

	if parserFn == nil {
		panic("parser: new parser func is nil")
	}
	if _, dup := parsers[name]; dup {
		panic(fmt.Sprintf("parser: parser already registered (%s)", name))
	}
	parsers[name] = parserFn
}

// Given a name and symbolLength, lookup the parser and make a new one.
func NewParser(name string, symbolLength int) (Parser, error) {
	parserMutex.Lock()
	defer parserMutex.Unlock()

	if parserFn, exists := parsers[name]; exists {
		return parserFn(symbolLength), nil
	} else {
		return nil, fmt.Errorf("invalid message type: %q\n", name)
	}
}

// Used by parsers to interpret received bits/bytes
// into their appropriate fields.
type Data struct {
	Idx   int
	Bits  string
	Bytes []byte
}

func NewData(data []byte) (d Data) {
	d = newData(data, true)
	return
}

func newData(data []byte, includeBits bool) (d Data) {
	d.Bytes = make([]byte, len(data))
	copy(d.Bytes, data)
	if !includeBits {
		return
	}
	var bits strings.Builder
	bits.Grow(len(data) << 3)
	for _, b := range data {
		for shift := uint(8); shift > 0; shift-- {
			bits.WriteByte('0' + (b>>(shift-1))&1)
		}
	}
	d.Bits = bits.String()

	return
}

// A Parser converts slices of bytes to messages.
type Parser interface {
	Parse([]Data, []Message) []Message
	SetDecoder(*Decoder)
	Cfg() PacketConfig
}

// CandidateIndexParser is an optional parser capability for protocols that
// need only the preamble positions, not the packet bit and byte projections
// produced by Decoder.Slice. The indices alias decoder scratch and must not be
// modified or retained after the call returns.
type CandidateIndexParser interface {
	ParseCandidateIndices([]int, []Message) []Message
}

// PacketBytesOnly is an optional parser capability for protocols that consume
// packet bytes but never inspect Data.Bits. When every packet parser for a
// preamble reports this capability, the decoder omits the redundant bit-string
// projection while preserving owned packet bytes and candidate indices.
type PacketBytesOnly interface {
	PacketBytesOnly() bool
}

// Power16Compatible is an optional parser capability. A decoder may use an
// exact uint16 power pipeline only when every registered parser reports that
// it supports that representation. Parser intentionally does not embed this
// interface so unknown and external parsers retain the float64 path without
// source changes.
type Power16Compatible interface {
	Power16Compatible() bool
}

// Power16History provides read-only access to decoder-owned integer power.
// Returned windows alias decoder storage and must not be modified or retained
// after the decoder advances to the next input block.
type Power16History interface {
	Power16Window(idx, length int) []uint16
}

// Power16HistoryConsumer is implemented by parsers that need retained integer
// power for payload processing. Passing nil detaches the integer history and
// restores the parser's ordinary float64 path.
type Power16HistoryConsumer interface {
	SetPower16History(Power16History)
}

type Message interface {
	csv.Recorder
	MsgType() string
	MeterID() uint32
	MeterType() uint8
	Checksum() []byte
}

// Uniquely identifies a message spanning two sample blocks.
type Digest struct {
	MsgType   string
	MeterType uint8
	MeterID   uint32
	Checksum  string
}

func NewDigest(msg Message) Digest {
	return Digest{
		msg.MsgType(),
		msg.MeterType(),
		msg.MeterID(),
		string(msg.Checksum()),
	}
}

// A LogMessage associates a message with a point in time and an offset and
// length into a binary sample file.
type LogMessage struct {
	Time   time.Time `xml:",attr"`
	Offset int64     `xml:",attr"`
	Length int       `xml:",attr"`
	Type   string    `xml:",attr"`
	Message
}

func (msg LogMessage) String() string {
	return fmt.Sprintf("{Time:%s Offset:%d Length:%d %s:%s}",
		msg.Time.Format(TimeFormat), msg.Offset, msg.Length, msg.MsgType(), msg.Message,
	)
}

func (msg LogMessage) StringNoOffset() string {
	return fmt.Sprintf("{Time:%s %s:%s}", msg.Time.Format(TimeFormat), msg.MsgType(), msg.Message)
}

func (msg LogMessage) Record() (r []string) {
	r = append(r, msg.Time.Format(time.RFC3339Nano))
	r = append(r, strconv.FormatInt(msg.Offset, 10))
	r = append(r, strconv.FormatInt(int64(msg.Length), 10))
	r = append(r, msg.Message.Record()...)
	return r
}

// A FilterChain takse a list of filters and applies them iteratively to
// messages sent through the chain.
type FilterChain []MessageFilter

func (fc *FilterChain) Add(filter MessageFilter) {
	*fc = append(*fc, filter)
}

func (fc FilterChain) Match(msg Message) bool {
	if len(fc) == 0 {
		return true
	}

	for _, filter := range fc {
		if !filter.Filter(msg) {
			return false
		}
	}

	return true
}

type MessageFilter interface {
	Filter(Message) bool
}
