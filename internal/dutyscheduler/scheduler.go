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
	Name            string
	HistoryLimit    int
	WarmupIntervals int
	R900Floor       time.Duration
	OtherFloor      time.Duration
	JitterQuantile  float64
	JitterMargin    time.Duration
	PreScale        float64
	PostScale       float64
	AuditFraction   float64
}

type Config struct {
	Mode             Mode
	Senders          []SenderConfig
	Candidates       []CandidateConfig
	CaptureTarget    float64
	Confidence       float64
	MinimumAudit     float64
	RecoveryDuration time.Duration
	RandomSeed       uint64
}

type Decision struct {
	Decode   bool
	Audit    bool
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
}

type CandidateSnapshot struct {
	Name                   string                    `json:"name"`
	HistoryLimit           int                       `json:"history_limit"`
	Eligible               bool                      `json:"eligible"`
	Qualified              bool                      `json:"qualified"`
	Invalid                bool                      `json:"invalid"`
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
	Protocol         string `json:"protocol"`
	Observations     uint64 `json:"observations"`
	LastSeenNS       int64  `json:"last_seen_ns"`
	DeadlineDeficits uint64 `json:"deadline_deficits"`
	CountDeficits    uint64 `json:"count_deficits"`
	OverdueOpen      bool   `json:"overdue_open"`
	CountOpen        bool   `json:"count_open"`
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
	ObservedSenders  map[uint64]ObservedSenderSnapshot `json:"observed_senders"`
	Candidates       []CandidateSnapshot               `json:"candidates"`
}

type senderModel struct {
	protocol string
	history  []time.Duration
	last     time.Duration
	seen     bool
	learned  bool
	period   time.Duration
	anchor   time.Duration
	preWake  time.Duration
	postWake time.Duration
	events   uint64
	misses   uint64
}

type candidate struct {
	cfg              CandidateConfig
	senders          map[uint64]*senderModel
	lastEligible     bool
	lastAwake        bool
	totalDuration    time.Duration
	totalAwake       time.Duration
	eligibleDuration time.Duration
	awakeDuration    time.Duration
	invalid          bool
	qualified        bool
}

type senderRuntime struct {
	cfg              SenderConfig
	protocol         string
	lastSeen         time.Duration
	seen             bool
	observations     []time.Duration
	total            uint64
	overdueOpen      bool
	countOpen        bool
	deadlineDeficits uint64
	countDeficits    uint64
}

type Scheduler struct {
	cfg              Config
	candidates       []*candidate
	senders          map[uint64]*senderRuntime
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
}

func New(cfg Config) (*Scheduler, error) {
	if cfg.Mode != ModeOff && cfg.Mode != ModeShadow && cfg.Mode != ModeGated {
		return nil, errors.New("dutyscheduler: invalid mode")
	}
	if cfg.CaptureTarget <= 0 || cfg.CaptureTarget >= 1 {
		return nil, errors.New("dutyscheduler: capture target must be between zero and one")
	}
	if cfg.Confidence <= 0 || cfg.Confidence >= 1 {
		return nil, errors.New("dutyscheduler: confidence must be between zero and one")
	}
	if cfg.MinimumAudit < 0 || cfg.MinimumAudit > 1 {
		return nil, errors.New("dutyscheduler: minimum audit must be between zero and one")
	}
	if cfg.Mode != ModeOff && len(cfg.Senders) == 0 {
		return nil, errors.New("dutyscheduler: sender inventory is empty")
	}
	if cfg.Mode != ModeOff && len(cfg.Candidates) == 0 {
		return nil, errors.New("dutyscheduler: candidate inventory is empty")
	}

	s := &Scheduler{
		cfg:     cfg,
		senders: make(map[uint64]*senderRuntime, len(cfg.Senders)),
		rng:     cfg.RandomSeed,
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
		s.senders[senderCfg.ID] = &senderRuntime{cfg: senderCfg}
	}
	for _, candidateCfg := range cfg.Candidates {
		if err := validateCandidate(candidateCfg, cfg.MinimumAudit); err != nil {
			return nil, err
		}
		c := &candidate{cfg: candidateCfg, senders: make(map[uint64]*senderModel, len(cfg.Senders))}
		for _, senderCfg := range cfg.Senders {
			c.senders[senderCfg.ID] = &senderModel{}
		}
		s.candidates = append(s.candidates, c)
	}
	return s, nil
}

