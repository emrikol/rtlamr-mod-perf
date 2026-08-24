package dutyscheduler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

const StateSchema = "rtlamr-duty-scheduler-state-v1"

var ErrStateConfigurationMismatch = errors.New("dutyscheduler: state configuration mismatch")

// State is the complete scheduler state needed to continue one evidence
// stream across a process restart. Runtime-owned sample and block counters are
// stored by the caller beside this value.
type State struct {
	Schema           string           `json:"schema"`
	ConfigSHA256     string           `json:"config_sha256"`
	LastEndNS        int64            `json:"last_end_ns"`
	Started          bool             `json:"started"`
	RecoveryUntilNS  int64            `json:"recovery_until_ns"`
	DeadlineDeficits uint64           `json:"deadline_deficits"`
	CountDeficits    uint64           `json:"count_deficits"`
	Discontinuities  uint64           `json:"discontinuities"`
	RNG              uint64           `json:"rng"`
	Senders          []SenderState    `json:"senders"`
	Candidates       []CandidateState `json:"candidates"`
}

type SenderState struct {
	ID               uint64  `json:"id"`
	Protocol         string  `json:"protocol"`
	LastSeenNS       int64   `json:"last_seen_ns"`
	Seen             bool    `json:"seen"`
	ObservationsNS   []int64 `json:"observations_ns"`
	Total            uint64  `json:"total"`
	OverdueOpen      bool    `json:"overdue_open"`
	CountOpen        bool    `json:"count_open"`
	DeadlineDeficits uint64  `json:"deadline_deficits"`
	CountDeficits    uint64  `json:"count_deficits"`
}

type CandidateState struct {
	Name               string             `json:"name"`
	LastEligible       bool               `json:"last_eligible"`
	LastAwake          bool               `json:"last_awake"`
	TotalDurationNS    int64              `json:"total_duration_ns"`
	TotalAwakeNS       int64              `json:"total_awake_ns"`
	EligibleDurationNS int64              `json:"eligible_duration_ns"`
	AwakeDurationNS    int64              `json:"awake_duration_ns"`
	Invalid            bool               `json:"invalid"`
	Senders            []SenderModelState `json:"senders"`
}

type SenderModelState struct {
	ID         uint64  `json:"id"`
	Protocol   string  `json:"protocol"`
	HistoryNS  []int64 `json:"history_ns"`
	LastNS     int64   `json:"last_ns"`
	Seen       bool    `json:"seen"`
	Learned    bool    `json:"learned"`
	PeriodNS   int64   `json:"period_ns"`
	AnchorNS   int64   `json:"anchor_ns"`
	PreWakeNS  int64   `json:"pre_wake_ns"`
	PostWakeNS int64   `json:"post_wake_ns"`
	Events     uint64  `json:"events"`
	Misses     uint64  `json:"misses"`
}

func configSHA256(cfg Config) (string, error) {
	contents, err := json.Marshal(cfg)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:]), nil
}

// ExportState returns a deep copy with deterministic sender ordering.
func (s *Scheduler) ExportState() (State, error) {
	fingerprint, err := configSHA256(s.cfg)
	if err != nil {
		return State{}, err
	}
	result := State{
		Schema:           StateSchema,
		ConfigSHA256:     fingerprint,
		LastEndNS:        int64(s.lastEnd),
		Started:          s.started,
		RecoveryUntilNS:  int64(s.recoveryUntil),
		DeadlineDeficits: s.deadlineDeficits,
		CountDeficits:    s.countDeficits,
		Discontinuities:  s.discontinuities,
		RNG:              s.rng,
	}
	ids := sortedSenderIDs(s.senders)
	for _, id := range ids {
		sender := s.senders[id]
		state := SenderState{
			ID:               id,
			Protocol:         sender.protocol,
			LastSeenNS:       int64(sender.lastSeen),
			Seen:             sender.seen,
			Total:            sender.total,
			OverdueOpen:      sender.overdueOpen,
			CountOpen:        sender.countOpen,
			DeadlineDeficits: sender.deadlineDeficits,
			CountDeficits:    sender.countDeficits,
		}
		for _, observed := range sender.observations {
			state.ObservationsNS = append(state.ObservationsNS, int64(observed))
		}
		result.Senders = append(result.Senders, state)
	}
	for _, candidate := range s.candidates {
		state := CandidateState{
			Name:               candidate.cfg.Name,
			LastEligible:       candidate.lastEligible,
			LastAwake:          candidate.lastAwake,
			TotalDurationNS:    int64(candidate.totalDuration),
			TotalAwakeNS:       int64(candidate.totalAwake),
			EligibleDurationNS: int64(candidate.eligibleDuration),
			AwakeDurationNS:    int64(candidate.awakeDuration),
			Invalid:            candidate.invalid,
		}
		for _, id := range ids {
			model := candidate.senders[id]
			modelState := SenderModelState{
				ID:         id,
				Protocol:   model.protocol,
				LastNS:     int64(model.last),
				Seen:       model.seen,
				Learned:    model.learned,
				PeriodNS:   int64(model.period),
				AnchorNS:   int64(model.anchor),
				PreWakeNS:  int64(model.preWake),
				PostWakeNS: int64(model.postWake),
				Events:     model.events,
				Misses:     model.misses,
			}
			for _, interval := range model.history {
				modelState.HistoryNS = append(modelState.HistoryNS, int64(interval))
			}
			state.Senders = append(state.Senders, modelState)
		}
		result.Candidates = append(result.Candidates, state)
	}
	return result, nil
}

