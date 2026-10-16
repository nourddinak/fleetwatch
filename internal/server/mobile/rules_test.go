package mobile

import (
	"testing"
	"time"
)

func TestAlertRules(t *testing.T) {
	r := AlertRules{MinSeverity: "warning", Servers: []string{"s1"}, QuietFrom: "22:00", QuietTo: "07:00", TimeZone: "Europe/Paris"}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)    // 14:00 in Paris
	night := time.Date(2026, 10, 16, 23, 30, 0, 0, time.UTC) // 01:30 in Paris
	cases := []struct {
		m    Message
		at   time.Time
		want bool
	}{
		{Message{Kind: "incident", Severity: "critical", ServerID: "s1"}, day, true},
		{Message{Kind: "incident", Severity: "info", ServerID: "s1"}, day, false},
		{Message{Kind: "resolved", Severity: "ok", ServerID: "s1"}, day, true},
		{Message{Kind: "incident", Severity: "critical", ServerID: "s2"}, day, false},
		{Message{Kind: "incident", Severity: "critical", ServerID: "s1"}, night, false},
	}
	for i, c := range cases {
		if got := r.allows(c.m, c.at); got != c.want {
			t.Errorf("case %d: %v", i, got)
		}
	}
	for _, bad := range []AlertRules{{MinSeverity: "ok", TimeZone: "UTC"}, {MinSeverity: "info", QuietFrom: "22:00", TimeZone: "UTC"}, {MinSeverity: "info", QuietFrom: "25:00", QuietTo: "07:00", TimeZone: "UTC"}, {MinSeverity: "info", TimeZone: "Mars/Base"}} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
