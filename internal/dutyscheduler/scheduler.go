// Package dutyscheduler implements the state and confidence controller for
// selectively suppressing expensive decoder work while sample ingestion stays
// continuous. It is independent of the receiver and decoder so shadow replay
// can exercise exactly the same policy as a live process.
package dutyscheduler

import (
	"errors"
	"math"
	"sort"
	"strings"
	"time"
)

type Mode string

const (
	ModeOff    Mode = "off"
	ModeShadow Mode = "shadow"
	ModeGated  Mode = "gated"
)

type SenderConfig struct {
	ID           uint64
	Overdue      time.Duration
	CountWindow  time.Duration
	MinimumCount int
}

type CandidateConfig struct {
	Name             string
	HistoryLimit     int
	MinHistoryLimit  int
	WarmupIntervals  int
	R900Floor        time.Duration
	OtherFloor       time.Duration
	JitterQuantile   float64
	JitterMargin     time.Duration
	PreScale         float64
	PostScale        float64
	AuditFraction    float64
	ChangePointScale float64
	HistoryGrowAfter int
	WakeScaleStep    float64
	MaxWakeScale     float64
}

type Config struct {
	Mode                 Mode
	Senders              []SenderConfig
	Candidates           []CandidateConfig
	CaptureTarget        float64
	Confidence           float64
	MinimumAudit         float64
	RecoveryDuration     time.Duration
	RefreshInterval      time.Duration
	RefreshDuration      time.Duration
	PromotionMargin      float64
	PromotionStability   time.Duration
	WatchdogHistory      int
	WatchdogMinIntervals int
	WatchdogWindow       time.Duration
	WatchdogQuantile     float64
	WatchdogMargin       float64
	RandomSeed           uint64
}

type Decision struct {
	Decode   bool
	Audit    bool
	Refresh  bool
	Selected string
	State    string
}

type SenderSnapshot struct {
	Protocol                string  `json:"protocol"`
	EligibleEvents          uint64  `json:"eligible_events"`
	WouldMiss               uint64  `json:"would_miss"`
	RequiredEligibleEvents  uint64  `json:"required_eligible_events"`
	RemainingEligibleEvents uint64  `json:"remaining_eligible_events"`
	UpperMissBound          float64 `json:"upper_miss_bound"`
	PeriodNS                int64   `json:"period_ns"`
	PreWakeNS               int64   `json:"pre_wake_ns"`
	PostWakeNS              int64   `json:"post_wake_ns"`
	EffectiveHistory        int     `json:"effective_history"`
	ChangePoints            uint64  `json:"change_points"`
}

type CandidateSnapshot struct {
	Name                   string                    `json:"name"`
	HistoryLimit           int                       `json:"history_limit"`
	Eligible               bool                      `json:"eligible"`
	Qualified              bool                      `json:"qualified"`
	ContractQualified      bool                      `json:"contract_qualified"`
	Invalid                bool                      `json:"invalid"`
	Epoch                  uint64                    `json:"epoch"`
	WakeScale              float64                   `json:"wake_scale"`
	AuditFraction          float64                   `json:"audit_fraction"`
	PromotionReadyNS       int64                     `json:"promotion_ready_ns,omitempty"`
	RecoveryUntilNS        int64                     `json:"recovery_until_ns,omitempty"`
	ProjectedSleepFraction float64                   `json:"projected_sleep_fraction"`
	SteadySleepFraction    float64                   `json:"steady_sleep_fraction"`
	EffectiveSleepFraction float64                   `json:"effective_sleep_fraction"`
	TotalDurationNS        int64                     `json:"total_duration_ns"`
	TotalAwakeDurationNS   int64                     `json:"total_awake_duration_ns"`
	EligibleDurationNS     int64                     `json:"eligible_duration_ns"`
	AwakeDurationNS        int64                     `json:"awake_duration_ns"`
	Senders                map[uint64]SenderSnapshot `json:"senders"`
}

type ObservedSenderSnapshot struct {
	Protocol            string `json:"protocol"`
	Observations        uint64 `json:"observations"`
	LastSeenNS          int64  `json:"last_seen_ns"`
	DeadlineDeficits    uint64 `json:"deadline_deficits"`
	CountDeficits       uint64 `json:"count_deficits"`
	OverdueOpen         bool   `json:"overdue_open"`
	CountOpen           bool   `json:"count_open"`
	WatchdogLearned     bool   `json:"watchdog_learned"`
	LearnedOverdueNS    int64  `json:"learned_overdue_ns,omitempty"`
	LearnedMinimumCount int    `json:"learned_minimum_count,omitempty"`
	ObligationDueNS     int64  `json:"obligation_due_ns,omitempty"`
	ObligationsOpened   uint64 `json:"obligations_opened"`
	ObligationsClosed   uint64 `json:"obligations_closed"`
	ObligationDeficits  uint64 `json:"obligation_deficits"`
}

type ProtocolSnapshot struct {
	Observations       uint64 `json:"observations"`
	DeadlineDeficits   uint64 `json:"deadline_deficits"`
	CountDeficits      uint64 `json:"count_deficits"`
	ObligationDeficits uint64 `json:"obligation_deficits"`
}

type Snapshot struct {
	Mode             Mode                              `json:"mode"`
	State            string                            `json:"state"`
	Selected         string                            `json:"selected,omitempty"`
	SampleTimeNS     int64                             `json:"sample_time_ns"`
	RecoveryUntilNS  int64                             `json:"recovery_until_ns,omitempty"`
	DeadlineDeficits uint64                            `json:"deadline_deficits"`
	CountDeficits    uint64                            `json:"count_deficits"`
	Discontinuities  uint64                            `json:"discontinuities"`
	Refreshes        uint64                            `json:"refreshes"`
	RefreshUntilNS   int64                             `json:"refresh_until_ns,omitempty"`
	NextRefreshNS    int64                             `json:"next_refresh_ns,omitempty"`
	ObservedSenders  map[uint64]ObservedSenderSnapshot `json:"observed_senders"`
	Protocols        map[string]ProtocolSnapshot       `json:"protocols"`
	Candidates       []CandidateSnapshot               `json:"candidates"`
}

