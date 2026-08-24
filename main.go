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
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"

	"github.com/bemasher/rtlamr/protocol"
	"github.com/bemasher/rtlamr/r900"
	"github.com/bemasher/rtltcp"

	_ "github.com/bemasher/rtlamr/idm"
	_ "github.com/bemasher/rtlamr/netidm"
	_ "github.com/bemasher/rtlamr/r900bcd"
	_ "github.com/bemasher/rtlamr/scm"
	_ "github.com/bemasher/rtlamr/scmplus"
)

var rcvr Receiver

func startCPUProfiler() func() {
	if *cpuProfile == "" {
		return func() {}
	}

	profileFile, err := os.Create(*cpuProfile)
	if err != nil {
		slog.Error("create CPU profile", "error", err)
		os.Exit(1)
	}
	if err := pprof.StartCPUProfile(profileFile); err != nil {
		_ = profileFile.Close()
		slog.Error("start CPU profile", "error", err)
		os.Exit(1)
	}

	var once sync.Once
	stop := func() {
		once.Do(func() {
			pprof.StopCPUProfile()
			if err := profileFile.Close(); err != nil {
				slog.Error("close CPU profile", "error", err)
			}
			slog.Info("CPU profile complete", "path", *cpuProfile)
		})
	}

	if *cpuProfileDuration > 0 {
		go func() {
			timer := time.NewTimer(*cpuProfileDuration)
			defer timer.Stop()
			<-timer.C
			stop()
		}()
	}

	return stop
}

type Receiver struct {
	rtltcp.SDR
	source            receiverSource
	d                 protocol.Decoder
	protocolNames     []string
	fc                protocol.FilterChain
	continuousCapture *blockCapture
	duty              *dutyRuntime

	ctx       context.Context
	cancel    context.CancelFunc
	wg        *sync.WaitGroup
	closeOnce sync.Once

	err error
}

const receiverReadBlocks = 16
const receiverRateWindow = 10 * time.Second

type receiverRunState struct {
	sampleBuf     bytes.Buffer
	recordSamples bool
	sampleLength  int
	prev          map[protocol.Digest]bool
	next          map[protocol.Digest]bool
	dutyPrev      map[protocol.Digest]bool
	dutyNext      map[protocol.Digest]bool
}

func expandAllMessageTypes() {
	if _, all := msgType["all"]; all && len(msgType) == 1 {
		delete(msgType, "all")
		msgType["scm"] = true
		msgType["scm+"] = true
		msgType["idm"] = true
		msgType["r900"] = true
	}
}

