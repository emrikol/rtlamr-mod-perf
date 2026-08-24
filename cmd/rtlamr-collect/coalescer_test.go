package main

import (
	"testing"
	"time"
)

const (
	testElectricID = "1001001"
	testGasID      = "2002002"
	testWaterID    = "3003003"
)

func TestParseUnchangedInterval(t *testing.T) {
	tests := []struct {
		value string
		want  time.Duration
		ok    bool
	}{
		{"", 0, true},
		{"0", 0, true},
		{"60s", time.Minute, true},
		{"1m", time.Minute, true},
		{"-1s", 0, false},
		{"sixty", 0, false},
	}

	for _, test := range tests {
		got, err := parseUnchangedInterval(test.value)
		if (err == nil) != test.ok || got != test.want {
			t.Errorf("parseUnchangedInterval(%q) = %s, %v; want %s, ok=%t", test.value, got, err, test.want, test.ok)
		}
	}
}

func TestPointCoalescerChangeOrHeartbeat(t *testing.T) {
	c := NewPointCoalescer(time.Minute)
	t0 := time.Unix(1_000, 0)
	tags := r900Tags(testGasID)
	steady := r900Fields(100)

	assertEmit(t, c, t0, tags, steady, true, "first observation")
	assertEmit(t, c, t0.Add(14*time.Second), tags, steady, false, "early duplicate")
	assertEmit(t, c, t0.Add(59*time.Second), tags, steady, false, "last pre-heartbeat duplicate")
	assertEmit(t, c, t0.Add(60*time.Second), tags, steady, true, "heartbeat boundary")
	assertEmit(t, c, t0.Add(74*time.Second), tags, steady, false, "heartbeat resets interval")

	changed := r900Fields(101)
	assertEmit(t, c, t0.Add(75*time.Second), tags, changed, true, "change emits immediately")
	assertEmit(t, c, t0.Add(134*time.Second), tags, changed, false, "change resets interval")
	assertEmit(t, c, t0.Add(135*time.Second), tags, changed, true, "post-change heartbeat")
}

func TestPointCoalescerSeparatesMetersAndProtocols(t *testing.T) {
	c := NewPointCoalescer(time.Minute)
	t0 := time.Unix(1_000, 0)
	fields := r900Fields(100)

	assertEmit(t, c, t0, r900Tags(testGasID), fields, true, "first gas")
	assertEmit(t, c, t0.Add(time.Second), r900Tags(testWaterID), fields, true, "first water")

	r900bcd := r900Tags(testGasID)
	r900bcd["protocol"] = "R900BCD"
	assertEmit(t, c, t0.Add(2*time.Second), r900bcd, fields, true, "separate protocol")

	idm := r900Tags(testElectricID)
	idm["protocol"] = "IDM"
	for i := 0; i < 2; i++ {
		assertEmit(t, c, t0.Add(time.Duration(i)*time.Second), idm, fields, true, "IDM always passes")
	}
}

func TestPointCoalescerDisabled(t *testing.T) {
	c := NewPointCoalescer(0)
	t0 := time.Unix(1_000, 0)
	tags := r900Tags(testGasID)
	fields := r900Fields(100)

	assertEmit(t, c, t0, tags, fields, true, "first")
	assertEmit(t, c, t0, tags, fields, true, "disabled duplicate")
}

func TestPointCoalescerTimestampRegressionFailsOpen(t *testing.T) {
	c := NewPointCoalescer(time.Minute)
	t0 := time.Unix(1_000, 0)
	tags := r900Tags(testGasID)
	fields := r900Fields(100)

	assertEmit(t, c, t0, tags, fields, true, "first")
	assertEmit(t, c, t0.Add(-time.Second), tags, fields, true, "clock regression")
	assertEmit(t, c, t0, tags, fields, false, "regression resets interval")
}

func TestPointCoalescerCopiesFields(t *testing.T) {
	c := NewPointCoalescer(time.Minute)
	t0 := time.Unix(1_000, 0)
	tags := r900Tags(testGasID)
	fields := r900Fields(100)

	assertEmit(t, c, t0, tags, fields, true, "first")
	fields["consumption"] = int64(101)
	assertEmit(t, c, t0.Add(time.Second), tags, fields, true, "caller mutation is a change")
}

func TestR900MappedFieldsDriveCoalescer(t *testing.T) {
	c := NewPointCoalescer(time.Minute)
	t0 := time.Unix(1_000, 0)
	msg := LogMessage{Time: t0, Type: "R900"}
	r900 := R900{
		EndpointID:   2_002_002,
		EndpointType: 2,
		Consumption:  100,
		NoUse:        1,
		BackFlow:     2,
		Leak:         3,
		LeakNow:      4,
	}

	emit := func(at time.Time, value R900) bool {
		emitted := false
		msg.Time = at
		value.AddPoints(msg, func(t time.Time, tags map[string]string, fields map[string]interface{}) {
			emitted = c.ShouldEmit(t, tags, fields)
		})
		return emitted
	}

	if !emit(t0, r900) {
		t.Fatal("first mapped point was suppressed")
	}
	if emit(t0.Add(14*time.Second), r900) {
		t.Fatal("unchanged mapped point was emitted early")
	}
	r900.LeakNow++
	if !emit(t0.Add(15*time.Second), r900) {
		t.Fatal("mapped field change was suppressed")
	}
}

func assertEmit(t *testing.T, c *PointCoalescer, at time.Time, tags map[string]string, fields map[string]interface{}, want bool, label string) {
	t.Helper()
	if got := c.ShouldEmit(at, tags, fields); got != want {
		t.Fatalf("%s: ShouldEmit() = %t, want %t", label, got, want)
	}
}

func r900Tags(endpointID string) map[string]string {
	return map[string]string{
		"protocol":      "R900",
		"msg_type":      "cumulative",
		"endpoint_type": "2",
		"endpoint_id":   endpointID,
	}
}

func r900Fields(consumption int64) map[string]interface{} {
	return map[string]interface{}{
		"consumption": consumption,
		"nouse":       int64(0),
		"backflow":    int64(0),
		"leak":        int64(0),
		"leak_now":    int64(0),
	}
}