type senderModel struct {
	protocol         string
	history          []time.Duration
	last             time.Duration
	seen             bool
	learned          bool
	period           time.Duration
	anchor           time.Duration
	preWake          time.Duration
	postWake         time.Duration
	events           uint64
	misses           uint64
	effectiveHistory int
	stableIntervals  int
	changePoints     uint64
	boundEvents      uint64
	boundMisses      uint64
	boundAlpha       float64
	boundUpper       float64
	boundValid       bool
	boundEvaluations uint64
}

type candidate struct {
	cfg              CandidateConfig
	senders          map[uint64]*senderModel
	senderList       []*senderModel
	lastEligible     bool
	lastAwake        bool
	totalDuration    time.Duration
	totalAwake       time.Duration
	eligibleDuration time.Duration
	awakeDuration    time.Duration
	invalid          bool
	qualified        bool
	epoch            uint64
	wakeScale        float64
	promotionReady   time.Duration
	recoveryUntil    time.Duration
}

type senderRuntime struct {
	cfg                 SenderConfig
	protocol            string
	lastSeen            time.Duration
	seen                bool
	observations        []time.Duration
	total               uint64
	overdueOpen         bool
	countOpen           bool
	deadlineDeficits    uint64
	countDeficits       uint64
	gaps                []time.Duration
	learnedOverdue      time.Duration
	learnedMinimumCount int
	watchdogLearned     bool
	obligationDue       time.Duration
	obligationOpen      bool
	obligationsOpened   uint64
	obligationsClosed   uint64
	obligationDeficits  uint64
}

type protocolRuntime struct {
	observations       uint64
	deadlineDeficits   uint64
	countDeficits      uint64
	obligationDeficits uint64
}

type Scheduler struct {
	cfg              Config
	candidates       []*candidate
	senders          map[uint64]*senderRuntime
	senderList       []*senderRuntime
	selected         *candidate
	lastEnd          time.Duration
	started          bool
	lastDecision     Decision
	recoveryUntil    time.Duration
	deadlineDeficits uint64
	countDeficits    uint64
	discontinuities  uint64
	rng              uint64
	quietActive      bool
	auditActive      bool
	quietCandidate   string
	refreshUntil     time.Duration
	nextRefresh      time.Duration
	refreshes        uint64
	protocols        map[string]*protocolRuntime
	reanchorPending  map[uint64]bool
}