func sortedProtocolNames(types StringMap) []string {
	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (rcvr *Receiver) newProtocolDecoder() (protocol.Decoder, error) {
	decoder := protocol.NewDecoder()
	for _, name := range rcvr.protocolNames {
		parser, err := protocol.NewParser(name, *symbolLength)
		if err != nil {
			return protocol.Decoder{}, err
		}
		decoder.RegisterProtocol(parser)
	}
	decoder.Allocate()
	return decoder, nil
}

func (rcvr *Receiver) NewReceiver() {
	rcvr.ctx, rcvr.cancel = context.WithCancel(context.Background())
	rcvr.wg = &sync.WaitGroup{}
	rcvr.closeOnce = sync.Once{}
	rcvr.err = nil
	rcvr.protocolNames = rcvr.protocolNames[:0]

	expandAllMessageTypes()
	rcvr.protocolNames = append(rcvr.protocolNames, sortedProtocolNames(msgType)...)
	var err error
	rcvr.d, err = rcvr.newProtocolDecoder()
	if err != nil {
		slog.Error("message type", "error", err)
		os.Exit(1)
	}
	if msgType["r900"] || msgType["r900bcd"] {
		status := r900.Power16QuantizerDispatchStatus()
		slog.Info("R900 Power16 quantizer",
			"implementation", status.Implementation,
			"fallback_reason", status.FallbackReason,
			"midr", status.MIDR,
			"asimd", status.ASIMD,
			"genuine_a72", status.GenuineA72,
			"self_test_passed", status.SelfTestPassed,
			"kill_switch", status.KillSwitch,
			"kill_switch_name", status.KillSwitchName,
			"native_available", status.NativeAvailable,
			"native_calls", status.NativeCalls,
			"portable_calls", status.PortableCalls,
		)
	}

	cfg := rcvr.d.Cfg

	visited := make(map[string]bool)
	gainFlagSet := false
	flag.Visit(func(f *flag.Flag) {
		visited[f.Name] = true
		switch f.Name {
		case "centerfreq":
			cfg.CenterFreq = uint32(rcvr.Flags.CenterFreq)
		case "samplerate":
			cfg.SampleRate = int(rcvr.Flags.SampleRate)
		case "gainbyindex", "tunergainmode", "tunergain", "agcmode":
			gainFlagSet = true
		case "unique":
			if f.Value.String() == "true" {
				rcvr.fc.Add(NewUniqueFilter())
			}
		case "filterid":
			rcvr.fc.Add(meterID)
		case "filtertype":
			rcvr.fc.Add(meterType)
		}
	})

	switch strings.ToLower(*inputSource) {
	case "tcp":
		if rcvr.err = rcvr.Connect(nil); rcvr.err != nil {
			slog.Error("receiver connect", "error", errors.Wrap(rcvr.err, "rcvr.Connect"))
			os.Exit(1)
		}
		if rcvr.err = rcvr.HandleFlags(); rcvr.err != nil {
			slog.Error("configure rtl_tcp", "error", rcvr.err)
			os.Exit(1)
		}
		if rcvr.err = rcvr.SetCenterFreq(cfg.CenterFreq); rcvr.err == nil {
			rcvr.err = rcvr.SetSampleRate(uint32(cfg.SampleRate))
		}
		if rcvr.err == nil && !gainFlagSet {
			rcvr.err = rcvr.SetGainMode(true)
		}
		if rcvr.err != nil {
			slog.Error("configure rtl_tcp defaults", "error", rcvr.err)
			os.Exit(1)
		}
		rcvr.source, rcvr.err = newStreamReceiverSource(rcvr.TCPConn, rcvr.TCPConn, cfg.BlockSize2, receiverReadBlocks)
		if rcvr.err != nil {
			slog.Error("configure rtl_tcp batches", "error", rcvr.err)
			os.Exit(1)
		}
	case "direct":
		if !directRTLSourceAvailable() {
			slog.Error("direct RTL-SDR source is unavailable in this build")
			os.Exit(1)
		}
		directConfig := directRTLConfig{
			Device:            *directDevice,
			CenterFreq:        cfg.CenterFreq,
			SampleRate:        uint32(cfg.SampleRate),
			BlockBytes:        uint32(cfg.BlockSize2),
			BatchBytes:        uint32(cfg.BlockSize2 * receiverReadBlocks),
			TunerGainModeSet:  visited["tunergainmode"] || !gainFlagSet,
			TunerGainMode:     rcvr.Flags.TunerGainMode || !gainFlagSet,
			TunerGainSet:      visited["tunergain"],
			TunerGainTenthsDB: int(rcvr.Flags.TunerGain * 10),
			GainByIndexSet:    visited["gainbyindex"],
			GainByIndex:       uint32(rcvr.Flags.GainByIndex),
			FreqCorrectionSet: visited["freqcorrection"],
			FreqCorrectionPPM: rcvr.Flags.FreqCorrection,
			TestModeSet:       visited["testmode"],
			TestMode:          rcvr.Flags.TestMode,
			AGCModeSet:        visited["agcmode"],
			AGCMode:           rcvr.Flags.AgcMode,
			DirectSamplingSet: visited["directsampling"],
			DirectSampling:    rcvr.Flags.DirectSampling,
			OffsetTuningSet:   visited["offsettuning"],
			OffsetTuning:      rcvr.Flags.OffsetTuning,
			RTLXtalFreqSet:    visited["rtlxtalfreq"],
			RTLXtalFreq:       uint32(rcvr.Flags.RtlXtalFreq),
			TunerXtalFreqSet:  visited["tunerxtalfreq"],
			TunerXtalFreq:     uint32(rcvr.Flags.TunerXtalFreq),
		}
		var tunerType, gainCount uint32
		rcvr.source, tunerType, gainCount, rcvr.err = newDirectRTLSource(directConfig)
		if rcvr.err != nil {
			slog.Error("open direct RTL-SDR source", "error", rcvr.err)
			os.Exit(1)
		}
		rcvr.Info.Tuner = rtltcp.Tuner(tunerType)
		rcvr.Info.GainCount = gainCount
	default:
		slog.Error("invalid sample source: " + *inputSource)
		os.Exit(1)
	}

	rcvr.d.Cfg = cfg
	if *dutySchedulerMode != "off" {
		ids := make([]uint64, 0, len(meterID.UintMap))
		for id := range meterID.UintMap {
			ids = append(ids, uint64(id))
		}
		rcvr.duty, rcvr.err = newDutyRuntimeWithCaptureTarget(*dutySchedulerMode, rcvr.d.Cfg, ids, *dutySchedulerCaptureTarget/100)
		if rcvr.err != nil {
			slog.Error("configure DSP duty scheduler", "error", rcvr.err)
			os.Exit(1)
		}
		if *dutySchedulerCheckpointDir != "" {
			if rcvr.err = rcvr.duty.configureCheckpoints(*dutySchedulerCheckpointDir, time.Hour); rcvr.err != nil {
				slog.Error("configure DSP duty scheduler checkpoints", "error", rcvr.err)
				os.Exit(1)
			}
		}
	}
	rcvr.d.Log()

	if *continuousSampleFile != os.DevNull {
		rcvr.continuousCapture, rcvr.err = newBlockCapture(
			continuousSampleWriter,
			cfg.SampleRate,
			cfg.BlockSize2,
			*continuousSampleDuration,
		)
		if rcvr.err != nil {
			slog.Error("configure continuous sample capture", "error", rcvr.err)
			os.Exit(1)
		}
		slog.Info("continuous sample capture configured",
			"path", *continuousSampleFile,
			"duration", continuousSampleDuration.String(),
			"blocks", rcvr.continuousCapture.blocksRemaining,
		)
	}

	slog.Info(rcvr.source.Name(), "tuner", rcvr.SDR.Info.Tuner, "GainCount", rcvr.SDR.Info.GainCount)
}

func (rcvr *Receiver) Close() {
	rcvr.closeOnce.Do(func() {
		rcvr.cancel()

		// Cancel interrupts either a blocked TCP read or the asynchronous USB
		// reader. Close is deferred until the decoder goroutine releases any
		// batch it currently owns.
		if rcvr.source != nil {
			if err := rcvr.source.Cancel(); err != nil && rcvr.err == nil {
				rcvr.err = errors.Wrap(err, "cancel sample source")
			}
		}
		rcvr.wg.Wait()
		if rcvr.source != nil {
			if err := rcvr.source.Close(); err != nil && rcvr.err == nil {
				rcvr.err = errors.Wrap(err, "close sample source")
			}
		}
		if rcvr.duty != nil {
			if err := rcvr.duty.writeFinalCheckpoint(time.Now()); err != nil {
				slog.Error("write final DSP duty scheduler checkpoint", "error", err)
			}
			if err := rcvr.duty.writeReport(*dutySchedulerReport); err != nil && rcvr.err == nil {
				rcvr.err = errors.Wrap(err, "write DSP duty scheduler report")
			}
		}
	})
}

func (rcvr *Receiver) Run() {
	rcvr.wg.Add(1)

	state := receiverRunState{
		recordSamples: *sampleFile != os.DevNull,
		prev:          map[protocol.Digest]bool{},
		next:          map[protocol.Digest]bool{},
		dutyPrev:      map[protocol.Digest]bool{},
		dutyNext:      map[protocol.Digest]bool{},
	}

	// Read and decode on one goroutine. The optimized decoder is much faster
	// than the live block interval, so a separate reader, an unbuffered channel,
	// and alternating ownership buffers add scheduler work without useful
	// overlap.
	go func() {
		defer rcvr.cancel()
		defer rcvr.wg.Done()

		bytesRead := 0
		rateStarted := time.Now()
		blockBytes := rcvr.d.Cfg.BlockSize2

		for {
			select {
			case <-rcvr.ctx.Done():
				return
			default:
			}

			batch, readErr := rcvr.source.Next()
			if readErr != nil {
				if rcvr.ctx.Err() != nil {
					return
				}
				rcvr.err = errors.Wrap(readErr, "sample source")
				return
			}
			if len(batch) == 0 || len(batch)%blockBytes != 0 {
				rcvr.err = fmt.Errorf("sample source returned invalid batch length %d", len(batch))
				_ = rcvr.source.Release()
				return
			}
			bytesRead += len(batch)

			now := time.Now()
			if elapsed := now.Sub(rateStarted); elapsed >= receiverRateWindow {
				rate := receiverSampleRate(bytesRead, elapsed)
				if rate < int64(rcvr.d.Cfg.SampleRate)*99/100 {
					slog.Error("not keeping up with sample input", "rate", rate)
				}
				bytesRead = 0
				rateStarted = now
			}

			keepRunning := true
			for offset := 0; offset < len(batch); offset += blockBytes {
				if !rcvr.processBlock(&state, batch[offset:offset+blockBytes]) {
					keepRunning = false
					break
				}
			}
			if releaseErr := rcvr.source.Release(); releaseErr != nil {
				if rcvr.err == nil {
					rcvr.err = errors.Wrap(releaseErr, "release sample source batch")
				}
				return
			}
			if !keepRunning {
				return
			}
		}
	}()
}

func receiverSampleRate(bytesRead int, elapsed time.Duration) int64 {
	if bytesRead <= 0 || elapsed <= 0 {
		return 0
	}
	// rtl_tcp supplies one unsigned byte for I and one for Q. Calculate from
	// the actual monotonic interval so larger reads cannot alias against a
	// one-second ticker and produce a false shortfall.
	return int64(bytesRead) * int64(time.Second) / (2 * int64(elapsed))
}

func (rcvr *Receiver) processBlock(state *receiverRunState, block []byte) bool {
	if rcvr.continuousCapture != nil && !rcvr.continuousCapture.Complete() {
		if err := rcvr.continuousCapture.WriteBlock(block); err != nil {
			rcvr.err = errors.Wrap(err, "continuous sample capture")
			return false
		}
		if rcvr.continuousCapture.Complete() {
			slog.Info("continuous sample capture complete",
				"path", *continuousSampleFile,
				"bytes", rcvr.continuousCapture.BytesWritten(),
			)
		}
	}

	// Discard the oldest block from the buffer if
	// it's full and write the new block to it.
	if state.sampleLength > rcvr.d.Cfg.BufferLength<<1 {
		state.sampleLength -= len(block)
		if state.recordSamples {
			_, _ = io.CopyN(io.Discard, &state.sampleBuf, int64(len(block)))
		}
	}
	state.sampleLength += len(block)
	if state.recordSamples {
		_, _ = state.sampleBuf.Write(block)
	}

	pktFound := false
	if rcvr.duty == nil {
		messages := rcvr.d.Decode(block)
		var keepRunning bool
		pktFound, keepRunning = rcvr.processDecodedMessages(state, messages, 0, time.Time{}, true, false)
		if !keepRunning {
			return false
		}
	} else {
		start, end, decision := rcvr.duty.beginBlock()
		if rcvr.duty.needsRebuild(decision) {
			replayedFound, keepRunning := rcvr.rebuildDutyDecoder(state)
			pktFound = pktFound || replayedFound
			if !keepRunning {
				return false
			}
		}
		if decision.Decode {
			messages := rcvr.d.Decode(block)
			currentFound, keepRunning := rcvr.processDecodedMessages(state, messages, end, time.Time{}, true, true)
			pktFound = pktFound || currentFound
			if !keepRunning {
				return false
			}
		} else {
			_, _ = rcvr.processDecodedMessages(state, nil, end, time.Time{}, false, false)
		}
		rcvr.duty.finishBlock(block, start, end, time.Now(), decision)
		if err := rcvr.duty.maybeCheckpoint(time.Now()); err != nil {
			slog.Error("write DSP duty scheduler checkpoint", "error", err)
		}
	}

	if pktFound && state.recordSamples {
		_, err := sampleWriter.Write(state.sampleBuf.Bytes())
		if err != nil {
			rcvr.err = errors.Wrap(err, "write raw samples")
			return false
		}
		if *single && len(meterID.UintMap) == 0 {
			rcvr.cancel()
			return false
		}
	}

	return true
}

func clearDigestMap(values map[protocol.Digest]bool) {
	for key := range values {
		delete(values, key)
	}
}

func (rcvr *Receiver) processDecodedMessages(state *receiverRunState, messages []protocol.Message, sampleAt time.Duration, eventTime time.Time, publish, observe bool) (bool, bool) {
	clearDigestMap(state.next)
	if rcvr.duty != nil {
		clearDigestMap(state.dutyNext)
		if observe {
			for _, msg := range messages {
				digest := protocol.NewDigest(msg)
				state.dutyNext[digest] = true
				if !state.dutyPrev[digest] {
					rcvr.duty.observe(uint64(msg.MeterID()), msg.MsgType(), sampleAt)
				}
			}
		}
	}

	pktFound := false
	if publish {
		for _, msg := range messages {
			// If the filterchain rejects the message, skip it.
			if !rcvr.fc.Match(msg) {
				continue
			}

			// Make a new LogMessage
			var logMsg protocol.LogMessage
			if eventTime.IsZero() {
				logMsg.Time = time.Now()
			} else {
				logMsg.Time = eventTime
			}
			if s, ok := sampleWriter.(io.Seeker); ok {
				logMsg.Offset, _ = s.Seek(0, io.SeekCurrent)
			}
			logMsg.Length = state.sampleLength
			logMsg.Type = msg.MsgType()
			logMsg.Message = msg

			// This should be unique enough to identify a message between blocks.
			msgDigest := protocol.NewDigest(msg)

			// Mark the message as seen for the next loop.
			state.next[msgDigest] = true

			// If the message was seen in the previous loop, skip it.
			if state.prev[msgDigest] {
				continue
			}

			// Encode the message
			rcvr.err = encoder.Encode(logMsg)
			rcvr.err = errors.Wrap(rcvr.err, "encoder.Encode")

			if rcvr.err != nil {
				return false, false
			}

			pktFound = true
			if *single {
				if len(meterID.UintMap) == 0 {
					break
				} else {
					delete(meterID.UintMap, uint(msg.MeterID()))
				}
			}
		}
	}

	state.next, state.prev = state.prev, state.next
	if rcvr.duty != nil {
		state.dutyNext, state.dutyPrev = state.dutyPrev, state.dutyNext
	}
	return pktFound, true
}

func (rcvr *Receiver) rebuildDutyDecoder(state *receiverRunState) (bool, bool) {
	oldCfg := rcvr.d.Cfg
	fresh, err := rcvr.newProtocolDecoder()
	if err != nil {
		rcvr.err = fmt.Errorf("dutyscheduler: rebuild decoder: %w", err)
		return false, false
	}
	if fresh.Cfg.BlockSize != oldCfg.BlockSize || fresh.Cfg.BlockSize2 != oldCfg.BlockSize2 || fresh.Cfg.BufferLength != oldCfg.BufferLength {
		rcvr.err = errors.New("dutyscheduler: decoder geometry changed during wake")
		return false, false
	}
	fresh.Cfg.CenterFreq = oldCfg.CenterFreq
	fresh.Cfg.SampleRate = oldCfg.SampleRate
	rcvr.d = fresh

	clearDigestMap(state.prev)
	clearDigestMap(state.next)
	clearDigestMap(state.dutyPrev)
	clearDigestMap(state.dutyNext)
	pktFound := false
	entries := rcvr.duty.orderedCollar()
	for idx, entry := range entries {
		messages := rcvr.d.Decode(entry.data)
		publish := !entry.decoded && idx >= rcvr.duty.warmupBlocks
		found, keepRunning := rcvr.processDecodedMessages(state, messages, entry.sampleEnd, entry.wallTime, publish, publish)
		pktFound = pktFound || found
		if !keepRunning {
			return pktFound, false
		}
	}
	rcvr.duty.recordRebuild(len(entries))
	return pktFound, true
}

func init() {
	_, file, _, ok := runtime.Caller(0)
	dir := ""
	if ok {
		dir = filepath.Dir(file)
		dir = filepath.Dir(dir) + string(filepath.Separator)
		fmt.Println(dir)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr,
		&slog.HandlerOptions{
			AddSource: true,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				if a.Key == slog.SourceKey {
					source := a.Value.Any().(*slog.Source)
					source.File = strings.TrimPrefix(filepath.FromSlash(source.File), dir)
				}
				return a
			},
		})))
}

