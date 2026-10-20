package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"fleetwatch/internal/server/incidents"
)

func TestAcknowledgedBy(t *testing.T) {
	if _, who := acknowledged(incidents.Incident{}); who != "" {
		t.Fatal("found an acknowledgement in an empty timeline")
	}
	at := time.Now().Add(-25 * time.Minute).UnixMilli()
	var x incidents.Incident
	json.Unmarshal([]byte(fmt.Sprintf(`{"timeline":[{"ts":1,"text":"Opened"},{"ts":%d,"text":"Acknowledged by Nour (on call)"}]}`, at)), &x)
	if when, who := acknowledged(x); who != "Nour" || when.UnixMilli() != at {
		t.Fatal(when, who)
	}
}

func TestBlockedSummary(t *testing.T) {
	if s := blockedSummary([]string{"192.0.2.1"}); !strings.Contains(s, "1 address(es)") || !strings.Contains(s, "192.0.2.1") {
		t.Fatal(s)
	}
	var many []string
	for i := range 13 {
		many = append(many, fmt.Sprintf("192.0.2.%d", i))
	}
	s := blockedSummary(many)
	if !strings.Contains(s, "13 address(es)") || !strings.Contains(s, "and 3 more") || strings.Contains(s, "192.0.2.2,") {
		t.Fatal(s)
	}
}