func New(cfg Config) (*Scheduler, error) {
	cfg = normalizeConfig(cfg)
	if cfg.Mode != ModeOff && cfg.Mode != ModeShadow && cfg.Mode != ModeGated {
		return nil, errors.New("dutyscheduler: invalid mode")
	}
	if !finite(cfg.CaptureTarget) || cfg.CaptureTarget <= 0 || cfg.CaptureTarget >= 1 {
		return nil, errors.New("dutyscheduler: capture target must be between zero and one")
	}
	if !finite(cfg.Confidence) || cfg.Confidence <= 0 || cfg.Confidence >= 1 {
		return nil, errors.New("dutyscheduler: confidence must be between zero and one")
	}
	if !finite(cfg.MinimumAudit) || cfg.MinimumAudit < 0 || cfg.MinimumAudit > 1 {
		return nil, errors.New("dutyscheduler: minimum audit must be between zero and one")
	}
	if cfg.RefreshInterval <= 0 || cfg.RefreshDuration <= 0 || cfg.RefreshDuration >= cfg.RefreshInterval {
		return nil, errors.New("dutyscheduler: invalid refresh policy")
	}
	if !finite(cfg.PromotionMargin) || cfg.PromotionMargin < 0 || cfg.CaptureTarget+cfg.PromotionMargin >= 1 || cfg.PromotionStability < 0 {
		return nil, errors.New("dutyscheduler: invalid promotion policy")
	}
	if cfg.WatchdogHistory < 2 || cfg.WatchdogMinIntervals < 2 || cfg.WatchdogMinIntervals > cfg.WatchdogHistory || cfg.WatchdogWindow <= 0 || !finite(cfg.WatchdogQuantile) || cfg.WatchdogQuantile <= 0 || cfg.WatchdogQuantile > 1 || !finite(cfg.WatchdogMargin) || cfg.WatchdogMargin < 0 {
		return nil, errors.New("dutyscheduler: invalid learned watchdog policy")
	}
	if cfg.Mode != ModeOff && len(cfg.Senders) == 0 {
		return nil, errors.New("dutyscheduler: sender inventory is empty")
	}
	if cfg.Mode != ModeOff && len(cfg.Candidates) == 0 {
		return nil, errors.New("dutyscheduler: candidate inventory is empty")
	}

	s := &Scheduler{
		cfg:             cfg,
		senders:         make(map[uint64]*senderRuntime, len(cfg.Senders)),
		senderList:      make([]*senderRuntime, 0, len(cfg.Senders)),
		protocols:       make(map[string]*protocolRuntime),
		reanchorPending: make(map[uint64]bool),
		rng:             cfg.RandomSeed,
	}
	if s.rng == 0 {
		s.rng = 0x9e3779b97f4a7c15
	}
	for _, senderCfg := range cfg.Senders {
		if senderCfg.ID == 0 || senderCfg.Overdue < 0 || senderCfg.CountWindow < 0 || senderCfg.MinimumCount < 0 {
			return nil, errors.New("dutyscheduler: invalid sender configuration")
		}
		if senderCfg.MinimumCount > 0 && senderCfg.CountWindow == 0 {
			return nil, errors.New("dutyscheduler: count threshold requires a window")
		}
		if _, exists := s.senders[senderCfg.ID]; exists {
			return nil, errors.New("dutyscheduler: duplicate sender")
		}
		sender := &senderRuntime{cfg: senderCfg}
		s.senders[senderCfg.ID] = sender
		s.senderList = append(s.senderList, sender)
	}
	for _, candidateCfg := range cfg.Candidates {
		if err := validateCandidate(candidateCfg, cfg.MinimumAudit); err != nil {
			return nil, err
		}
		c := &candidate{
			cfg:        candidateCfg,
			senders:    make(map[uint64]*senderModel, len(cfg.Senders)),
			senderList: make([]*senderModel, 0, len(cfg.Senders)),
		}
		c.wakeScale = 1
		c.epoch = 1
		for _, senderCfg := range cfg.Senders {
			model := &senderModel{}
			c.senders[senderCfg.ID] = model
			c.senderList = append(c.senderList, model)
		}
		s.candidates = append(s.candidates, c)
	}
	return s, nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// Configuration returns the normalized controller configuration. Slice fields
// are copied so callers cannot mutate a running scheduler through the result.
func (s *Scheduler) Configuration() Config {
	result := s.cfg
	result.Senders = append([]SenderConfig(nil), s.cfg.Senders...)
	result.Candidates = append([]CandidateConfig(nil), s.cfg.Candidates...)
	return result
}

func normalizeConfig(cfg Config) Config {
	if cfg.RefreshInterval == 0 {
		cfg.RefreshInterval = 6 * time.Hour
	}
	if cfg.RefreshDuration == 0 {
		cfg.RefreshDuration = 10 * time.Minute
	}
	if cfg.PromotionMargin == 0 {
		cfg.PromotionMargin = 0.00025
	}
	if cfg.PromotionStability == 0 {
		cfg.PromotionStability = 10 * time.Minute
	}
	if cfg.WatchdogHistory == 0 {
		cfg.WatchdogHistory = 128
	}
	if cfg.WatchdogMinIntervals == 0 {
		cfg.WatchdogMinIntervals = 16
	}
	if cfg.WatchdogWindow == 0 {
		cfg.WatchdogWindow = 10 * time.Minute
	}
	if cfg.WatchdogQuantile == 0 {
		cfg.WatchdogQuantile = 0.99
	}
	if cfg.WatchdogMargin == 0 {
		cfg.WatchdogMargin = 0.25
	}
	for idx := range cfg.Candidates {
		candidate := &cfg.Candidates[idx]
		if candidate.MinHistoryLimit == 0 {
			candidate.MinHistoryLimit = 8
			if candidate.MinHistoryLimit > candidate.HistoryLimit {
				candidate.MinHistoryLimit = candidate.HistoryLimit
			}
		}
		if candidate.ChangePointScale == 0 {
			candidate.ChangePointScale = 2
		}
		if candidate.HistoryGrowAfter == 0 {
			candidate.HistoryGrowAfter = 8
		}
		if candidate.WakeScaleStep == 0 {
			candidate.WakeScaleStep = 1.25
		}
		if candidate.MaxWakeScale == 0 {
			candidate.MaxWakeScale = 4
		}
	}
	return cfg
}

func validateCandidate(cfg CandidateConfig, minimumAudit float64) error {
	if cfg.Name == "" || cfg.HistoryLimit < 2 || cfg.MinHistoryLimit < 2 || cfg.MinHistoryLimit > cfg.HistoryLimit || cfg.WarmupIntervals < 2 || cfg.WarmupIntervals > cfg.MinHistoryLimit {
		return errors.New("dutyscheduler: invalid candidate history")
	}
	if cfg.R900Floor <= 0 || cfg.OtherFloor <= 0 || cfg.JitterMargin < 0 {
		return errors.New("dutyscheduler: invalid candidate timing")
	}
	if !finite(cfg.JitterQuantile) || !finite(cfg.PreScale) || !finite(cfg.PostScale) || cfg.JitterQuantile <= 0 || cfg.JitterQuantile > 1 || cfg.PreScale <= 0 || cfg.PostScale <= 0 {
		return errors.New("dutyscheduler: invalid candidate fitting")
	}
	if !finite(cfg.AuditFraction) || cfg.AuditFraction < minimumAudit || cfg.AuditFraction > 1 {
		return errors.New("dutyscheduler: candidate audit is below the invariant")
	}
	if !finite(cfg.ChangePointScale) || !finite(cfg.WakeScaleStep) || !finite(cfg.MaxWakeScale) || cfg.ChangePointScale <= 0 || cfg.HistoryGrowAfter < 1 || cfg.WakeScaleStep <= 1 || cfg.MaxWakeScale < cfg.WakeScaleStep {
		return errors.New("dutyscheduler: invalid adaptive controller policy")
	}
	return nil
}

// Advance scores [start,end) for every candidate and returns whether the live
// decoder must process it. Calls must be contiguous and monotonic.
func (s *Scheduler) Advance(start, end time.Duration) Decision {
	if start < 0 || end <= start || (s.started && start != s.lastEnd) {
		now := s.lastEnd
		if end > start && end >= 0 {
			now = end
			s.lastEnd = end
		}
		s.started = true
		s.resetLearning()
		s.enterRecovery(now)
		decision := Decision{Decode: true, State: s.state()}
		s.lastDecision = decision
		return decision
	}
	s.started = true
	s.lastEnd = end
	if s.refreshUntil > 0 && start >= s.refreshUntil {
		if s.selected != nil {
			s.selected.tightenAfterRefresh(start)
			s.selected = nil
		}
		s.refreshUntil = 0
		s.nextRefresh = 0
	}

	for _, c := range s.candidates {
		c.lastEligible = c.allLearned()
		c.lastAwake = true
		duration := end - start
		if c.lastEligible {
			c.lastAwake = c.awakeDuring(start, end)
			c.eligibleDuration += duration
			if c.lastAwake {
				c.awakeDuration += duration
			}
		}
		c.totalDuration += duration
		if c.lastAwake {
			c.totalAwake += duration
		}
	}

	s.checkWatchdogs(end)
	s.refreshQualifications(end)
	s.chooseCandidate()

	decision := Decision{Decode: true, State: s.state(), Selected: s.selectedName()}
	if s.selected != nil && end >= s.recoveryUntil {
		if s.nextRefresh == 0 {
			s.nextRefresh = end + s.selected.refreshInterval(s.cfg.RefreshInterval)
		}
		if end >= s.nextRefresh {
			s.refreshUntil = end + s.cfg.RefreshDuration
			s.nextRefresh = end + s.selected.refreshInterval(s.cfg.RefreshInterval)
			s.refreshes++
		}
	}
	if s.selected != nil && end < s.refreshUntil {
		decision.Refresh = true
		decision.State = s.state()
		s.quietActive = false
		s.auditActive = false
		s.quietCandidate = ""
		s.lastDecision = decision
		return decision
	}
	sleepInterval := s.cfg.Mode == ModeGated && s.selected != nil && end >= s.recoveryUntil && s.selected.lastEligible && !s.selected.lastAwake
	if !sleepInterval {
		s.quietActive = false
		s.auditActive = false
		s.quietCandidate = ""
	} else {
		selectedName := s.selected.cfg.Name
		if !s.quietActive || s.quietCandidate != selectedName {
			s.quietActive = true
			s.quietCandidate = selectedName
			s.auditActive = s.randomUnit() < s.selected.auditFraction()
		}
		decision.Decode = s.auditActive
		decision.Audit = s.auditActive
	}
	s.lastDecision = decision
	return decision
}

func (s *Scheduler) resetLearning() {
	s.discontinuities++
	for _, c := range s.candidates {
		for _, sender := range c.senderList {
			*sender = senderModel{}
		}
		c.lastEligible = false
		c.lastAwake = true
		c.eligibleDuration = 0
		c.awakeDuration = 0
		c.invalid = false
		c.qualified = false
		c.epoch++
		c.promotionReady = 0
		c.recoveryUntil = 0
		c.wakeScale = 1
	}
	for _, sender := range s.senderList {
		sender.protocol = ""
		sender.lastSeen = 0
		sender.seen = false
		sender.observations = sender.observations[:0]
		sender.overdueOpen = false
		sender.countOpen = false
		sender.gaps = sender.gaps[:0]
		sender.learnedOverdue = 0
		sender.learnedMinimumCount = 0
		sender.watchdogLearned = false
		sender.obligationDue = 0
		sender.obligationOpen = false
	}
	s.refreshUntil = 0
	s.nextRefresh = 0
}

// Observe supplies one adjacent-deduplicated, configured-sender arrival. In
// shadow mode every arrival is observed. In gated mode an arrival during an
// audit is an escape and forces recovery.
func (s *Scheduler) Observe(id uint64, protocol string, at time.Duration) {
	s.observe(id, protocol, at, false)
}

// ObserveEscape supplies an arrival recovered from a block the scheduler had
// elected not to decode. Replay happens after the clock has advanced into an
// awake block, so the escape must be carried explicitly rather than inferred
// from the current block decision.
func (s *Scheduler) ObserveEscape(id uint64, protocol string, at time.Duration) {
	s.observe(id, protocol, at, true)
}

// PrepareResume keeps a complete checkpoint fail-open until every previously
// seen sender has supplied one live post-restart arrival. That first arrival
// reanchors cadence phase without treating process downtime as RF drift or as
// capture evidence.
func (s *Scheduler) PrepareResume() {
	clear(s.reanchorPending)
	for _, sender := range s.senderList {
		if sender.seen {
			s.reanchorPending[sender.cfg.ID] = true
		}
	}
	s.selected = nil
	s.quietActive = false
	s.auditActive = false
	s.quietCandidate = ""
}

func (s *Scheduler) observe(id uint64, protocol string, at time.Duration, forcedEscape bool) {
	runtimeSender, configured := s.senders[id]
	if !configured || at < 0 || (runtimeSender.seen && at <= runtimeSender.lastSeen) {
		if configured && runtimeSender.seen && at < runtimeSender.lastSeen {
			s.enterRecovery(s.lastEnd)
		}
		return
	}
	if runtimeSender.protocol != "" && runtimeSender.protocol != protocol {
		s.enterRecovery(s.lastEnd)
		return
	}
	previous := runtimeSender.lastSeen
	hadPrevious := runtimeSender.seen
	reanchor := s.reanchorPending[id]
	runtimeSender.protocol = protocol
	runtimeSender.lastSeen = at
	runtimeSender.seen = true
	runtimeSender.total++
	runtimeSender.overdueOpen = false
	if runtimeSender.obligationOpen {
		runtimeSender.obligationsClosed++
		runtimeSender.obligationOpen = false
	}
	runtimeSender.observations = append(runtimeSender.observations, at)
	if hadPrevious && !reanchor {
		runtimeSender.gaps = append(runtimeSender.gaps, at-previous)
		if len(runtimeSender.gaps) > s.cfg.WatchdogHistory {
			copy(runtimeSender.gaps, runtimeSender.gaps[len(runtimeSender.gaps)-s.cfg.WatchdogHistory:])
			runtimeSender.gaps = runtimeSender.gaps[:s.cfg.WatchdogHistory]
		}
		s.learnWatchdog(runtimeSender)
	}
	window := s.effectiveCountWindow(runtimeSender)
	if window > 0 {
		trimBefore := at - window*2
		first := 0
		for first < len(runtimeSender.observations) && runtimeSender.observations[first] < trimBefore {
			first++
		}
		if first > 0 {
			copy(runtimeSender.observations, runtimeSender.observations[first:])
			runtimeSender.observations = runtimeSender.observations[:len(runtimeSender.observations)-first]
		}
	}
	if overdue := s.effectiveOverdue(runtimeSender); overdue > 0 {
		runtimeSender.obligationDue = at + overdue
		runtimeSender.obligationOpen = true
		runtimeSender.obligationsOpened++
	}
	if protocol != "" {
		totals := s.protocols[protocol]
		if totals == nil {
			totals = &protocolRuntime{}
			s.protocols[protocol] = totals
		}
		totals.observations++
	}

	for _, c := range s.candidates {
		model := c.senders[id]
		if reanchor {
			model.reanchor(protocol, at)
			continue
		}
		if c.lastEligible {
			model.events++
			if !c.lastAwake {
				model.misses++
			}
		}
		if model.observe(protocol, at, c.cfg, c.wakeScale) {
			c.beginNewEpoch(at, true)
		}
	}
	if reanchor {
		delete(s.reanchorPending, id)
	}

	if s.cfg.Mode == ModeGated && s.selected != nil && (forcedEscape || (s.selected.lastEligible && !s.selected.lastAwake)) {
		s.recoverAtLeast(s.selected.steadySleep(), at)
		s.enterRecovery(s.lastEnd)
	}
	s.refreshQualifications(at)
	s.chooseCandidate()
}

func (m *senderModel) observe(protocol string, at time.Duration, cfg CandidateConfig, wakeScale float64) bool {
	changed := false
	if m.protocol != "" && m.protocol != protocol {
		m.history = m.history[:0]
		m.seen = false
		m.learned = false
		m.effectiveHistory = 0
		changed = true
	}
	m.protocol = protocol
	if m.seen {
		delta := at - m.last
		if delta > 0 {
			if m.learned && m.period > 0 {
				multiple := math.Round(float64(delta) / float64(m.period))
				if multiple < 1 {
					multiple = 1
				}
				residual := time.Duration(math.Abs(float64(delta) - multiple*float64(m.period)))
				tolerance := time.Duration(float64(maxDuration(m.preWake, m.postWake)) * cfg.ChangePointScale)
				if tolerance < m.period/20 {
					tolerance = m.period / 20
				}
				if residual > tolerance {
					changed = true
					m.changePoints++
					m.stableIntervals = 0
					m.effectiveHistory = min(cfg.MinHistoryLimit, len(m.history)+1)
				} else {
					m.stableIntervals++
				}
			}
			m.history = append(m.history, delta)
			if len(m.history) > cfg.HistoryLimit {
				copy(m.history, m.history[len(m.history)-cfg.HistoryLimit:])
				m.history = m.history[:cfg.HistoryLimit]
			}
		}
	}
	m.last = at
	m.seen = true
	if len(m.history) < cfg.WarmupIntervals {
		return changed
	}
	if m.effectiveHistory == 0 {
		m.effectiveHistory = min(len(m.history), cfg.MinHistoryLimit)
	}
	if m.stableIntervals >= cfg.HistoryGrowAfter && m.effectiveHistory < min(len(m.history), cfg.HistoryLimit) {
		next := nextHistoryLimit(m.effectiveHistory, min(len(m.history), cfg.HistoryLimit))
		if compatibleHistory(m.history, m.effectiveHistory, next, maxDuration(m.preWake, m.postWake)) {
			m.effectiveHistory = next
			m.stableIntervals = 0
		}
	}
	history := m.history[len(m.history)-m.effectiveHistory:]
	period, residuals := robustPeriod(history)
	floor := cfg.OtherFloor
	if strings.HasPrefix(strings.ToLower(protocol), "r900") {
		floor = cfg.R900Floor
	}
	halfwidth := floor
	learned := durationQuantile(residuals, cfg.JitterQuantile) + cfg.JitterMargin
	if learned > halfwidth {
		halfwidth = learned
	}
	m.period = period
	m.anchor = at
	m.preWake = scaleDuration(halfwidth, cfg.PreScale*wakeScale)
	m.postWake = scaleDuration(halfwidth, cfg.PostScale*wakeScale)
	m.learned = period > 0
	return changed
}

func (m *senderModel) reanchor(protocol string, at time.Duration) {
	m.protocol = protocol
	m.last = at
	m.anchor = at
	m.seen = true
}

func maxDuration(left, right time.Duration) time.Duration {
	if left > right {
		return left
	}
	return right
}

func nextHistoryLimit(current, maximum int) int {
	for _, candidate := range []int{8, 16, 32, 64, 128} {
		if candidate > current {
			return min(candidate, maximum)
		}
	}
	return maximum
}

func compatibleHistory(history []time.Duration, recentCount, extendedCount int, tolerance time.Duration) bool {
	if recentCount < 2 || extendedCount <= recentCount || extendedCount > len(history) {
		return false
	}
	recent, _ := robustPeriod(history[len(history)-recentCount:])
	extended, _ := robustPeriod(history[len(history)-extendedCount:])
	if recent <= 0 || extended <= 0 {
		return false
	}
	minimumTolerance := recent / 1000
	if tolerance < minimumTolerance {
		tolerance = minimumTolerance
	}
	return time.Duration(math.Abs(float64(recent-extended))) <= tolerance
}

func (c *candidate) beginNewEpoch(now time.Duration, widen bool) {
	c.epoch++
	c.qualified = false
	c.promotionReady = 0
	if widen {
		c.wakeScale *= c.cfg.WakeScaleStep
		if c.wakeScale > c.cfg.MaxWakeScale {
			c.wakeScale = c.cfg.MaxWakeScale
		}
	}
	for _, sender := range c.senderList {
		sender.events = 0
		sender.misses = 0
		if widen && sender.learned {
			sender.preWake = scaleDuration(sender.preWake, c.cfg.WakeScaleStep)
			sender.postWake = scaleDuration(sender.postWake, c.cfg.WakeScaleStep)
		}
	}
	c.recoveryUntil = now
}

func (c *candidate) tightenAfterRefresh(now time.Duration) {
	if c.wakeScale <= 1 {
		return
	}
	oldScale := c.wakeScale
	c.wakeScale /= c.cfg.WakeScaleStep
	if c.wakeScale < 1 {
		c.wakeScale = 1
	}
	ratio := c.wakeScale / oldScale
	for _, sender := range c.senderList {
		if sender.learned {
			sender.preWake = scaleDuration(sender.preWake, ratio)
			sender.postWake = scaleDuration(sender.postWake, ratio)
		}
	}
	c.beginNewEpoch(now, false)
}

func scaleDuration(value time.Duration, scale float64) time.Duration {
	if value <= 0 || scale <= 0 {
		return 0
	}
	return time.Duration(float64(value) * scale)
}

func (c *candidate) allLearned() bool {
	for _, sender := range c.senderList {
		if !sender.learned {
			return false
		}
	}
	return true
}

func (c *candidate) awakeDuring(start, end time.Duration) bool {
	for _, sender := range c.senderList {
		if sender.awakeDuring(start, end) {
			return true
		}
	}
	return false
}

func (m *senderModel) awakeDuring(start, end time.Duration) bool {
	if !m.learned || m.period <= 0 {
		return true
	}
	period := float64(m.period)
	first := math.Ceil(float64(start-m.postWake-m.anchor) / period)
	center := float64(m.anchor) + first*period
	return center-float64(m.preWake) < float64(end) && center+float64(m.postWake) > float64(start)
}

func robustPeriod(history []time.Duration) (time.Duration, []time.Duration) {
	values := make([]float64, len(history))
	base := history[0]
	for idx, value := range history {
		if value < base {
			base = value
		}
		values[idx] = float64(value)
	}
	period := float64(base)
	for iteration := 0; iteration < 4; iteration++ {
		normalized := make([]float64, len(values))
		for idx, value := range values {
			multiple := math.Round(value / period)
			if multiple < 1 {
				multiple = 1
			}
			normalized[idx] = value / multiple
		}
		center := medianFloat(normalized)
		deviations := make([]float64, len(normalized))
		for idx, value := range normalized {
			deviations[idx] = math.Abs(value - center)
		}
		mad := medianFloat(deviations)
		retained := normalized[:0]
		for _, value := range normalized {
			if mad == 0 || math.Abs(value-center) <= 4*mad {
				retained = append(retained, value)
			}
		}
		period = medianFloat(retained)
	}
	resultPeriod := time.Duration(period)
	residuals := make([]time.Duration, len(values))
	for idx, value := range values {
		multiple := math.Round(value / period)
		if multiple < 1 {
			multiple = 1
		}
		residuals[idx] = time.Duration(math.Abs(value - multiple*period))
	}
	return resultPeriod, residuals
}

func medianFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	middle := len(ordered) / 2
	if len(ordered)%2 == 0 {
		return (ordered[middle-1] + ordered[middle]) / 2
	}
	return ordered[middle]
}

