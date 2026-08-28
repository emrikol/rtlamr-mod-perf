package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/bemasher/rtlamr/internal/dutyscheduler"
	"github.com/bemasher/rtlamr/protocol"
)

const dutyCollarDuration = 100 * time.Millisecond

func dutyCollarBlockCount(cfg protocol.PacketConfig) int {
	collarBlocks := int((int64(cfg.SampleRate)*int64(dutyCollarDuration) + int64(time.Second)*int64(cfg.BlockSize) - 1) / (int64(time.Second) * int64(cfg.BlockSize)))
	warmupBlocks := (cfg.BufferLength + cfg.BlockSize - 1) / cfg.BlockSize
	if collarBlocks <= warmupBlocks {
		collarBlocks = warmupBlocks + 1
	}
	return collarBlocks
}

type dutyCollarBlock struct {
	data        []byte
	sampleStart time.Duration
	sampleEnd   time.Duration
	wallTime    time.Time
	decoded     bool
	rawValid    bool
}

type dutyRuntime struct {
	mode                 dutyscheduler.Mode
	scheduler            *dutyscheduler.Scheduler
	schedulerConfig      dutyscheduler.Config
	captureTarget        float64
	confidence           float64
	minimumAudit         float64
	recoveryDuration     time.Duration
	refreshInterval      time.Duration
	refreshDuration      time.Duration
	promotionMargin      float64
	promotionStability   time.Duration
	watchdogHistory      int
	watchdogMinIntervals int
	watchdogWindow       time.Duration
	watchdogQuantile     float64
	watchdogMargin       float64
	senderIDs            map[uint64]bool
	sampleRate           int64
	blockSamples         int64
	blockBytes           int
	sampleTime           time.Duration
	sampleRemainder      int64

	collar       []dutyCollarBlock
	collarCount  int
	collarNext   int
	warmupBlocks int
	borrowRaw    bool
	wasSkipped   bool
	skippedRun   int

	decodedBlocks  uint64
	skippedBlocks  uint64
	auditedBlocks  uint64
	refreshBlocks  uint64
	rebuilds       uint64
	replayedBlocks uint64

	startedUTC         time.Time
	checkpointDir      string
	checkpointInterval time.Duration
	nextCheckpointUTC  time.Time
	checkpointSequence uint64
	checkpointFailures uint64
	resume             dutyResumeInfo
}

func (d *dutyRuntime) useBorrowedCollar(retainedBlocks int) error {
	if retainedBlocks < len(d.collar) {
		return fmt.Errorf("dutyscheduler: source retains %d blocks; collar requires %d", retainedBlocks, len(d.collar))
	}
	for idx := range d.collar {
		d.collar[idx].data = nil
	}
	d.borrowRaw = true
	return nil
}

type dutyResumeInfo struct {
	Status             string `json:"status"`
	SourceFile         string `json:"source_file,omitempty"`
	SourceSHA256       string `json:"source_sha256,omitempty"`
	SourceSchema       string `json:"source_schema,omitempty"`
	SourceCreatedUTC   string `json:"source_created_utc,omitempty"`
	SourceSequence     uint64 `json:"source_sequence,omitempty"`
	SkippedCheckpoints uint64 `json:"skipped_checkpoints"`
	PendingReceipt     bool   `json:"-"`
}

type dutySenderETA struct {
	ID                        uint64   `json:"id"`
	Protocol                  string   `json:"protocol"`
	EligibleEvents            uint64   `json:"eligible_events"`
	WouldMiss                 uint64   `json:"would_miss"`
	RequiredEligibleEvents    uint64   `json:"required_eligible_events"`
	RemainingEligibleEvents   uint64   `json:"remaining_eligible_events"`
	EligibleEventsPerHour     float64  `json:"eligible_events_per_hour"`
	EstimatedRemainingSeconds *float64 `json:"estimated_remaining_seconds"`
}

type dutyCandidateETA struct {
	Name                      string          `json:"name"`
	Status                    string          `json:"status"`
	EstimatedRemainingSeconds *float64        `json:"estimated_remaining_seconds"`
	LimitingSenderID          uint64          `json:"limiting_sender_id,omitempty"`
	Senders                   []dutySenderETA `json:"senders"`
}

type dutyTrustETA struct {
	Status                    string             `json:"status"`
	Assumption                string             `json:"assumption"`
	Candidate                 string             `json:"candidate,omitempty"`
	LimitingSenderID          uint64             `json:"limiting_sender_id,omitempty"`
	EstimatedRemainingSeconds *float64           `json:"estimated_remaining_seconds"`
	EstimatedReadyUTC         string             `json:"estimated_ready_utc,omitempty"`
	Candidates                []dutyCandidateETA `json:"candidates"`
}