// RestoreState validates and restores an exact state checkpoint. It mutates
// the receiver only after every field has passed validation. A gated restore
// deliberately enters recovery so process restart can never begin by
// suppressing DSP.
func (s *Scheduler) RestoreState(state State) error {
	if state.Schema != StateSchema {
		return fmt.Errorf("dutyscheduler: unsupported state schema %q", state.Schema)
	}
	fingerprint, err := configSHA256(s.cfg)
	if err != nil {
		return err
	}
	if state.ConfigSHA256 != fingerprint {
		return ErrStateConfigurationMismatch
	}
	restored, err := New(s.cfg)
	if err != nil {
		return err
	}
	if err := restored.applyState(state); err != nil {
		return err
	}
	*s = *restored
	return nil
}

func (s *Scheduler) applyState(state State) error {
	if state.LastEndNS < 0 || state.RecoveryUntilNS < 0 || (!state.Started && state.LastEndNS != 0) || state.RNG == 0 {
		return errors.New("dutyscheduler: invalid state clock")
	}
	if len(state.Senders) != len(s.senders) || len(state.Candidates) != len(s.candidates) {
		return errors.New("dutyscheduler: state inventory mismatch")
	}
	seenSenders := make(map[uint64]bool, len(state.Senders))
	for _, senderState := range state.Senders {
		sender, ok := s.senders[senderState.ID]
		if !ok || seenSenders[senderState.ID] {
			return errors.New("dutyscheduler: invalid state sender inventory")
		}
		seenSenders[senderState.ID] = true
		if senderState.LastSeenNS < 0 || (senderState.Seen && senderState.LastSeenNS > state.LastEndNS) || (!senderState.Seen && (senderState.LastSeenNS != 0 || senderState.Protocol != "")) {
			return errors.New("dutyscheduler: invalid sender clock")
		}
		if senderState.Total < uint64(len(senderState.ObservationsNS)) {
			return errors.New("dutyscheduler: invalid sender observation total")
		}
		previous := int64(-1)
		for _, observed := range senderState.ObservationsNS {
			if observed < 0 || observed <= previous || observed > state.LastEndNS {
				return errors.New("dutyscheduler: invalid sender observations")
			}
			previous = observed
			sender.observations = append(sender.observations, time.Duration(observed))
		}
		sender.protocol = senderState.Protocol
		sender.lastSeen = time.Duration(senderState.LastSeenNS)
		sender.seen = senderState.Seen
		sender.total = senderState.Total
		sender.overdueOpen = senderState.OverdueOpen
		sender.countOpen = senderState.CountOpen
		sender.deadlineDeficits = senderState.DeadlineDeficits
		sender.countDeficits = senderState.CountDeficits
	}
	for idx, candidateState := range state.Candidates {
		candidate := s.candidates[idx]
		if candidateState.Name != candidate.cfg.Name || len(candidateState.Senders) != len(candidate.senders) {
			return errors.New("dutyscheduler: invalid state candidate inventory")
		}
		if !validDurations(candidateState.TotalDurationNS, candidateState.TotalAwakeNS, candidateState.EligibleDurationNS, candidateState.AwakeDurationNS) {
			return errors.New("dutyscheduler: invalid candidate durations")
		}
		candidate.lastEligible = candidateState.LastEligible
		candidate.lastAwake = candidateState.LastAwake
		candidate.totalDuration = time.Duration(candidateState.TotalDurationNS)
		candidate.totalAwake = time.Duration(candidateState.TotalAwakeNS)
		candidate.eligibleDuration = time.Duration(candidateState.EligibleDurationNS)
		candidate.awakeDuration = time.Duration(candidateState.AwakeDurationNS)
		candidate.invalid = candidateState.Invalid
		seenModels := make(map[uint64]bool, len(candidateState.Senders))
		for _, modelState := range candidateState.Senders {
			model, ok := candidate.senders[modelState.ID]
			if !ok || seenModels[modelState.ID] {
				return errors.New("dutyscheduler: invalid model sender inventory")
			}
			seenModels[modelState.ID] = true
			if err := validateModelState(modelState, candidate.cfg, state.LastEndNS); err != nil {
				return err
			}
			model.protocol = modelState.Protocol
			model.last = time.Duration(modelState.LastNS)
			model.seen = modelState.Seen
			model.learned = modelState.Learned
			model.period = time.Duration(modelState.PeriodNS)
			model.anchor = time.Duration(modelState.AnchorNS)
			model.preWake = time.Duration(modelState.PreWakeNS)
			model.postWake = time.Duration(modelState.PostWakeNS)
			model.events = modelState.Events
			model.misses = modelState.Misses
			for _, interval := range modelState.HistoryNS {
				model.history = append(model.history, time.Duration(interval))
			}
		}
	}
	s.lastEnd = time.Duration(state.LastEndNS)
	s.started = state.Started
	s.recoveryUntil = time.Duration(state.RecoveryUntilNS)
	s.deadlineDeficits = state.DeadlineDeficits
	s.countDeficits = state.CountDeficits
	s.discontinuities = state.Discontinuities
	s.rng = state.RNG
	s.selected = nil
	s.quietActive = false
	s.auditActive = false
	s.quietCandidate = ""
	if s.cfg.Mode == ModeGated {
		s.enterRecovery(s.lastEnd)
	}
	s.refreshQualifications()
	s.chooseCandidate()
	return nil
}