func durationQuantile(values []time.Duration, probability float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	rank := int(math.Ceil(probability*float64(len(ordered)))) - 1
	if rank < 0 {
		rank = 0
	}
	return ordered[rank]
}

func (s *Scheduler) learnWatchdog(sender *senderRuntime) {
	if len(sender.gaps) < s.cfg.WatchdogMinIntervals {
		return
	}
	gap := durationQuantile(sender.gaps, s.cfg.WatchdogQuantile)
	learned := time.Duration(float64(gap) * (1 + s.cfg.WatchdogMargin))
	if learned <= 0 {
		return
	}
	sender.learnedOverdue = learned
	minimum := int(s.cfg.WatchdogWindow / learned)
	if minimum < 1 {
		minimum = 1
	}
	sender.learnedMinimumCount = minimum
	sender.watchdogLearned = true
}

func (s *Scheduler) effectiveOverdue(sender *senderRuntime) time.Duration {
	result := sender.cfg.Overdue
	if sender.learnedOverdue > result {
		result = sender.learnedOverdue
	}
	return result
}

func (s *Scheduler) effectiveCountWindow(sender *senderRuntime) time.Duration {
	if sender.cfg.CountWindow > 0 {
		return sender.cfg.CountWindow
	}
	if sender.watchdogLearned {
		return s.cfg.WatchdogWindow
	}
	return 0
}