type dutyRuntimeReport struct {
	Schema               string                 `json:"schema"`
	CreatedUTC           string                 `json:"created_utc"`
	SessionStartedUTC    string                 `json:"session_started_utc"`
	ReportKind           string                 `json:"report_kind"`
	CheckpointSequence   uint64                 `json:"checkpoint_sequence"`
	CheckpointFailures   uint64                 `json:"checkpoint_failures"`
	Mode                 dutyscheduler.Mode     `json:"mode"`
	CaptureTarget        float64                `json:"capture_target"`
	Confidence           float64                `json:"confidence"`
	MinimumAudit         float64                `json:"minimum_audit"`
	RecoveryDurationNS   int64                  `json:"recovery_duration_ns"`
	RefreshIntervalNS    int64                  `json:"refresh_interval_ns"`
	RefreshDurationNS    int64                  `json:"refresh_duration_ns"`
	PromotionMargin      float64                `json:"promotion_margin"`
	PromotionStabilityNS int64                  `json:"promotion_stability_ns"`
	WatchdogHistory      int                    `json:"watchdog_history"`
	WatchdogMinIntervals int                    `json:"watchdog_min_intervals"`
	WatchdogWindowNS     int64                  `json:"watchdog_window_ns"`
	WatchdogQuantile     float64                `json:"watchdog_quantile"`
	WatchdogMargin       float64                `json:"watchdog_margin"`
	SenderIDs            []uint64               `json:"sender_ids"`
	SampleRate           int64                  `json:"sample_rate"`
	BlockSamples         int64                  `json:"block_samples"`
	BlockBytes           int                    `json:"block_bytes"`
	CollarBlocks         int                    `json:"collar_blocks"`
	WarmupBlocks         int                    `json:"warmup_blocks"`
	SampleRemainder      int64                  `json:"sample_remainder"`
	DecodedBlocks        uint64                 `json:"decoded_blocks"`
	SkippedBlocks        uint64                 `json:"skipped_blocks"`
	AuditedBlocks        uint64                 `json:"audited_blocks"`
	RefreshBlocks        uint64                 `json:"refresh_blocks"`
	Rebuilds             uint64                 `json:"rebuilds"`
	ReplayedBlocks       uint64                 `json:"replayed_blocks"`
	TrustETA             dutyTrustETA           `json:"trust_eta"`
	Snapshot             dutyscheduler.Snapshot `json:"snapshot"`
	Resume               dutyResumeInfo         `json:"resume"`
	SchedulerState       dutyscheduler.State    `json:"scheduler_state"`
}

func newDutyRuntime(modeText string, cfg protocol.PacketConfig, ids []uint64) (*dutyRuntime, error) {
	return newDutyRuntimeWithCaptureTarget(modeText, cfg, ids, dutyscheduler.DefaultConfig(dutyscheduler.Mode(modeText), ids).CaptureTarget)
}

func newDutyRuntimeWithCaptureTarget(modeText string, cfg protocol.PacketConfig, ids []uint64, captureTarget float64) (*dutyRuntime, error) {
	mode := dutyscheduler.Mode(modeText)
	if mode == dutyscheduler.ModeOff {
		return nil, nil
	}
	schedulerConfig := dutyscheduler.DefaultConfig(mode, ids)
	schedulerConfig.CaptureTarget = captureTarget
	return newDutyRuntimeWithConfig(cfg, ids, schedulerConfig)
}

