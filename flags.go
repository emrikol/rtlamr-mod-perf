// RTLAMR - An rtl-sdr receiver for smart meters operating in the 900MHz ISM band.
// Copyright (C) 2015 Douglas Hall
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

package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bemasher/rtlamr/csv"
	"github.com/bemasher/rtlamr/protocol"
)

var (
	sampleFile               = flag.String("samplefile", os.DevNull, "packet-triggered raw signal dump file")
	sampleWriter             = io.Discard
	continuousSampleFile     = flag.String("continuoussamplefile", os.DevNull, "bounded continuous raw signal dump file")
	continuousSampleDuration = flag.Duration("continuoussampleduration", 0, "amount of raw signal to capture; required with -continuoussamplefile")
	continuousSampleWriter   = io.Discard
)

var (
	inputSource             = flag.String("source", "tcp", "sample source: tcp or direct")
	directDevice            = flag.String("device", "0", "RTL-SDR device index or serial for the direct source")
	directKernelRing        = flag.Bool("directkernelring", false, "use the optional Linux kernel-owned direct-SDR ring")
	directKernelBatchBlocks = flag.Int("directkernelbatchblocks", receiverReadBlocks, "decoder blocks per kernel-ring USB batch")
)

var msgType StringMap

var symbolLength = flag.Int("symbollength", 72, "symbol length in samples (8, 32, 40, 48, 56, 64, 72, 80, 88, 96)")

var (
	timeLimit = flag.Duration("duration", 0, "time to run for, 0 for infinite, ex. 1h5m10s")
	meterID   MeterIDFilter
	meterType MeterTypeFilter
)

var _ = flag.Bool("unique", false, "suppress duplicate messages from each meter")

var (
	encoder Encoder
	format  = flag.String("format", "plain", "decoded message output format: plain, csv, json, or xml")
)

var single = flag.Bool("single", false, "one shot execution, if used with -filterid, will wait for exactly one packet from each meter id")

var version = flag.Bool("version", false, "display build date and commit hash")

var (
	cpuProfile                 = flag.String("cpuprofile", "", "write a CPU profile to this file")
	cpuProfileDuration         = flag.Duration("cpuprofileduration", 0, "stop CPU profiling after this duration, 0 for process lifetime")
	dutySchedulerMode          = flag.String("dutyscheduler", "off", "DSP duty scheduler mode: off, shadow, or gated")
	dutySchedulerCaptureTarget = flag.Float64("dutyschedulercapturetarget", 99.5, "per-sender scheduler capture target percentage (greater than 0 and less than 100)")
	dutySchedulerReport        = flag.String("dutyschedulerreport", "", "write the final DSP duty scheduler report as JSON")
	dutySchedulerCheckpointDir = flag.String("dutyschedulercheckpointdir", "", "resume the strongest compatible checkpoint, then write hourly atomic DSP duty scheduler state in this existing directory")
	dutySchedulerPolicy        = flag.String("dutyschedulerpolicy", "", "optional protected JSON policy file for DSP duty scheduler controller and per-sender watchdog seeds")
)

func RegisterFlags() {
	msgType = StringMap{"scm": true}
	flag.Var(msgType, "msgtype", "comma-separated list of message types to receive: all, scm, scm+, idm, netidm, r900 and r900bcd")

	meterID = MeterIDFilter{make(UintMap)}
	meterType = MeterTypeFilter{make(UintMap)}

	flag.Var(meterID, "filterid", "display only messages matching an id in a comma-separated list of ids.")
	flag.Var(meterType, "filtertype", "display only messages matching a type in a comma-separated list of types.")

	rtlamrFlags := map[string]bool{
		"samplefile":                 true,
		"continuoussamplefile":       true,
		"continuoussampleduration":   true,
		"msgtype":                    true,
		"symbollength":               true,
		"duration":                   true,
		"filterid":                   true,
		"filtertype":                 true,
		"format":                     true,
		"unique":                     true,
		"single":                     true,
		"cpuprofile":                 true,
		"cpuprofileduration":         true,
		"dutyscheduler":              true,
		"dutyschedulercapturetarget": true,
		"dutyschedulerreport":        true,
		"dutyschedulercheckpointdir": true,
		"dutyschedulerpolicy":        true,
		"version":                    true,
		"source":                     true,
		"device":                     true,
		"directkernelring":           true,
		"directkernelbatchblocks":    true,
	}

	printDefaults := func(validFlags map[string]bool, inclusion bool) {
		flag.CommandLine.VisitAll(func(f *flag.Flag) {
			if validFlags[f.Name] != inclusion {
				return
			}

			format := "  -%s=%s: %s\n"
			fmt.Fprintf(os.Stderr, format, f.Name, f.Value, f.Usage)
		})
	}

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", filepath.Base(os.Args[0]))
		printDefaults(rtlamrFlags, true)

		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "rtltcp specific:")
		printDefaults(rtlamrFlags, false)
	}
}

func EnvOverride() {
	flag.VisitAll(func(f *flag.Flag) {
		envName := "RTLAMR_" + strings.ToUpper(f.Name)
		flagValue := os.Getenv(envName)
		if flagValue != "" {
			if err := flag.Set(f.Name, flagValue); err != nil {
				log.Printf(
					"Environment variable %q failed to override flag %q with value %q: %q\n",
					envName, f.Name, flagValue, err,
				)
			} else {
				log.Printf("Environment variable %q overrides flag %q with %q\n", envName, f.Name, flagValue)
			}
		}
	})
}