func (s *Scheduler) effectiveMinimumCount(sender *senderRuntime) int {
	result := sender.cfg.MinimumCount
	if sender.watchdogLearned && (result == 0 || sender.learnedMinimumCount < result) {
		result = sender.learnedMinimumCount
	}
	return result
}

func (s *Scheduler) watchdogsReady() bool {
	for _, sender := range s.senderList {
		seeded := sender.cfg.Overdue > 0 && sender.cfg.CountWindow > 0 && sender.cfg.MinimumCount > 0
		if !seeded && !sender.watchdogLearned {
			return false
		}
	}
	return true
}

func (s *Scheduler) checkWatchdogs(now time.Duration) {
	failed := false
	for _, sender := range s.senderList {
		overdue := s.effectiveOverdue(sender)
		if sender.seen && sender.obligationOpen && overdue > 0 && now >= sender.obligationDue {
			if !sender.overdueOpen {
				s.deadlineDeficits++
				sender.deadlineDeficits++
				sender.obligationDeficits++
				sender.overdueOpen = true
				sender.obligationOpen = false
				if totals := s.protocols[sender.protocol]; totals != nil {
					totals.deadlineDeficits++
					totals.obligationDeficits++
				}
				failed = true
			}
		}
		window := s.effectiveCountWindow(sender)
		minimum := s.effectiveMinimumCount(sender)
		if sender.seen && minimum > 0 && window > 0 && now >= window {
			lower := now - window
			count := 0
			for _, observed := range sender.observations {
				if observed >= lower && observed <= now {
					count++
				}
			}
			deficit := count < minimum
			if deficit && !sender.countOpen {
				s.countDeficits++
				sender.countDeficits++
				sender.countOpen = true
				if totals := s.protocols[sender.protocol]; totals != nil {
					totals.countDeficits++
				}
				failed = true
			} else if !deficit {
				sender.countOpen = false
			}
		}
	}
	if failed && s.cfg.Mode == ModeGated && s.selected != nil && now >= s.recoveryUntil {
		selectedSkip := s.selected.steadySleep()
		s.recoverAtLeast(selectedSkip, now)
		s.enterRecovery(now)
	}
}