func newDutyRuntimeWithConfig(cfg protocol.PacketConfig, ids []uint64, schedulerConfig dutyscheduler.Config) (*dutyRuntime, error) {
	mode := schedulerConfig.Mode
	scheduler, err := dutyscheduler.New(schedulerConfig)
	if err != nil {
		return nil, err
	}
	schedulerConfig = scheduler.Configuration()
	if cfg.SampleRate <= 0 || cfg.BlockSize <= 0 || cfg.BlockSize2 <= 0 || cfg.BufferLength <= 0 {
		return nil, fmt.Errorf("dutyscheduler: invalid decoder geometry")
	}
	collarBlocks := dutyCollarBlockCount(cfg)
	warmupBlocks := (cfg.BufferLength + cfg.BlockSize - 1) / cfg.BlockSize
	runtime := &dutyRuntime{
		mode:                 mode,
		scheduler:            scheduler,
		schedulerConfig:      schedulerConfig,
		captureTarget:        schedulerConfig.CaptureTarget,
		confidence:           schedulerConfig.Confidence,
		minimumAudit:         schedulerConfig.MinimumAudit,
		recoveryDuration:     schedulerConfig.RecoveryDuration,
		refreshInterval:      schedulerConfig.RefreshInterval,
		refreshDuration:      schedulerConfig.RefreshDuration,
		promotionMargin:      schedulerConfig.PromotionMargin,
		promotionStability:   schedulerConfig.PromotionStability,
		watchdogHistory:      schedulerConfig.WatchdogHistory,
		watchdogMinIntervals: schedulerConfig.WatchdogMinIntervals,
		watchdogWindow:       schedulerConfig.WatchdogWindow,
		watchdogQuantile:     schedulerConfig.WatchdogQuantile,
		watchdogMargin:       schedulerConfig.WatchdogMargin,
		senderIDs:            make(map[uint64]bool, len(ids)),
		sampleRate:           int64(cfg.SampleRate),
		blockSamples:         int64(cfg.BlockSize),
		blockBytes:           cfg.BlockSize2,
		collar:               make([]dutyCollarBlock, collarBlocks),
		warmupBlocks:         warmupBlocks,
		startedUTC:           time.Now().UTC(),
		resume:               dutyResumeInfo{Status: "FRESH"},
	}
	for idx := range runtime.collar {
		runtime.collar[idx].data = make([]byte, runtime.blockBytes)
	}
	for _, id := range ids {
		runtime.senderIDs[id] = true
	}
	return runtime, nil
}

func (d *dutyRuntime) configureCheckpoints(path string, interval time.Duration) error {
	if path == "" || interval <= 0 {
		return fmt.Errorf("dutyscheduler: invalid checkpoint configuration")
	}
	canonical, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("dutyscheduler: checkpoint path is not a directory")
	}
	d.checkpointDir = canonical
	d.checkpointInterval = interval
	d.nextCheckpointUTC = time.Time{}
	return d.restoreBestCheckpoint()
}

func (d *dutyRuntime) beginBlock() (time.Duration, time.Duration, dutyscheduler.Decision) {
	start := d.sampleTime
	numerator := d.blockSamples*int64(time.Second) + d.sampleRemainder
	delta := numerator / d.sampleRate
	d.sampleRemainder = numerator % d.sampleRate
	end := start + time.Duration(delta)
	d.sampleTime = end
	return start, end, d.scheduler.Advance(start, end)
}

func (d *dutyRuntime) needsRebuild(decision dutyscheduler.Decision) bool {
	return d.mode == dutyscheduler.ModeGated && d.wasSkipped && decision.Decode
}

func (d *dutyRuntime) finishBlock(block []byte, start, end time.Duration, wallTime time.Time, decision dutyscheduler.Decision) {
	entry := &d.collar[d.collarNext]
	// A decoded block can never be replayed: a short wake replays only the
	// immediately preceding skipped run, while a long skipped run replaces the
	// complete collar before the full decoder rebuild. Retaining decoded raw IQ
	// therefore spends one block copy without supplying recovery data.
	entry.rawValid = !decision.Decode
	if entry.rawValid {
		if d.borrowRaw {
			entry.data = block
		} else {
			copy(entry.data, block)
		}
	} else if d.borrowRaw {
		entry.data = nil
	}
	entry.sampleStart = start
	entry.sampleEnd = end
	entry.wallTime = wallTime
	entry.decoded = decision.Decode
	d.collarNext++
	if d.collarNext == len(d.collar) {
		d.collarNext = 0
	}
	if d.collarCount < len(d.collar) {
		d.collarCount++
	}
	if decision.Decode {
		d.decodedBlocks++
		d.skippedRun = 0
	} else {
		d.skippedBlocks++
		d.skippedRun++
	}
	if decision.Audit {
		d.auditedBlocks++
	}
	if decision.Refresh {
		d.refreshBlocks++
	}
	d.wasSkipped = !decision.Decode
}

func (d *dutyRuntime) orderedCollar() []*dutyCollarBlock {
	result := make([]*dutyCollarBlock, 0, d.collarCount)
	start := d.collarNext - d.collarCount
	if start < 0 {
		start += len(d.collar)
	}
	for offset := 0; offset < d.collarCount; offset++ {
		idx := start + offset
		if idx >= len(d.collar) {
			idx -= len(d.collar)
		}
		result = append(result, &d.collar[idx])
	}
	return result
}

func (d *dutyRuntime) observe(id uint64, protocolName string, at time.Duration) {
	if d.senderIDs[id] {
		d.scheduler.Observe(id, protocolName, at)
	}
}

func (d *dutyRuntime) observeEscape(id uint64, protocolName string, at time.Duration) {
	if d.senderIDs[id] {
		d.scheduler.ObserveEscape(id, protocolName, at)
	}
}

