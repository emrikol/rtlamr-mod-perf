package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bemasher/rtlamr/internal/dutyscheduler"
)

const dutyCheckpointMaxBytes = 4 << 20

type dutyCheckpointCandidate struct {
	path            string
	sha256          string
	report          dutyRuntimeReport
	scheduler       *dutyscheduler.Scheduler
	sampleRemainder int64
	evidence        uint64
	exact           bool
	policyMigration bool
	modeMigration   bool
}

func (d *dutyRuntime) restoreBestCheckpoint() error {
	entries, err := os.ReadDir(d.checkpointDir)
	if err != nil {
		return err
	}
	bySession := make(map[string]dutyCheckpointCandidate)
	var skipped uint64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "checkpoint-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		candidate, err := d.readCheckpoint(filepath.Join(d.checkpointDir, entry.Name()))
		if err != nil {
			skipped++
			continue
		}
		session := candidate.report.SessionStartedUTC
		previous, exists := bySession[session]
		if !exists || candidate.report.CheckpointSequence > previous.report.CheckpointSequence {
			bySession[session] = candidate
		}
	}
	if len(bySession) == 0 {
		if skipped > 0 {
			d.resume.Status = "FRESH_CHECKPOINTS_INVALID"
		}
		d.resume.SkippedCheckpoints = skipped
		return nil
	}
	candidates := make([]dutyCheckpointCandidate, 0, len(bySession))
	for _, candidate := range bySession {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if d.mode == dutyscheduler.ModeGated {
			leftGated := candidates[i].report.Mode == dutyscheduler.ModeGated
			rightGated := candidates[j].report.Mode == dutyscheduler.ModeGated
			if leftGated != rightGated {
				return leftGated
			}
		}
		if candidates[i].evidence != candidates[j].evidence {
			return candidates[i].evidence > candidates[j].evidence
		}
		leftBlocks := candidates[i].report.DecodedBlocks + candidates[i].report.SkippedBlocks
		rightBlocks := candidates[j].report.DecodedBlocks + candidates[j].report.SkippedBlocks
		if leftBlocks != rightBlocks {
			return leftBlocks > rightBlocks
		}
		return candidates[i].report.CreatedUTC > candidates[j].report.CreatedUTC
	})
	chosen := candidates[0]
	started, err := time.Parse(time.RFC3339Nano, chosen.report.SessionStartedUTC)
	if err != nil {
		return err
	}
	d.scheduler = chosen.scheduler
	d.sampleTime = time.Duration(chosen.report.Snapshot.SampleTimeNS)
	d.sampleRemainder = chosen.sampleRemainder
	d.decodedBlocks = chosen.report.DecodedBlocks
	d.skippedBlocks = chosen.report.SkippedBlocks
	d.auditedBlocks = chosen.report.AuditedBlocks
	d.refreshBlocks = chosen.report.RefreshBlocks
	d.rebuilds = chosen.report.Rebuilds
	d.replayedBlocks = chosen.report.ReplayedBlocks
	d.startedUTC = started.UTC()
	d.checkpointSequence = chosen.report.CheckpointSequence
	d.checkpointFailures = chosen.report.CheckpointFailures
	status := "RESTORED_LEGACY"
	if chosen.exact {
		status = "RESTORED_EXACT"
	} else if chosen.modeMigration {
		status = "RESTORED_SHADOW_TO_GATED"
	} else if chosen.policyMigration {
		status = "RESTORED_POLICY_MIGRATION"
	}
	d.resume = dutyResumeInfo{
		Status:             status,
		SourceFile:         filepath.Base(chosen.path),
		SourceSHA256:       chosen.sha256,
		SourceSchema:       chosen.report.Schema,
		SourceCreatedUTC:   chosen.report.CreatedUTC,
		SourceSequence:     chosen.report.CheckpointSequence,
		SkippedCheckpoints: skipped,
		PendingReceipt:     true,
	}
	return nil
}