func (s *Scheduler) enterRecovery(now time.Duration) {
	until := now + s.cfg.RecoveryDuration
	if until > s.recoveryUntil {
		s.recoveryUntil = until
	}
	s.selected = nil
	s.quietActive = false
	s.auditActive = false
	s.quietCandidate = ""
	s.refreshUntil = 0
	s.nextRefresh = 0
}

func (s *Scheduler) recoverAtLeast(skip float64, now time.Duration) {
	for _, c := range s.candidates {
		if c.steadySleep() >= skip {
			c.invalid = true
			c.beginNewEpoch(now, true)
			c.recoveryUntil = now + s.cfg.RecoveryDuration
		}
	}
}

func (s *Scheduler) chooseCandidate() {
	if s.cfg.Mode != ModeGated || s.lastEnd < s.recoveryUntil || len(s.reanchorPending) > 0 || !s.watchdogsReady() {
		s.selected = nil
		return
	}
	var best *candidate
	for _, c := range s.candidates {
		if !c.qualified {
			continue
		}
		if best == nil || c.effectiveSleep() > best.effectiveSleep() {
			best = c
		}
	}
	s.selected = best
}

func (s *Scheduler) qualifiesAt(c *candidate, target float64) bool {
	if c.invalid || !c.allLearned() || c.eligibleDuration <= 0 {
		return false
	}
	maxMiss := 1 - target
	alpha := 1 - s.cfg.Confidence
	for _, sender := range c.senderList {
		if sender.events == 0 || sender.confidenceUpperBound(alpha) > maxMiss {
			return false
		}
	}
	return true
}