func validDurations(total, awake, eligible, eligibleAwake int64) bool {
	return total >= 0 && awake >= 0 && eligible >= 0 && eligibleAwake >= 0 && awake <= total && eligible <= total && eligibleAwake <= eligible
}

func validateModelState(state SenderModelState, cfg CandidateConfig, lastEnd int64) error {
	if state.LastNS < 0 || state.PeriodNS < 0 || state.AnchorNS < 0 || state.PreWakeNS < 0 || state.PostWakeNS < 0 || state.Misses > state.Events {
		return errors.New("dutyscheduler: invalid sender model")
	}
	if len(state.HistoryNS) > cfg.HistoryLimit || (!state.Seen && (state.LastNS != 0 || state.Protocol != "" || state.Learned)) || state.LastNS > lastEnd {
		return errors.New("dutyscheduler: invalid sender model history")
	}
	for _, interval := range state.HistoryNS {
		if interval <= 0 {
			return errors.New("dutyscheduler: invalid sender model interval")
		}
	}
	if state.Learned && (!state.Seen || state.PeriodNS <= 0 || len(state.HistoryNS) < cfg.WarmupIntervals) {
		return errors.New("dutyscheduler: invalid learned sender model")
	}
	return nil
}

// RestoreLegacySnapshot conservatively imports evidence from a report that
// predates complete state checkpoints. Evidence and cumulative durations are
// retained, while cadence histories and watchdog windows are reset. New events
// are not scored until each candidate has relearned its configured warmup.
func (s *Scheduler) RestoreLegacySnapshot(snapshot Snapshot) error {
	if snapshot.Mode != s.cfg.Mode || snapshot.SampleTimeNS < 0 || len(snapshot.ObservedSenders) != len(s.senders) || len(snapshot.Candidates) != len(s.candidates) {
		return errors.New("dutyscheduler: incompatible legacy snapshot")
	}
	restored, err := New(s.cfg)
	if err != nil {
		return err
	}
	for id, old := range snapshot.ObservedSenders {
		sender, ok := restored.senders[id]
		if !ok {
			return errors.New("dutyscheduler: legacy sender inventory mismatch")
		}
		sender.total = old.Observations
		sender.deadlineDeficits = old.DeadlineDeficits
		sender.countDeficits = old.CountDeficits
	}
	for idx, oldCandidate := range snapshot.Candidates {
		candidate := restored.candidates[idx]
		if oldCandidate.Name != candidate.cfg.Name || oldCandidate.HistoryLimit != candidate.cfg.HistoryLimit || len(oldCandidate.Senders) != len(candidate.senders) {
			return errors.New("dutyscheduler: legacy candidate inventory mismatch")
		}
		if !validDurations(oldCandidate.TotalDurationNS, oldCandidate.TotalAwakeDurationNS, oldCandidate.EligibleDurationNS, oldCandidate.AwakeDurationNS) {
			return errors.New("dutyscheduler: invalid legacy candidate durations")
		}
		candidate.totalDuration = time.Duration(oldCandidate.TotalDurationNS)
		candidate.totalAwake = time.Duration(oldCandidate.TotalAwakeDurationNS)
		candidate.eligibleDuration = time.Duration(oldCandidate.EligibleDurationNS)
		candidate.awakeDuration = time.Duration(oldCandidate.AwakeDurationNS)
		candidate.invalid = oldCandidate.Invalid
		for id, oldModel := range oldCandidate.Senders {
			model, ok := candidate.senders[id]
			if !ok || oldModel.WouldMiss > oldModel.EligibleEvents {
				return errors.New("dutyscheduler: invalid legacy model")
			}
			model.events = oldModel.EligibleEvents
			model.misses = oldModel.WouldMiss
		}
	}
	restored.lastEnd = time.Duration(snapshot.SampleTimeNS)
	restored.started = snapshot.SampleTimeNS > 0
	restored.deadlineDeficits = snapshot.DeadlineDeficits
	restored.countDeficits = snapshot.CountDeficits
	restored.discontinuities = snapshot.Discontinuities + 1
	if restored.cfg.Mode == ModeGated {
		restored.enterRecovery(restored.lastEnd)
	}
	restored.refreshQualifications()
	*s = *restored
	return nil
}

func sortedSenderIDs(senders map[uint64]*senderRuntime) []uint64 {
	ids := make([]uint64, 0, len(senders))
	for id := range senders {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