func HandleFlags() {
	var err error
	if *dutySchedulerCaptureTarget <= 0 || *dutySchedulerCaptureTarget >= 100 {
		log.Fatal("dutyschedulercapturetarget must be greater than 0 and less than 100")
	}

	switch *symbolLength {
	case 8, 32, 40, 48, 56, 64, 72, 80, 88, 96:
		break
	default:
		log.Fatal("invalid symbollength")
	}

	*format = strings.ToLower(*format)
	encoder, err = newOutputEncoder(*format, os.Stdout, *sampleFile)
	if err != nil {
		log.Fatal(err)
	}

	if *sampleFile != os.DevNull {
		sampleWriter, err = os.Create(*sampleFile)
		if err != nil {
			log.Fatal("Error creating sample file:", err)
		}
	}
	if *continuousSampleFile != os.DevNull {
		if *continuousSampleDuration <= 0 {
			log.Fatal("continuoussampleduration must be positive when continuoussamplefile is set")
		}
		continuousSampleWriter, err = os.Create(*continuousSampleFile)
		if err != nil {
			log.Fatal("Error creating continuous sample file:", err)
		}
	} else if *continuousSampleDuration != 0 {
		log.Fatal("continuoussamplefile must be set when continuoussampleduration is non-zero")
	}

	*dutySchedulerMode = strings.ToLower(*dutySchedulerMode)
	if *directKernelRing && strings.ToLower(*inputSource) != "direct" {
		log.Fatal("directkernelring requires source=direct")
	}
	if err := validateDirectKernelBatchBlocks(*directKernelBatchBlocks); err != nil {
		log.Fatal(err)
	}
	if *directKernelBatchBlocks != receiverReadBlocks && !*directKernelRing {
		log.Fatal("directkernelbatchblocks requires directkernelring")
	}
	switch *dutySchedulerMode {
	case "off":
		if *dutySchedulerReport != "" || *dutySchedulerCheckpointDir != "" || *dutySchedulerPolicy != "" {
			log.Fatal("dutyscheduler report and checkpoint directory require dutyscheduler=shadow or gated")
		}
	case "shadow", "gated":
		if *dutySchedulerReport == "" {
			log.Fatal("dutyschedulerreport is required when dutyscheduler is enabled")
		}
		if len(meterID.UintMap) == 0 {
			log.Fatal("dutyscheduler requires an explicit filterid sender inventory")
		}
		if *dutySchedulerMode == "gated" && (*sampleFile != os.DevNull || *single) {
			log.Fatal("gated dutyscheduler is incompatible with samplefile and single")
		}
	default:
		log.Fatal("invalid dutyscheduler mode")
	}
}

func validateDirectKernelBatchBlocks(blocks int) error {
	if blocks < 1 || blocks > 256 {
		return fmt.Errorf("directkernelbatchblocks must be between 1 and 256")
	}
	return nil
}

func newOutputEncoder(outputFormat string, output io.Writer, sampleFilename string) (Encoder, error) {
	switch strings.ToLower(outputFormat) {
	case "plain":
		return PlainEncoder{sampleFilename}, nil
	case "csv":
		return csv.NewEncoder(output), nil
	case "json":
		return json.NewEncoder(output), nil
	case "xml":
		return NewLineEncoder{xml.NewEncoder(output)}, nil
	default:
		return nil, fmt.Errorf("invalid output format %q", outputFormat)
	}
}

// JSON, XML and GOB all implement this interface so we can simplify log
// output formatting.
type Encoder interface {
	Encode(interface{}) error
}

// The XML encoder doesn't write new lines after each element, make a wrapper
// for the Encoder interface that prints a new line after each call.
type NewLineEncoder struct {
	Encoder
}

func (nle NewLineEncoder) Encode(e interface{}) error {
	err := nle.Encoder.Encode(e)
	fmt.Println()
	return err
}

// A Flag value that populates a map of string to bool from a comma-separated list.
type StringMap map[string]bool

func (m StringMap) String() (s string) {
	var keys []string
	for key := range m {
		keys = append(keys, key)
	}
	return strings.Join(keys, ",")
}

func (m StringMap) Set(value string) error {
	// Delete any default keys.
	var keys []string
	for key := range m {
		keys = append(keys, key)
	}
	for _, key := range keys {
		delete(m, key)
	}

	// Set keys from value.
	for _, val := range strings.Split(value, ",") {
		m[strings.ToLower(val)] = true
	}

	return nil
}

type UintMap map[uint]bool

func (m UintMap) String() (s string) {
	var values []string
	for k := range m {
		values = append(values, strconv.FormatUint(uint64(k), 10))
	}
	return strings.Join(values, ",")
}

func (m UintMap) Set(value string) error {
	values := strings.Split(value, ",")

	for _, v := range values {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return err
		}

		m[uint(n)] = true
	}

	return nil
}

type MeterIDFilter struct {
	UintMap
}

func (m MeterIDFilter) Filter(msg protocol.Message) bool {
	return m.UintMap[uint(msg.MeterID())]
}

type MeterTypeFilter struct {
	UintMap
}

func (m MeterTypeFilter) Filter(msg protocol.Message) bool {
	return m.UintMap[uint(msg.MeterType())]
}

type UniqueFilter map[uint][]byte

func NewUniqueFilter() UniqueFilter {
	return make(UniqueFilter)
}

func (uf UniqueFilter) Filter(msg protocol.Message) bool {
	checksum := msg.Checksum()
	mid := uint(msg.MeterID())

	if val, ok := uf[mid]; ok && bytes.Equal(val, checksum) {
		return false
	}

	uf[mid] = make([]byte, len(checksum))
	copy(uf[mid], checksum)
	return true
}

type PlainEncoder struct {
	sampleFilename string
}

func (pe PlainEncoder) Encode(msg interface{}) (err error) {
	if m, ok := msg.(protocol.LogMessage); ok && pe.sampleFilename == os.DevNull {
		_, err = fmt.Println(m.StringNoOffset())
	} else {
		_, err = fmt.Println(m)
	}
	return
}