func (s *Scheduler) refreshQualifications(now time.Duration) {
	for _, c := range s.candidates {
		if c.invalid && now >= c.recoveryUntil {
			c.invalid = false
		}
		if c.qualified {
			if !s.qualifiesAt(c, s.cfg.CaptureTarget) {
				c.qualified = false
				c.promotionReady = 0
			}
			continue
		}
		promotionTarget := s.cfg.CaptureTarget + s.cfg.PromotionMargin
		if !s.qualifiesAt(c, promotionTarget) {
			c.promotionReady = 0
			continue
		}
		if c.promotionReady == 0 {
			c.promotionReady = now + s.cfg.PromotionStability
		}
		if now >= c.promotionReady {
			c.qualified = true
		}
	}
}

func (c *candidate) projectedSleep() float64 {
	if c.totalDuration <= 0 {
		return 0
	}
	return 1 - float64(c.totalAwake)/float64(c.totalDuration)
}

func (c *candidate) steadySleep() float64 {
	if c.eligibleDuration <= 0 {
		return 0
	}
	return 1 - float64(c.awakeDuration)/float64(c.eligibleDuration)
}

func (c *candidate) effectiveSleep() float64 {
	return c.steadySleep() * (1 - c.auditFraction())
}

func (c *candidate) auditFraction() float64 {
	result := c.cfg.AuditFraction * c.wakeScale
	if result > 1 {
		return 1
	}
	return result
}

func (c *candidate) refreshInterval(base time.Duration) time.Duration {
	if c.wakeScale <= 1 {
		return base
	}
	interval := time.Duration(float64(base) / math.Min(c.wakeScale, 4))
	if interval < time.Minute {
		return time.Minute
	}
	return interval
}

func (s *Scheduler) randomUnit() float64 {
	x := s.rng
	x ^= x >> 12
	x ^= x << 25
	x ^= x >> 27
	s.rng = x
	return float64(x*2685821657736338717>>11) / float64(uint64(1)<<53)
}

func (s *Scheduler) selectedName() string {
	if s.selected == nil {
		return ""
	}
	return s.selected.cfg.Name
}

func (s *Scheduler) state() string {
	if s.cfg.Mode == ModeOff {
		return "OFF"
	}
	if s.lastEnd < s.recoveryUntil {
		return "RECOVERY"
	}
	if s.cfg.Mode == ModeGated && s.selected != nil {
		return "GATED"
	}
	for _, c := range s.candidates {
		if c.qualified {
			if s.watchdogsReady() {
				return "QUALIFIED"
			}
			return "SEED"
		}
	}
	return "SEED"
}

