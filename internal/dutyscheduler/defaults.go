package dutyscheduler

import (
	"fmt"
	"time"
)

func defaultCandidate(name string, history int, r900Floor, otherFloor time.Duration, quantile float64, margin time.Duration) CandidateConfig {
	return CandidateConfig{
		Name:             name,
		HistoryLimit:     history,
		MinHistoryLimit:  min(8, history),
		WarmupIntervals:  4,
		R900Floor:        r900Floor,
		OtherFloor:       otherFloor,
		JitterQuantile:   quantile,
		JitterMargin:     margin,
		PreScale:         1,
		PostScale:        1,
		AuditFraction:    0.10,
		ChangePointScale: 2,
		HistoryGrowAfter: 8,
		WakeScaleStep:    1.25,
		MaxWakeScale:     4,
	}
}

// DefaultCandidates is the candidate-blind bank used by offline replay and
// the live shadow process. Keeping it in one package prevents experiments from
// silently testing a different controller than production.
func DefaultCandidates() []CandidateConfig {
	var result []CandidateConfig
	for _, history := range []int{8, 16, 32, 64, 128} {
		result = append(result, defaultCandidate(fmt.Sprintf("balanced-n%d", history), history, 500*time.Millisecond, 5*time.Second, 0.95, 500*time.Millisecond))
	}
	result = append(result,
		defaultCandidate("tight-n64", 64, 250*time.Millisecond, 3*time.Second, 0.90, 250*time.Millisecond),
		defaultCandidate("conservative-n64", 64, time.Second, 8*time.Second, 0.99, time.Second),
	)
	return result
}

// DefaultSenderConfig returns the site-independent safe seed. The public
// default deliberately leaves static sender count/deadline thresholds disabled;
// learned cadence, qualification, audit, and fail-open recovery remain active.
func DefaultSenderConfig(id uint64) SenderConfig {
	return SenderConfig{ID: id}
}

func DefaultConfig(mode Mode, senderIDs []uint64) Config {
	senders := make([]SenderConfig, 0, len(senderIDs))
	for _, id := range senderIDs {
		senders = append(senders, DefaultSenderConfig(id))
	}
	return Config{
		Mode:                 mode,
		Senders:              senders,
		Candidates:           DefaultCandidates(),
		CaptureTarget:        0.995,
		Confidence:           0.95,
		MinimumAudit:         0.10,
		RecoveryDuration:     10 * time.Minute,
		RefreshInterval:      6 * time.Hour,
		RefreshDuration:      10 * time.Minute,
		PromotionMargin:      0.00025,
		PromotionStability:   10 * time.Minute,
		WatchdogHistory:      128,
		WatchdogMinIntervals: 16,
		WatchdogWindow:       10 * time.Minute,
		WatchdogQuantile:     0.99,
		WatchdogMargin:       0.25,
		RandomSeed:           0x72746c616d72,
	}
}
