package docker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/live"
)

func TestActAndResults(t *testing.T) {
	s := New(live.NewBus())
	var sent []proto.DockerAction
	s.Send = func(serverID, typ string, v any) error {
		if a, ok := v.(proto.DockerAction); ok {
			sent = append(sent, a)
			if a.Action == "logs" { // the agent answers right away
				raw, _ := json.Marshal(proto.DockerResult{RunID: a.RunID, OK: true, Output: "hello"})
				go s.OnResult(serverID, raw)
			}
		}
		return nil
	}
	s.AgentVersion = func(string) string { return "v0.9.38" }
	for _, a := range []proto.DockerAction{{Kind: "container", Action: "explode", Target: "x"}, {Kind: "volume", Action: "remove"}, {Kind: "host", Action: "stop", Target: "x"}} {
		if _, err := s.Act("s1", a); err == nil {
			t.Fatalf("accepted %+v", a)
		}
	}
	id, err := s.Act("s1", proto.DockerAction{Kind: "container", Action: "restart", Target: "web"})
	if err != nil || len(s.State("s1").Running) != 1 {
		t.Fatal(err, s.State("s1"))
	}
	raw, _ := json.Marshal(proto.DockerResult{RunID: id, OK: true})
	s.OnResult("s1", raw)
	if len(s.State("s1").Running) != 0 {
		t.Fatal("finished action still running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := s.Logs(ctx, "s1", "web")
	if err != nil || res.Output != "hello" {
		t.Fatal(res, err)
	}
	s.AgentVersion = func(string) string { return "v0.9.30" }
	if _, err := s.Act("s1", proto.DockerAction{Kind: "container", Action: "start", Target: "web"}); err != ErrOldAgent {
		t.Fatal("old agent accepted")
	}
}
