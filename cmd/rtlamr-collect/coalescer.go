package main

import (
	"fmt"
	"reflect"
	"time"
)

// PointCoalescer suppresses unchanged R900 points between heartbeat
// intervals. Other protocols, including IDM's historical interval expansion,
// always pass through unchanged.
type PointCoalescer struct {
	interval time.Duration
	meters   map[pointKey]pointState
}

type pointKey struct {
	protocol     string
	messageType  string
	endpointType string
	endpointID   string
}

type pointState struct {
	fields      map[string]interface{}
	lastEmitted time.Time
}

func NewPointCoalescer(interval time.Duration) *PointCoalescer {
	return &PointCoalescer{
		interval: interval,
		meters:   make(map[pointKey]pointState),
	}
}

func parseUnchangedInterval(value string) (time.Duration, error) {
	if value == "" || value == "0" {
		return 0, nil
	}

	interval, err := time.ParseDuration(value)
	if err != nil {
		return 0, err
	}
	if interval < 0 {
		return 0, fmt.Errorf("must not be negative")
	}
	return interval, nil
}

// ShouldEmit returns true for the first observation, every changed
// observation, and the first unchanged observation at or after the heartbeat
// interval. A timestamp that does not advance fails open and starts a new
// interval.
func (c *PointCoalescer) ShouldEmit(t time.Time, tags map[string]string, fields map[string]interface{}) bool {
	if c == nil || c.interval == 0 || !isR900(tags["protocol"]) {
		return true
	}

	key := pointKey{
		protocol:     tags["protocol"],
		messageType:  tags["msg_type"],
		endpointType: tags["endpoint_type"],
		endpointID:   tags["endpoint_id"],
	}
	previous, seen := c.meters[key]
	if seen && t.After(previous.lastEmitted) && reflect.DeepEqual(fields, previous.fields) && t.Sub(previous.lastEmitted) < c.interval {
		return false
	}

	c.meters[key] = pointState{
		fields:      cloneFields(fields),
		lastEmitted: t,
	}
	return true
}

func isR900(protocol string) bool {
	return protocol == "R900" || protocol == "R900BCD"
}

func cloneFields(fields map[string]interface{}) map[string]interface{} {
	clone := make(map[string]interface{}, len(fields))
	for key, value := range fields {
		clone[key] = value
	}
	return clone
}
