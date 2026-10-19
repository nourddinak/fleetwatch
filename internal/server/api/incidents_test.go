package api

import (
	"encoding/json"
	"strings"
	"testing"

	"fleetwatch/internal/proto"
)

func TestIncidentLifecycleAndChannels(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	c.do("POST", "/api/auth/login", creds)
	serverID, ws, _ := pairAgent(t, c)
	_, app := c.do("POST", "/api/apps", map[string]string{"serverId": serverID, "name": "API", "manager": "pm2", "processKey": "api"})
	inv := func(status string) {
		agentSend(t, ws, proto.TypeInventory, proto.Inventory{Processes: []proto.Process{{Manager: "pm2", Key: "api", Status: status, RawStatus: status, Likely: true}}})
	}
	inv("online")
	inv("errored")
	var open []map[string]any
	waitFor(t, "incident opened", func() bool {
		open = c.list("/api/incidents?status=open")
		return len(open) == 1
	})
	x := open[0]
	if x["typeKey"] != "pm2_errored" || x["appId"] != app["id"] || x["severity"] != "critical" {
		t.Fatalf("incident: %v", x)
	}
	id := x["id"].(string)
	if st, y := c.do("POST", "/api/incidents/"+id+"/acknowledge", nil); st != 200 || y["status"] != "acknowledged" {
		t.Fatalf("acknowledge: %d %v", st, y)
	}
	if st, _ := c.do("POST", "/api/incidents/"+id+"/explode", nil); st != 400 {
		t.Fatalf("unknown action: %d", st)
	}
	if st, _ := c.do("POST", "/api/incidents/inc_missing/resolve", nil); st != 404 {
		t.Fatalf("missing incident: %d", st)
	}
	inv("online")
	waitFor(t, "auto-resolve", func() bool {
		_, y := c.do("GET", "/api/incidents/"+id, nil)
		return y["status"] == "resolved" && y["resolvedBy"] == "auto"
	})

	if st, _ := c.do("PUT", "/api/notifications/channels/telegram", map[string]any{"enabled": true, "fields": map[string]string{"botToken": "123:supersecret", "chatId": "42"}}); st != 200 {
		t.Fatalf("save channel: %d", st)
	}
	raw, _ := json.Marshal(c.list("/api/notifications/channels"))
	if strings.Contains(string(raw), "supersecret") || !strings.Contains(string(raw), `"botToken":true`) {
		t.Fatalf("channels: %s", raw)
	}
	if st, _ := c.do("PUT", "/api/notifications/channels/pagerduty", map[string]any{"enabled": true}); st != 404 {
		t.Fatalf("unknown provider: %d", st)
	}
	if st, _ := c.do("PUT", "/api/settings/incident-rules", map[string]int{"restartThreshold": 0}); st != 400 {
		t.Fatalf("invalid rules accepted: %d", st)
	}
}
