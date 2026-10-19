package main

import (
	"encoding/json"
	"fmt"
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