func (s *Scheduler) Snapshot() Snapshot {
	result := Snapshot{
		Mode:             s.cfg.Mode,
		State:            s.state(),
		Selected:         s.selectedName(),
		SampleTimeNS:     int64(s.lastEnd),
		RecoveryUntilNS:  int64(s.recoveryUntil),
		DeadlineDeficits: s.deadlineDeficits,
		CountDeficits:    s.countDeficits,
		Discontinuities:  s.discontinuities,
		Refreshes:        s.refreshes,
		RefreshUntilNS:   int64(s.refreshUntil),
		NextRefreshNS:    int64(s.nextRefresh),
		ObservedSenders:  make(map[uint64]ObservedSenderSnapshot, len(s.senders)),
		Protocols:        make(map[string]ProtocolSnapshot, len(s.protocols)),
	}
	for id, sender := range s.senders {
		result.ObservedSenders[id] = ObservedSenderSnapshot{
			Protocol:            sender.protocol,
			Observations:        sender.total,
			LastSeenNS:          int64(sender.lastSeen),
			DeadlineDeficits:    sender.deadlineDeficits,
			CountDeficits:       sender.countDeficits,
			OverdueOpen:         sender.overdueOpen,
			CountOpen:           sender.countOpen,
			WatchdogLearned:     sender.watchdogLearned,
			LearnedOverdueNS:    int64(sender.learnedOverdue),
			LearnedMinimumCount: sender.learnedMinimumCount,
			ObligationDueNS:     sender.obligationDue.Nanoseconds(),
			ObligationsOpened:   sender.obligationsOpened,
			ObligationsClosed:   sender.obligationsClosed,
			ObligationDeficits:  sender.obligationDeficits,
		}
	}
	for protocol, totals := range s.protocols {
		result.Protocols[protocol] = ProtocolSnapshot{
			Observations:       totals.observations,
			DeadlineDeficits:   totals.deadlineDeficits,
			CountDeficits:      totals.countDeficits,
			ObligationDeficits: totals.obligationDeficits,
		}
	}
	alpha := 1 - s.cfg.Confidence
	for _, c := range s.candidates {
		candidateSnapshot := CandidateSnapshot{
			Name:                   c.cfg.Name,
			HistoryLimit:           c.cfg.HistoryLimit,
			Eligible:               c.allLearned(),
			Qualified:              c.qualified,
			ContractQualified:      s.qualifiesAt(c, s.cfg.CaptureTarget),
			Invalid:                c.invalid,
			Epoch:                  c.epoch,
			WakeScale:              c.wakeScale,
			AuditFraction:          c.auditFraction(),
			PromotionReadyNS:       int64(c.promotionReady),
			RecoveryUntilNS:        int64(c.recoveryUntil),
			ProjectedSleepFraction: c.projectedSleep(),
			SteadySleepFraction:    c.steadySleep(),
			EffectiveSleepFraction: c.effectiveSleep(),
			TotalDurationNS:        int64(c.totalDuration),
			TotalAwakeDurationNS:   int64(c.totalAwake),
			EligibleDurationNS:     int64(c.eligibleDuration),
			AwakeDurationNS:        int64(c.awakeDuration),
			Senders:                make(map[uint64]SenderSnapshot, len(c.senders)),
		}
		for id, sender := range c.senders {
			required := RequiredObservations(sender.misses, s.cfg.CaptureTarget, s.cfg.Confidence)
			remaining := uint64(0)
			if required > sender.events {
				remaining = required - sender.events
			}
			candidateSnapshot.Senders[id] = SenderSnapshot{
				Protocol:                sender.protocol,
				EligibleEvents:          sender.events,
				WouldMiss:               sender.misses,
				RequiredEligibleEvents:  required,
				RemainingEligibleEvents: remaining,
				UpperMissBound:          sender.confidenceUpperBound(alpha),
				PeriodNS:                int64(sender.period),
				PreWakeNS:               int64(sender.preWake),
				PostWakeNS:              int64(sender.postWake),
				EffectiveHistory:        sender.effectiveHistory,
				ChangePoints:            sender.changePoints,
			}
		}
		result.Candidates = append(result.Candidates, candidateSnapshot)
	}
	return result
}

// confidenceUpperBound memoizes the exact confidence calculation for one
// evidence tuple. Advance evaluates qualification for every input block, but
// the tuple changes only when a configured sender is observed. Keeping the
// cache on the model preserves exact qualification and timer behavior without
// repeating the numerical solver for unchanged evidence.
func (m *senderModel) confidenceUpperBound(alpha float64) float64 {
	if m.boundValid && m.boundEvents == m.events && m.boundMisses == m.misses && m.boundAlpha == alpha {
		return m.boundUpper
	}
	m.boundEvents = m.events
	m.boundMisses = m.misses
	m.boundAlpha = alpha
	m.boundUpper = upperMissBound(m.misses, m.events, alpha)
	m.boundValid = true
	m.boundEvaluations++
	return m.boundUpper
}

// RequiredObservations returns the smallest total observation count that
// would satisfy the configured one-sided miss-rate bound if no further misses
// occurred. It is an ETA aid only; qualification always recomputes the bound
// from the observations actually collected.
func RequiredObservations(misses uint64, captureTarget, confidence float64) uint64 {
	maxMiss := 1 - captureTarget
	alpha := 1 - confidence
	if maxMiss <= 0 || maxMiss >= 1 || alpha <= 0 || alpha >= 1 {
		return ^uint64(0)
	}
	passes := func(observations uint64) bool {
		return observations > misses && upperMissBound(misses, observations, alpha) <= maxMiss
	}
	low := misses
	high := misses + 1
	for !passes(high) {
		if high >= 1<<62 {
			return ^uint64(0)
		}
		high *= 2
	}
	for low+1 < high {
		middle := low + (high-low)/2
		if passes(middle) {
			high = middle
		} else {
			low = middle
		}
	}
	return high
}

// upperMissBound is the exact one-sided Clopper-Pearson upper confidence
// bound for a binomial failure rate. alpha is one minus confidence.
func upperMissBound(misses, observations uint64, alpha float64) float64 {
	if observations == 0 || misses >= observations {
		return 1
	}
	if misses == 0 {
		return 1 - math.Pow(alpha, 1/float64(observations))
	}
	low, high := float64(misses)/float64(observations), 1.0
	for iteration := 0; iteration < 80; iteration++ {
		mid := (low + high) / 2
		if binomialCDF(observations, misses, mid) > alpha {
			low = mid
		} else {
			high = mid
		}
	}
	return high
}

func binomialCDF(n, through uint64, probability float64) float64 {
	logs := make([]float64, through+1)
	maximum := math.Inf(-1)
	for k := uint64(0); k <= through; k++ {
		choose, _ := math.Lgamma(float64(n + 1))
		left, _ := math.Lgamma(float64(k + 1))
		right, _ := math.Lgamma(float64(n - k + 1))
		value := choose - left - right + float64(k)*math.Log(probability) + float64(n-k)*math.Log1p(-probability)
		logs[k] = value
		if value > maximum {
			maximum = value
		}
	}
	sum := 0.0
	for _, value := range logs {
		sum += math.Exp(value - maximum)
	}
	return math.Exp(maximum) * sum
}
