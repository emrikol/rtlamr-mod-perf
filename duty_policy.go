package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/bemasher/rtlamr/internal/dutyscheduler"
)

const dutyPolicySchema = "rtlamr-duty-scheduler-policy-v1"
const dutyPolicyMaxBytes = 1 << 20

type dutyPolicyDocument struct {
	Schema               string             `json:"schema"`
	Confidence           *float64           `json:"confidence,omitempty"`
	MinimumAudit         *float64           `json:"minimum_audit,omitempty"`
	RecoveryDuration     string             `json:"recovery_duration,omitempty"`
	RefreshInterval      string             `json:"refresh_interval,omitempty"`
	RefreshDuration      string             `json:"refresh_duration,omitempty"`
	PromotionMargin      *float64           `json:"promotion_margin,omitempty"`
	PromotionStability   string             `json:"promotion_stability,omitempty"`
	WatchdogHistory      *int               `json:"watchdog_history,omitempty"`
	WatchdogMinIntervals *int               `json:"watchdog_min_intervals,omitempty"`
	WatchdogWindow       string             `json:"watchdog_window,omitempty"`
	WatchdogQuantile     *float64           `json:"watchdog_quantile,omitempty"`
	WatchdogMargin       *float64           `json:"watchdog_margin,omitempty"`
	Senders              []dutySenderPolicy `json:"senders,omitempty"`
}

type dutySenderPolicy struct {
	ID           uint64 `json:"id"`
	Overdue      string `json:"overdue,omitempty"`
	CountWindow  string `json:"count_window,omitempty"`
	MinimumCount int    `json:"minimum_count,omitempty"`
}

func parseOptionalDuration(label, value string, target *time.Duration) error {
	if value == "" {
		return nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fmt.Errorf("dutyscheduler: invalid %s", label)
	}
	*target = parsed
	return nil
}

func loadDutySchedulerConfig(mode dutyscheduler.Mode, ids []uint64, captureTarget float64, path string) (dutyscheduler.Config, error) {
	config := dutyscheduler.DefaultConfig(mode, ids)
	config.CaptureTarget = captureTarget
	if path == "" {
		return config, nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return dutyscheduler.Config{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || info.Size() <= 0 || info.Size() > dutyPolicyMaxBytes {
		return dutyscheduler.Config{}, errors.New("dutyscheduler: invalid policy file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return dutyscheduler.Config{}, err
	}
	var policy dutyPolicyDocument
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return dutyscheduler.Config{}, fmt.Errorf("dutyscheduler: decode policy: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return dutyscheduler.Config{}, errors.New("dutyscheduler: policy contains trailing data")
	}
	if policy.Schema != dutyPolicySchema {
		return dutyscheduler.Config{}, fmt.Errorf("dutyscheduler: unsupported policy schema %q", policy.Schema)
	}
	if policy.Confidence != nil {
		config.Confidence = *policy.Confidence
	}
	if policy.MinimumAudit != nil {
		config.MinimumAudit = *policy.MinimumAudit
		for idx := range config.Candidates {
			if config.Candidates[idx].AuditFraction < config.MinimumAudit {
				config.Candidates[idx].AuditFraction = config.MinimumAudit
			}
		}
	}
	if policy.PromotionMargin != nil {
		config.PromotionMargin = *policy.PromotionMargin
	}
	if policy.WatchdogHistory != nil {
		config.WatchdogHistory = *policy.WatchdogHistory
	}
	if policy.WatchdogMinIntervals != nil {
		config.WatchdogMinIntervals = *policy.WatchdogMinIntervals
	}
	if policy.WatchdogQuantile != nil {
		config.WatchdogQuantile = *policy.WatchdogQuantile
	}
	if policy.WatchdogMargin != nil {
		config.WatchdogMargin = *policy.WatchdogMargin
	}
	if err := parseOptionalDuration("recovery duration", policy.RecoveryDuration, &config.RecoveryDuration); err != nil {
		return dutyscheduler.Config{}, err
	}
	if err := parseOptionalDuration("refresh interval", policy.RefreshInterval, &config.RefreshInterval); err != nil {
		return dutyscheduler.Config{}, err
	}
	if err := parseOptionalDuration("refresh duration", policy.RefreshDuration, &config.RefreshDuration); err != nil {
		return dutyscheduler.Config{}, err
	}
	if err := parseOptionalDuration("promotion stability", policy.PromotionStability, &config.PromotionStability); err != nil {
		return dutyscheduler.Config{}, err
	}
	if err := parseOptionalDuration("watchdog window", policy.WatchdogWindow, &config.WatchdogWindow); err != nil {
		return dutyscheduler.Config{}, err
	}
	configured := make(map[uint64]int, len(config.Senders))
	for idx, sender := range config.Senders {
		configured[sender.ID] = idx
	}
	seen := make(map[uint64]bool, len(policy.Senders))
	for _, senderPolicy := range policy.Senders {
		idx, ok := configured[senderPolicy.ID]
		if !ok || seen[senderPolicy.ID] || senderPolicy.MinimumCount < 0 {
			return dutyscheduler.Config{}, errors.New("dutyscheduler: invalid policy sender inventory")
		}
		seen[senderPolicy.ID] = true
		sender := &config.Senders[idx]
		if err := parseOptionalDuration("sender overdue", senderPolicy.Overdue, &sender.Overdue); err != nil {
			return dutyscheduler.Config{}, err
		}
		if err := parseOptionalDuration("sender count window", senderPolicy.CountWindow, &sender.CountWindow); err != nil {
			return dutyscheduler.Config{}, err
		}
		sender.MinimumCount = senderPolicy.MinimumCount
	}
	if _, err := dutyscheduler.New(config); err != nil {
		return dutyscheduler.Config{}, err
	}
	return config, nil
}