func (d *dutyRuntime) readCheckpoint(path string) (dutyCheckpointCandidate, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return dutyCheckpointCandidate{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > dutyCheckpointMaxBytes {
		return dutyCheckpointCandidate{}, errors.New("dutyscheduler: invalid checkpoint file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return dutyCheckpointCandidate{}, err
	}
	var report dutyRuntimeReport
	if err := json.Unmarshal(contents, &report); err != nil {
		return dutyCheckpointCandidate{}, err
	}
	if err := d.validateCheckpointReport(report); err != nil {
		return dutyCheckpointCandidate{}, err
	}
	schedulerConfig := d.schedulerConfig
	scheduler, err := dutyscheduler.New(schedulerConfig)
	if err != nil {
		return dutyCheckpointCandidate{}, err
	}
	exact := false
	policyMigration := false
	modeMigration := false
	if report.Schema == "rtlamr-duty-scheduler-live-v3" || report.Schema == "rtlamr-duty-scheduler-live-v4" {
		err := scheduler.RestoreState(report.SchedulerState)
		switch {
		case err == nil:
			exact = true
		case report.Mode == dutyscheduler.ModeShadow && d.mode == dutyscheduler.ModeGated:
			if promotionErr := scheduler.RestoreShadowState(report.SchedulerState); promotionErr == nil {
				modeMigration = true
				break
			}
			if err := scheduler.RestoreLegacySnapshot(report.Snapshot); err != nil {
				return dutyCheckpointCandidate{}, err
			}
			modeMigration = true
		case errors.Is(err, dutyscheduler.ErrStateConfigurationMismatch), errors.Is(err, dutyscheduler.ErrStateSchemaMismatch):
			if err := scheduler.RestoreLegacySnapshot(report.Snapshot); err != nil {
				return dutyCheckpointCandidate{}, err
			}
			policyMigration = true
		default:
			return dutyCheckpointCandidate{}, err
		}
	} else if err := scheduler.RestoreLegacySnapshot(report.Snapshot); err != nil {
		return dutyCheckpointCandidate{}, err
	}
	if !dutyCheckpointEvidenceMatches(scheduler.Snapshot(), report.Snapshot) {
		return dutyCheckpointCandidate{}, errors.New("dutyscheduler: checkpoint state/evidence mismatch")
	}
	blocks := report.DecodedBlocks + report.SkippedBlocks
	expectedTime, remainder, err := dutySampleClock(blocks, d.blockSamples, d.sampleRate)
	if err != nil || int64(expectedTime) != report.Snapshot.SampleTimeNS {
		return dutyCheckpointCandidate{}, errors.New("dutyscheduler: checkpoint sample clock mismatch")
	}
	if exact && report.SampleRemainder != remainder {
		return dutyCheckpointCandidate{}, errors.New("dutyscheduler: checkpoint sample remainder mismatch")
	}
	sum := sha256.Sum256(contents)
	return dutyCheckpointCandidate{
		path:            path,
		sha256:          hex.EncodeToString(sum[:]),
		report:          report,
		scheduler:       scheduler,
		sampleRemainder: remainder,
		evidence:        dutyEvidenceCount(report.Snapshot),
		exact:           exact,
		policyMigration: policyMigration,
		modeMigration:   modeMigration,
	}, nil
}

func (d *dutyRuntime) validateCheckpointReport(report dutyRuntimeReport) error {
	if report.Schema != "rtlamr-duty-scheduler-live-v2" && report.Schema != "rtlamr-duty-scheduler-live-v3" && report.Schema != "rtlamr-duty-scheduler-live-v4" {
		return errors.New("dutyscheduler: unsupported checkpoint report")
	}
	compatibleMode := report.Mode == d.mode || (report.Mode == dutyscheduler.ModeShadow && d.mode == dutyscheduler.ModeGated)
	if !compatibleMode || report.Snapshot.Mode != report.Mode || report.SampleRate != d.sampleRate || report.BlockSamples != d.blockSamples || report.BlockBytes != d.blockBytes || report.CollarBlocks != len(d.collar) || report.WarmupBlocks != d.warmupBlocks {
		return errors.New("dutyscheduler: checkpoint runtime mismatch")
	}
	created, err := time.Parse(time.RFC3339Nano, report.CreatedUTC)
	if err != nil {
		return errors.New("dutyscheduler: invalid checkpoint creation time")
	}
	started, err := time.Parse(time.RFC3339Nano, report.SessionStartedUTC)
	if err != nil || created.Before(started) {
		return errors.New("dutyscheduler: invalid checkpoint session time")
	}
	switch report.ReportKind {
	case "startup", "hourly", "final", "resume":
	default:
		return errors.New("dutyscheduler: invalid checkpoint report kind")
	}
	if report.CheckpointSequence == 0 {
		return errors.New("dutyscheduler: invalid checkpoint sequence")
	}
	wantIDs := make([]uint64, 0, len(d.senderIDs))
	for id := range d.senderIDs {
		wantIDs = append(wantIDs, id)
	}
	sort.Slice(wantIDs, func(i, j int) bool { return wantIDs[i] < wantIDs[j] })
	if len(report.SenderIDs) != len(wantIDs) {
		return errors.New("dutyscheduler: checkpoint sender inventory mismatch")
	}
	for idx := range wantIDs {
		if report.SenderIDs[idx] != wantIDs[idx] {
			return errors.New("dutyscheduler: checkpoint sender inventory mismatch")
		}
	}
	if report.DecodedBlocks > math.MaxUint64-report.SkippedBlocks || report.Snapshot.SampleTimeNS < 0 {
		return errors.New("dutyscheduler: invalid checkpoint work")
	}
	if (report.Schema == "rtlamr-duty-scheduler-live-v3" || report.Schema == "rtlamr-duty-scheduler-live-v4") && report.SchedulerState.LastEndNS != report.Snapshot.SampleTimeNS {
		return errors.New("dutyscheduler: checkpoint state clock mismatch")
	}
	return nil
}

// dutyEvidenceCount intentionally ignores misses and confidence results. It
// selects the checkpoint with the largest retained observation inventory,
// avoiding outcome-based checkpoint selection.
func dutyEvidenceCount(snapshot dutyscheduler.Snapshot) uint64 {
	var best uint64
	for _, candidate := range snapshot.Candidates {
		if len(candidate.Senders) == 0 {
			continue
		}
		minimum := ^uint64(0)
		for _, sender := range candidate.Senders {
			if sender.EligibleEvents < minimum {
				minimum = sender.EligibleEvents
			}
		}
		if minimum != ^uint64(0) && minimum > best {
			best = minimum
		}
	}
	return best
}

func dutyCheckpointEvidenceMatches(restored, source dutyscheduler.Snapshot) bool {
	compatibleMode := restored.Mode == source.Mode || (source.Mode == dutyscheduler.ModeShadow && restored.Mode == dutyscheduler.ModeGated)
	if !compatibleMode || restored.SampleTimeNS != source.SampleTimeNS || restored.DeadlineDeficits != source.DeadlineDeficits || restored.CountDeficits != source.CountDeficits || len(restored.ObservedSenders) != len(source.ObservedSenders) || len(restored.Candidates) != len(source.Candidates) {
		return false
	}
	for id, want := range source.ObservedSenders {
		got, ok := restored.ObservedSenders[id]
		if !ok || got.Observations != want.Observations || got.DeadlineDeficits != want.DeadlineDeficits || got.CountDeficits != want.CountDeficits {
			return false
		}
	}
	for idx, want := range source.Candidates {
		got := restored.Candidates[idx]
		if got.Name != want.Name || got.HistoryLimit != want.HistoryLimit || got.Invalid != want.Invalid || got.TotalDurationNS != want.TotalDurationNS || got.TotalAwakeDurationNS != want.TotalAwakeDurationNS || got.EligibleDurationNS != want.EligibleDurationNS || got.AwakeDurationNS != want.AwakeDurationNS || len(got.Senders) != len(want.Senders) {
			return false
		}
		for id, wantSender := range want.Senders {
			gotSender, ok := got.Senders[id]
			if !ok || gotSender.EligibleEvents != wantSender.EligibleEvents || gotSender.WouldMiss != wantSender.WouldMiss {
				return false
			}
		}
	}
	return true
}

func dutySampleClock(blocks uint64, blockSamples, sampleRate int64) (time.Duration, int64, error) {
	if blockSamples <= 0 || sampleRate <= 0 || blockSamples > math.MaxInt64/int64(time.Second) {
		return 0, 0, errors.New("dutyscheduler: invalid sample geometry")
	}
	numerator := blockSamples * int64(time.Second)
	whole := numerator / sampleRate
	remainderPerBlock := numerator % sampleRate
	if blocks > math.MaxInt64 || whole < 0 || (whole > 0 && blocks > uint64(math.MaxInt64/whole)) || (remainderPerBlock > 0 && blocks > uint64(math.MaxInt64/remainderPerBlock)) {
		return 0, 0, errors.New("dutyscheduler: sample clock overflow")
	}
	remainderProduct := int64(blocks) * remainderPerBlock
	nanoseconds := int64(blocks)*whole + remainderProduct/sampleRate
	if nanoseconds < 0 {
		return 0, 0, fmt.Errorf("dutyscheduler: sample clock overflow")
	}
	return time.Duration(nanoseconds), remainderProduct % sampleRate, nil
}