func (d *dutyRuntime) recordRebuild(replayed int) {
	d.rebuilds++
	d.replayedBlocks += uint64(replayed)
}

func (d *dutyRuntime) trustETA(snapshot dutyscheduler.Snapshot, now time.Time) dutyTrustETA {
	result := dutyTrustETA{
		Status:     "LEARNING",
		Assumption: "conditional estimate if no additional scheduler would-misses occur; qualification uses actual observations",
	}
	var bestSeconds float64
	bestSet := false
	for _, candidate := range snapshot.Candidates {
		candidateETA := dutyCandidateETA{Name: candidate.Name, Status: "LEARNING"}
		allRatesKnown := candidate.Eligible && candidate.EligibleDurationNS > 0
		candidateSeconds := float64(0)
		for id, sender := range candidate.Senders {
			rate := float64(0)
			if candidate.EligibleDurationNS > 0 {
				rate = float64(sender.EligibleEvents) * float64(time.Hour) / float64(candidate.EligibleDurationNS)
			}
			senderETA := dutySenderETA{
				ID:                      id,
				Protocol:                sender.Protocol,
				EligibleEvents:          sender.EligibleEvents,
				WouldMiss:               sender.WouldMiss,
				RequiredEligibleEvents:  sender.RequiredEligibleEvents,
				RemainingEligibleEvents: sender.RemainingEligibleEvents,
				EligibleEventsPerHour:   rate,
			}
			if sender.RemainingEligibleEvents == 0 {
				zero := float64(0)
				senderETA.EstimatedRemainingSeconds = &zero
			} else if rate > 0 {
				seconds := float64(sender.RemainingEligibleEvents) / rate * float64(time.Hour/time.Second)
				senderETA.EstimatedRemainingSeconds = &seconds
				if seconds > candidateSeconds {
					candidateSeconds = seconds
					candidateETA.LimitingSenderID = id
				}
			} else {
				allRatesKnown = false
			}
			candidateETA.Senders = append(candidateETA.Senders, senderETA)
		}
		sort.Slice(candidateETA.Senders, func(i, j int) bool { return candidateETA.Senders[i].ID < candidateETA.Senders[j].ID })
		if candidate.Invalid {
			candidateETA.Status = "INVALID"
		} else if candidate.Qualified {
			candidateETA.Status = "QUALIFIED"
			zero := float64(0)
			candidateETA.EstimatedRemainingSeconds = &zero
			if result.Status != "QUALIFIED" {
				result.Status = "QUALIFIED"
				result.Candidate = candidate.Name
				result.EstimatedRemainingSeconds = &zero
				result.EstimatedReadyUTC = now.UTC().Format(time.RFC3339Nano)
			}
		} else if candidate.ContractQualified {
			candidateETA.Status = "CONTRACT_QUALIFIED"
			zero := float64(0)
			candidateETA.EstimatedRemainingSeconds = &zero
			if result.Status != "QUALIFIED" && result.Status != "CONTRACT_QUALIFIED" {
				result.Status = "CONTRACT_QUALIFIED"
				result.Candidate = candidate.Name
				result.EstimatedRemainingSeconds = &zero
				result.EstimatedReadyUTC = now.UTC().Format(time.RFC3339Nano)
			}
		} else if allRatesKnown && !math.IsInf(candidateSeconds, 0) && !math.IsNaN(candidateSeconds) {
			candidateETA.Status = "ESTIMATING"
			candidateETA.EstimatedRemainingSeconds = &candidateSeconds
			if result.Status != "QUALIFIED" && result.Status != "CONTRACT_QUALIFIED" && (!bestSet || candidateSeconds < bestSeconds) {
				bestSet = true
				bestSeconds = candidateSeconds
				result.Status = "ESTIMATING"
				result.Candidate = candidate.Name
				result.LimitingSenderID = candidateETA.LimitingSenderID
				seconds := candidateSeconds
				result.EstimatedRemainingSeconds = &seconds
				result.EstimatedReadyUTC = now.Add(time.Duration(seconds * float64(time.Second))).UTC().Format(time.RFC3339Nano)
			}
		}
		result.Candidates = append(result.Candidates, candidateETA)
	}
	return result
}