func validateCandidate(cfg CandidateConfig, minimumAudit float64) error {
	if cfg.Name == "" || cfg.HistoryLimit < 2 || cfg.WarmupIntervals < 2 || cfg.WarmupIntervals > cfg.HistoryLimit {
		return errors.New("dutyscheduler: invalid candidate history")
	}
	if cfg.R900Floor <= 0 || cfg.OtherFloor <= 0 || cfg.JitterMargin < 0 {
		return errors.New("dutyscheduler: invalid candidate timing")
	}
	if cfg.JitterQuantile <= 0 || cfg.JitterQuantile > 1 || cfg.PreScale <= 0 || cfg.PostScale <= 0 {
		return errors.New("dutyscheduler: invalid candidate fitting")
	}
	if cfg.AuditFraction < minimumAudit || cfg.AuditFraction > 1 {
		return errors.New("dutyscheduler: candidate audit is below the invariant")
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
	s.chooseCandidate()

	decision := Decision{Decode: true, State: s.state(), Selected: s.selectedName()}
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
			s.auditActive = s.randomUnit() < s.selected.cfg.AuditFraction
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
		for id := range c.senders {
			c.senders[id] = &senderModel{}
		}
		c.lastEligible = false
		c.lastAwake = true
		c.eligibleDuration = 0
		c.awakeDuration = 0
		c.invalid = false
		c.qualified = false
	}
	for _, sender := range s.senders {
		sender.protocol = ""
		sender.lastSeen = 0
		sender.seen = false
		sender.observations = sender.observations[:0]
		sender.overdueOpen = false
		sender.countOpen = false
	}
}

// Observe supplies one adjacent-deduplicated, configured-sender arrival. In
// shadow mode every arrival is observed. In gated mode an arrival during an
// audit is an escape and forces recovery.
func (s *Scheduler) Observe(id uint64, protocol string, at time.Duration) {
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
	runtimeSender.protocol = protocol
	runtimeSender.lastSeen = at
	runtimeSender.seen = true
	runtimeSender.total++
	runtimeSender.overdueOpen = false
	runtimeSender.observations = append(runtimeSender.observations, at)
	if runtimeSender.cfg.CountWindow > 0 {
		trimBefore := at - runtimeSender.cfg.CountWindow*2
		first := 0
		for first < len(runtimeSender.observations) && runtimeSender.observations[first] < trimBefore {
			first++
		}
		if first > 0 {
			copy(runtimeSender.observations, runtimeSender.observations[first:])
			runtimeSender.observations = runtimeSender.observations[:len(runtimeSender.observations)-first]
		}
	}

	for _, c := range s.candidates {
		model := c.senders[id]
		if c.lastEligible {
			model.events++
			if !c.lastAwake {
				model.misses++
			}
		}
		model.observe(protocol, at, c.cfg)
	}

	if s.cfg.Mode == ModeGated && s.selected != nil && s.selected.lastEligible && !s.selected.lastAwake {
		s.invalidateAtLeast(s.selected.steadySleep())
		s.enterRecovery(s.lastEnd)
	}
	s.refreshQualifications()
	s.chooseCandidate()
}

