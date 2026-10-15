package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"fleetwatch/internal/proto"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func dialAgent(t *testing.T, c *tc, cred string) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, strings.Replace(c.srv.URL, "https://", "wss://", 1)+"/agent/ws", &websocket.DialOptions{
		HTTPClient: c.srv.Client(),
		HTTPHeader: http.Header{"Authorization": {"Bearer " + cred}},
	})
}

func agentSend(t *testing.T, ws *websocket.Conn, typ string, v any) {
	t.Helper()
	frame, _ := proto.Encode(typ, "", v)
	if err := ws.Write(context.Background(), websocket.MessageText, frame); err != nil {
		t.Fatal(err)
	}
}

func (c *tc) server(id string) map[string]any {
	_, b := c.do("GET", "/api/servers/"+id, nil)
	return b
}

func TestAgentPairingEndToEnd(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	c.do("POST", "/api/auth/login", creds)

	// Browser live stream, opened before the server exists.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	jar := c.c.Jar
	browser, _, err := websocket.Dial(ctx, strings.Replace(srv.URL, "https://", "wss://", 1)+"/ws", &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: srv.Client().Transport, Jar: jar},
		HTTPHeader: http.Header{"Origin": {srv.URL}},
	})
	if err != nil {
		t.Fatalf("browser ws: %v", err)
	}
	defer browser.CloseNow()
	_, snap, err := browser.Read(ctx)
	if err != nil || !strings.Contains(string(snap), `"type":"servers"`) {
		t.Fatalf("snapshot: %s %v", snap, err)
	}

	st, created := c.do("POST", "/api/servers", map[string]string{"name": "VPS-1", "provider": "Hetzner"})
	if st != 201 {
		t.Fatalf("create: %d %v", st, created)
	}
	enroll := created["enroll"].(map[string]any)
	token := enroll["token"].(string)
	id := created["server"].(map[string]any)["id"].(string)
	if !strings.Contains(enroll["installCommand"].(string), srv.URL+"/install.sh | sudo bash -s -- "+token) {
		t.Fatalf("install command: %v", enroll["installCommand"])
	}
	if s := c.server(id); s["status"] != "pending" {
		t.Fatalf("status before enroll: %v", s["status"])
	}

	// install.sh is public and points at this server.
	res, _ := c.c.Get(srv.URL + "/install.sh")
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "SERVER_URL='"+srv.URL+"'") {
		t.Fatalf("install.sh: %s", body)
	}

	post := func(tok string) (int, proto.EnrollResponse) {
		b, _ := json.Marshal(proto.EnrollRequest{Token: tok})
		res, err := c.c.Post(srv.URL+"/agent/enroll", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out proto.EnrollResponse
		json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	if st, _ := post("fwe_guess"); st != 401 {
		t.Fatalf("bad token: %d", st)
	}
	st, er := post(token)
	if st != 200 || er.ServerID != id {
		t.Fatalf("enroll: %d %+v", st, er)
	}
	if st, _ := post(token); st != 401 {
		t.Fatal("token accepted twice")
	}

	if _, res, err := dialAgent(t, c, "fwa.bad.cred"); err == nil || res.StatusCode != 401 {
		t.Fatal("bad credential connected")
	}
	ws, _, err := dialAgent(t, c, er.Credential)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	go func() { // agents keep a reader running (answers pings, receives config)
		for {
			if _, _, err := ws.Read(context.Background()); err != nil {
				return
			}
		}
	}()
	agentSend(t, ws, proto.TypeHello, proto.Hello{Protocol: proto.Protocol, AgentVersion: "test", Hostname: "vps1", OS: "Ubuntu 24.04", CPUCores: 2, MemTotal: 4 << 30, DiskTotal: 80 << 30})
	agentSend(t, ws, proto.TypeMetrics, proto.Metrics{CPU: 12.5, MemUsed: 1 << 30, MemTotal: 4 << 30, DiskUsed: 20 << 30, DiskTotal: 80 << 30, NetRxBps: 125000, Load1: 0.5, Uptime: 3600})

	waitFor(t, "server online", func() bool { return c.server(id)["status"] == "online" })
	s := c.server(id)
	if s["cpu"] != 12.5 || s["ram"] != 25.0 || s["disk"] != 25.0 || s["netRx"] != 1.0 || s["hostname"] != "vps1" || s["ramGb"] != 4.0 {
		t.Fatalf("view: %v", s)
	}
	// The browser received live updates for this server.
	gotLive := false
	for i := 0; i < 10 && !gotLive; i++ {
		_, msg, err := browser.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		gotLive = strings.Contains(string(msg), `"type":"server"`) && strings.Contains(string(msg), `"cpu":12.5`)
	}
	if !gotLive {
		t.Fatal("no live update for the browser")
	}

	if st, _ := c.do("GET", "/api/metrics?server="+id+"&range=bogus", nil); st != 400 {
		t.Fatalf("bad range: %d", st)
	}
	if st, _ := c.do("PATCH", "/api/servers/"+id, map[string]string{"name": ""}); st != 400 {
		t.Fatalf("empty rename: %d", st)
	}
	if st, b := c.do("PATCH", "/api/servers/"+id, map[string]string{"name": "Renamed"}); st != 200 || b["name"] != "Renamed" {
		t.Fatalf("rename: %d %v", st, b)
	}

	// Revoking cuts the live connection and the credential stops working.
	if st, _ := c.do("POST", "/api/servers/"+id+"/revoke", nil); st != 200 {
		t.Fatal("revoke failed")
	}
	waitFor(t, "disconnect", func() bool { return c.server(id)["status"] == "disconnected" })
	if _, res, err := dialAgent(t, c, er.Credential); err == nil || res.StatusCode != 401 {
		t.Fatal("revoked credential reconnected")
	}
	if st, _ := c.do("DELETE", "/api/servers/"+id, nil); st != 200 {
		t.Fatal("delete failed")
	}
	if st, _ := c.do("GET", "/api/servers/"+id, nil); st != 404 {
		t.Fatal("deleted server still served")
	}
}

func TestAgentEndpointsNeedNoSessionButAPIDoes(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	if st, _ := c.do("GET", "/api/servers", nil); st != 401 {
		t.Fatalf("servers without session: %d", st)
	}
	if st, _ := c.do("POST", "/api/servers", map[string]string{"name": "x"}); st != 401 {
		t.Fatalf("create without session: %d", st)
	}
	res, _ := c.c.Get(srv.URL + "/agent/download/windows-amd64")
	if res.StatusCode != 404 {
		t.Fatalf("unknown platform: %d", res.StatusCode)
	}
	res.Body.Close()
}

func TestValidHost(t *testing.T) {
	for h, want := range map[string]bool{"ops.example.com": true, "127.0.0.1:8090": true, "[::1]:8090": true, "": false, "a b": false, "x'y": false, "x\ny": false, "x$(id)": false} {
		if validHost(h) != want {
			t.Errorf("%q: want %v", h, want)
		}
	}
}

// pairAgent creates a server, enrolls a fake agent and completes the hello.
func pairAgent(t *testing.T, c *tc) (string, *websocket.Conn, <-chan proto.Envelope) {
	t.Helper()
	_, created := c.do("POST", "/api/servers", map[string]string{"name": "VPS-logs"})
	token := created["enroll"].(map[string]any)["token"].(string)
	b, _ := json.Marshal(proto.EnrollRequest{Token: token})
	res, err := c.c.Post(c.srv.URL+"/agent/enroll", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	var er proto.EnrollResponse
	json.NewDecoder(res.Body).Decode(&er)
	res.Body.Close()
	ws, _, err := dialAgent(t, c, er.Credential)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	frames := make(chan proto.Envelope, 64)
	go func() {
		for {
			_, data, err := ws.Read(context.Background())
			if err != nil {
				return
			}
			var env proto.Envelope
			if json.Unmarshal(data, &env) == nil {
				select {
				case frames <- env:
				default:
				}
			}
		}
	}()
	agentSend(t, ws, proto.TypeHello, proto.Hello{Protocol: proto.Protocol, AgentVersion: "test"})
	waitFor(t, "online", func() bool { return c.server(er.ServerID)["status"] == "online" })
	return er.ServerID, ws, frames
}

func TestLogsIngestSearchAndLive(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	c.do("POST", "/api/auth/login", creds)
	serverID, ws, _ := pairAgent(t, c)
	st, app := c.do("POST", "/api/apps", map[string]string{"serverId": serverID, "name": "API", "manager": "pm2", "processKey": "api"})
	if st != 201 {
		t.Fatalf("create app: %d %v", st, app)
	}
	appID := app["id"].(string)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	live, _, err := websocket.Dial(ctx, strings.Replace(srv.URL, "https://", "wss://", 1)+"/ws/logs?app="+appID, &websocket.DialOptions{
		HTTPClient: &http.Client{Transport: srv.Client().Transport, Jar: c.c.Jar},
		HTTPHeader: http.Header{"Origin": {srv.URL}},
	})
	if err != nil {
		t.Fatalf("live ws: %v", err)
	}
	defer live.CloseNow()

	agentSend(t, ws, proto.TypeLogs, []proto.LogLine{
		{TS: time.Now().UnixMilli(), AppID: appID, Source: "pm2", Stream: "stderr", Priority: -1, Message: "Error: connect ECONNREFUSED 127.0.0.1:5432"},
		{TS: time.Now().UnixMilli(), AppID: "app_of_someone_else", Source: "pm2", Stream: "stdout", Priority: -1, Message: "should be refused"},
	})
	_, msg, err := live.Read(ctx)
	if err != nil || !strings.Contains(string(msg), "ECONNREFUSED") || strings.Contains(string(msg), "refused\"") {
		t.Fatalf("live: %s %v", msg, err)
	}
	var page struct {
		Lines []map[string]any `json:"lines"`
	}
	waitFor(t, "search result", func() bool {
		req, _ := http.NewRequest("GET", srv.URL+"/api/logs?q=econnrefused&app="+appID, nil)
		res, err := c.c.Do(req)
		if err != nil {
			return false
		}
		defer res.Body.Close()
		json.NewDecoder(res.Body).Decode(&page)
		return len(page.Lines) == 1
	})
	if page.Lines[0]["severity"] != "error" || page.Lines[0]["serverId"] != serverID {
		t.Fatalf("line: %v", page.Lines[0])
	}
	if st, _ := c.do("GET", "/api/logs?q="+strings.Repeat("x", 300), nil); st != 400 {
		t.Fatalf("overlong query: %d", st)
	}
	if st, _ := c.do("PUT", "/api/settings/logs", map[string]int{"retentionDays": 5, "maxLineKB": 16}); st != 400 {
		t.Fatalf("bad retention accepted: %d", st)
	}
	if st, _ := c.do("PUT", "/api/settings/logs", map[string]int{"retentionDays": 14, "maxLineKB": 16}); st != 200 {
		t.Fatalf("valid retention: %d", st)
	}
}

func TestCommandsRequirePasswordAndReachAgent(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	c.do("POST", "/api/auth/login", creds)
	serverID, ws, frames := pairAgent(t, c)
	_, app := c.do("POST", "/api/apps", map[string]string{"serverId": serverID, "name": "API", "manager": "pm2", "processKey": "api", "path": "/var/www/api"})
	appID := app["id"].(string)

	def := map[string]any{"name": "Build", "command": "npm run build", "env": []string{"TOKEN=s3cr3t"}}
	if st, b := c.do("POST", "/api/apps/"+appID+"/commands", def); st != 400 || b["code"] != "wrong_password" {
		t.Fatalf("create without password: %d %v", st, b)
	}
	def["password"] = "admin-password-1"
	st, cmd := c.do("POST", "/api/apps/"+appID+"/commands", def)
	if st != 201 || cmd["env"].([]any)[0] != "TOKEN=••••••" {
		t.Fatalf("create: %d %v", st, cmd)
	}
	cmdID := cmd["id"].(string)
	// Editing with the masked value keeps the stored secret.
	def["name"], def["env"] = "Build app", []string{"TOKEN=••••••", "EXTRA=1"}
	if st, _ := c.do("PUT", "/api/commands/"+cmdID, def); st != 200 {
		t.Fatalf("update: %d", st)
	}

	// With re-authentication on, running needs the password.
	c.do("PUT", "/api/settings/security", map[string]any{"sessionTimeoutMin": 60, "lockoutAfter": 5, "reauthForCommands": true})
	if st, b := c.do("POST", "/api/apps/"+appID+"/commands/"+cmdID+"/run", nil); st != 403 || b["code"] != "password_required" {
		t.Fatalf("run without password: %d %v", st, b)
	}
	st, run := c.do("POST", "/api/apps/"+appID+"/commands/"+cmdID+"/run", map[string]string{"password": "admin-password-1"})
	if st != 202 {
		t.Fatalf("run: %d %v", st, run)
	}
	var exec proto.Exec
	for env := range frames {
		if env.Type == proto.TypeExec {
			json.Unmarshal(env.Data, &exec)
			break
		}
	}
	if exec.Command != "npm run build" || exec.Env[0] != "TOKEN=s3cr3t" || exec.Env[1] != "EXTRA=1" || exec.AppPath != "/var/www/api" {
		t.Fatalf("agent received: %+v", exec)
	}
	agentSend(t, ws, proto.TypeExecResult, proto.ExecResult{RunID: exec.RunID, Status: "failed", ExitCode: 2, RunAs: "deploy"})
	waitFor(t, "run finished", func() bool {
		_, r := c.do("GET", "/api/runs/"+exec.RunID, nil)
		return r["status"] == "failed" && r["exitCode"] == float64(2) && r["runAs"] == "deploy"
	})
	if st, _ := c.do("POST", "/api/apps/"+appID+"/commands/cmd_doesnotexist/run", map[string]string{"password": "admin-password-1"}); st != 404 {
		t.Fatalf("unknown command: %d", st)
	}
}