func (d *dutyRuntime) makeReport(now time.Time, kind string) (dutyRuntimeReport, error) {
	ids := make([]uint64, 0, len(d.senderIDs))
	for id := range d.senderIDs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	snapshot := d.scheduler.Snapshot()
	state, err := d.scheduler.ExportState()
	if err != nil {
		return dutyRuntimeReport{}, err
	}
	return dutyRuntimeReport{
		Schema:               "rtlamr-duty-scheduler-live-v4",
		CreatedUTC:           now.UTC().Format(time.RFC3339Nano),
		SessionStartedUTC:    d.startedUTC.Format(time.RFC3339Nano),
		ReportKind:           kind,
		CheckpointSequence:   d.checkpointSequence,
		CheckpointFailures:   d.checkpointFailures,
		Mode:                 d.mode,
		CaptureTarget:        d.captureTarget,
		Confidence:           d.confidence,
		MinimumAudit:         d.minimumAudit,
		RecoveryDurationNS:   int64(d.recoveryDuration),
		RefreshIntervalNS:    int64(d.refreshInterval),
		RefreshDurationNS:    int64(d.refreshDuration),
		PromotionMargin:      d.promotionMargin,
		PromotionStabilityNS: int64(d.promotionStability),
		WatchdogHistory:      d.watchdogHistory,
		WatchdogMinIntervals: d.watchdogMinIntervals,
		WatchdogWindowNS:     int64(d.watchdogWindow),
		WatchdogQuantile:     d.watchdogQuantile,
		WatchdogMargin:       d.watchdogMargin,
		SenderIDs:            ids,
		SampleRate:           d.sampleRate,
		BlockSamples:         d.blockSamples,
		BlockBytes:           d.blockBytes,
		CollarBlocks:         len(d.collar),
		WarmupBlocks:         d.warmupBlocks,
		SampleRemainder:      d.sampleRemainder,
		DecodedBlocks:        d.decodedBlocks,
		SkippedBlocks:        d.skippedBlocks,
		AuditedBlocks:        d.auditedBlocks,
		RefreshBlocks:        d.refreshBlocks,
		Rebuilds:             d.rebuilds,
		ReplayedBlocks:       d.replayedBlocks,
		TrustETA:             d.trustETA(snapshot, now),
		Snapshot:             snapshot,
		Resume:               d.resume,
		SchedulerState:       state,
	}, nil

}

func marshalDutyReport(report dutyRuntimeReport) ([]byte, error) {
	contents, err := json.Marshal(report)
	if err != nil {
		return nil, err
	}
	return append(contents, '\n'), nil
}

func writeExclusiveSynced(path string, contents []byte) error {
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, encodeErr := output.Write(contents)
	syncErr := output.Sync()
	closeErr := output.Close()
	if encodeErr != nil {
		return encodeErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func (d *dutyRuntime) writeCheckpoint(now time.Time, kind string) error {
	d.checkpointSequence++
	report, err := d.makeReport(now, kind)
	if err != nil {
		return err
	}
	contents, err := marshalDutyReport(report)
	if err != nil {
		return err
	}
	session := d.startedUTC.Format("20060102T150405.000000000Z")
	immutable := filepath.Join(d.checkpointDir, fmt.Sprintf("checkpoint-%s-%06d.json", session, d.checkpointSequence))
	if err := writeExclusiveSynced(immutable, contents); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(d.checkpointDir, ".latest-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, filepath.Join(d.checkpointDir, "latest.json")); err != nil {
		return err
	}
	removeTemporary = false
	return syncDirectory(d.checkpointDir)
}

func (d *dutyRuntime) maybeCheckpoint(now time.Time) error {
	if d.checkpointDir == "" {
		return nil
	}
	if !d.nextCheckpointUTC.IsZero() && now.Before(d.nextCheckpointUTC) {
		return nil
	}
	kind := "hourly"
	if d.resume.PendingReceipt {
		kind = "resume"
	} else if d.checkpointSequence == 0 {
		kind = "startup"
	}
	if err := d.writeCheckpoint(now, kind); err != nil {
		d.checkpointFailures++
		d.nextCheckpointUTC = now.Add(5 * time.Minute)
		return err
	}
	d.resume.PendingReceipt = false
	d.nextCheckpointUTC = now.Add(d.checkpointInterval)
	return nil
}

func (d *dutyRuntime) writeFinalCheckpoint(now time.Time) error {
	if d.checkpointDir == "" {
		return nil
	}
	return d.writeCheckpoint(now, "final")
}

func (d *dutyRuntime) writeReport(path string) error {
	now := time.Now().UTC()
	report, err := d.makeReport(now, "final")
	if err != nil {
		return err
	}
	contents, err := marshalDutyReport(report)
	if err != nil {
		return err
	}
	if err := writeAtomicSynced(path, contents); err != nil {
		return err
	}
	return nil
}

func writeAtomicSynced(path string, contents []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".report-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	removeTemporary = false
	return syncDirectory(directory)
}