func (m *senderModel) observe(protocol string, at time.Duration, cfg CandidateConfig) {
	if m.protocol != "" && m.protocol != protocol {
		m.history = m.history[:0]
		m.seen = false
		m.learned = false
	}
	m.protocol = protocol
	if m.seen {
		delta := at - m.last
		if delta > 0 {
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
		return
	}
	period, residuals := robustPeriod(m.history)
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
	m.preWake = scaleDuration(halfwidth, cfg.PreScale)
	m.postWake = scaleDuration(halfwidth, cfg.PostScale)
	m.learned = period > 0
}

func scaleDuration(value time.Duration, scale float64) time.Duration {
	if value <= 0 || scale <= 0 {
		return 0
	}
	return time.Duration(float64(value) * scale)
}

func (c *candidate) allLearned() bool {
	for _, sender := range c.senders {
		if !sender.learned {
			return false
		}
	}
	return true
}

func (c *candidate) awakeDuring(start, end time.Duration) bool {
	for _, sender := range c.senders {
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

func (s *Scheduler) checkWatchdogs(now time.Duration) {
	failed := false
	for _, sender := range s.senders {
		if sender.seen && sender.cfg.Overdue > 0 && now-sender.lastSeen > sender.cfg.Overdue {
			if !sender.overdueOpen {
				s.deadlineDeficits++
				sender.deadlineDeficits++
				sender.overdueOpen = true
				failed = true
			}
		}
		if sender.cfg.MinimumCount > 0 && now >= sender.cfg.CountWindow {
			lower := now - sender.cfg.CountWindow
			count := 0
			for _, observed := range sender.observations {
				if observed >= lower && observed <= now {
					count++
				}
			}
			deficit := count < sender.cfg.MinimumCount
			if deficit && !sender.countOpen {
				s.countDeficits++
				sender.countDeficits++
				sender.countOpen = true
				failed = true
			} else if !deficit {
				sender.countOpen = false
			}
		}
	}
	if failed && s.cfg.Mode == ModeGated && s.selected != nil && now >= s.recoveryUntil {
		selectedSkip := s.selected.steadySleep()
		s.invalidateAtLeast(selectedSkip)
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
}

func (s *Scheduler) invalidateAtLeast(skip float64) {
	for _, c := range s.candidates {
		if c.steadySleep() >= skip {
			c.invalid = true
			c.qualified = false
		}
	}
}

func (s *Scheduler) chooseCandidate() {
	if s.cfg.Mode != ModeGated || s.lastEnd < s.recoveryUntil {
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

func (s *Scheduler) qualifies(c *candidate) bool {
	if c.invalid || !c.allLearned() || c.eligibleDuration <= 0 {
		return false
	}
	maxMiss := 1 - s.cfg.CaptureTarget
	alpha := 1 - s.cfg.Confidence
	for _, sender := range c.senders {
		if sender.events == 0 || upperMissBound(sender.misses, sender.events, alpha) > maxMiss {
			return false
		}
	}
	return true
}

func (s *Scheduler) refreshQualifications() {
	for _, c := range s.candidates {
		c.qualified = s.qualifies(c)
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
	return c.steadySleep() * (1 - c.cfg.AuditFraction)
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
			return "QUALIFIED"
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
		ObservedSenders:  make(map[uint64]ObservedSenderSnapshot, len(s.senders)),
	}
	for id, sender := range s.senders {
		result.ObservedSenders[id] = ObservedSenderSnapshot{
			Protocol:         sender.protocol,
			Observations:     sender.total,
			LastSeenNS:       int64(sender.lastSeen),
			DeadlineDeficits: sender.deadlineDeficits,
			CountDeficits:    sender.countDeficits,
			OverdueOpen:      sender.overdueOpen,
			CountOpen:        sender.countOpen,
		}
	}
	alpha := 1 - s.cfg.Confidence
	for _, c := range s.candidates {
		candidateSnapshot := CandidateSnapshot{
			Name:                   c.cfg.Name,
			HistoryLimit:           c.cfg.HistoryLimit,
			Eligible:               c.allLearned(),
			Qualified:              c.qualified,
			Invalid:                c.invalid,
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
				UpperMissBound:          upperMissBound(sender.misses, sender.events, alpha),
				PeriodNS:                int64(sender.period),
				PreWakeNS:               int64(sender.preWake),
				PostWakeNS:              int64(sender.postWake),
			}
		}
		result.Candidates = append(result.Candidates, candidateSnapshot)
	}
	return result
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