func main() {
	rcvr.RegisterFlags()
	RegisterFlags()
	EnvOverride()
	flag.Parse()

	if *version {
		if info, ok := debug.ReadBuildInfo(); ok {
			fmt.Printf("%+v\n", info)
		} else {
			slog.Error("could not read build info")
		}
		os.Exit(0)
	}

	HandleFlags()

	rcvr.NewReceiver()
	stopCPUProfiler := startCPUProfiler()
	defer stopCPUProfiler()

	defer func() {
		// Stop the receiver before closing either writer: a duration timeout can
		// occur while the decoder goroutine still owns an in-flight block.
		rcvr.Close()
		if c, ok := sampleWriter.(io.Closer); ok {
			c.Close()
		}
		if c, ok := continuousSampleWriter.(io.Closer); ok {
			c.Close()
		}

		if rcvr.err != nil {
			slog.Error("receiver", "error", rcvr.err)
			os.Exit(1)
		}
	}()

	start := time.Now()
	rcvr.Run()

	// Setup signal channel for interruption.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)

	// Setup time limit channel
	timeLimitCh := make(<-chan time.Time, 1)
	if *timeLimit != 0 {
		timeLimitCh = time.After(*timeLimit)
	}

	select {
	case sig := <-sigCh:
		slog.Info("signal received", "signal", sig)
	case <-timeLimitCh:
		slog.Info("time limit reached:", "since", time.Since(start))
	case <-rcvr.ctx.Done():
		slog.Info("receiver context cancelled")
	}

	rcvr.Close()
}
