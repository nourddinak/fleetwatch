package commands

import (
	"database/sql"
	"errors"
	"strings"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/logs"
	"fleetwatch/internal/server/valid"
)

const runCols = `id, app_id, command_id, name, preview, cwd, run_as, started_at, COALESCE(finished_at, 0), status, exit_code, stdout, stderr, error, initiated_by`

func scanRun(sc interface{ Scan(...any) error }) (Run, error) {
	var r Run
	var exit sql.NullInt64
	err := sc.Scan(&r.ID, &r.AppID, &r.CommandID, &r.Name, &r.Preview, &r.Cwd, &r.RunAs, &r.StartedAt, &r.FinishedAt, &r.Status, &exit, &r.Stdout, &r.Stderr, &r.Error, &r.InitiatedBy)
	if exit.Valid {
		n := int(exit.Int64)
		r.ExitCode = &n
	}
	return r, err
}

// Start launches a command on the app's server. initiatedBy is recorded with the run.
func (s *Service) Start(appID, commandID, initiatedBy string) (Run, error) {
	app, err := s.Apps.Get(appID)
	if err != nil {
		return Run{}, err
	}
	mgr := managerKey(app.Manager)
	req := proto.Exec{AppID: appID, Manager: mgr, Key: app.ProcessKey, AppPath: app.Path}
	run := Run{AppID: appID, CommandID: commandID, StartedAt: s.Now().UnixMilli(), Status: "running", InitiatedBy: initiatedBy}
	if action, ok := strings.CutPrefix(commandID, "builtin:"); ok {
		found := false
		for _, b := range builtins {
			found = found || b.ID == commandID
		}
		if !found || mgr == "standalone" {
			return Run{}, ErrNotFound
		}
		req.Kind, req.Action, req.TimeoutSec = "builtin", action, 90
		run.Name = strings.ToUpper(action[:1]) + action[1:]
		run.Preview = builtinPreview(mgr, app.ProcessKey, action)
	} else {
		c, err := s.get(commandID)
		if err != nil || c.AppID != appID {
			return Run{}, ErrNotFound
		}
		if app.Path == "" {
			return Run{}, valid.Error("set the application's working directory before running custom commands")
		}
		req.Kind, req.Command, req.Cwd, req.Env, req.TimeoutSec = "custom", c.Command, c.Cwd, c.Env, c.TimeoutSec
		run.Name, run.Preview, run.Cwd = c.Name, c.Command, strings.TrimSuffix(app.Path+"/"+c.Cwd, "/")
	}
	run.ID = newID("run_")
	run.serverID = app.ServerID
	req.RunID = run.ID
	_, err = s.db.Exec(`INSERT INTO command_runs (id, app_id, command_id, name, preview, cwd, started_at, status, initiated_by) VALUES (?, ?, ?, ?, ?, ?, ?, 'running', ?)`,
		run.ID, appID, commandID, run.Name, run.Preview, run.Cwd, run.StartedAt, initiatedBy)
	if err != nil {
		return Run{}, err
	}
	s.mu.Lock()
	s.running[run.ID] = &run
	s.mu.Unlock()
	if err := s.Send(app.ServerID, proto.TypeExec, req); err != nil {
		s.finish(run.ID, proto.ExecResult{RunID: run.ID, Status: "error", ExitCode: -1, Error: ErrOffline.Error()})
		return Run{}, ErrOffline
	}
	s.Bus.Publish("run", run)
	return run, nil
}

// Output appends a chunk from the agent and forwards it to browsers.
func (s *Service) Output(serverID string, o proto.ExecOutput) {
	o.Data = logs.SanitizeOutput(o.Data) // no terminal escapes reach the browser
	s.mu.Lock()
	r := s.running[o.RunID]
	if r == nil || r.serverID != serverID {
		s.mu.Unlock()
		return // unknown run, or another server's agent
	}
	target := &r.Stdout
	if o.Stream == "stderr" {
		target = &r.Stderr
	}
	if room := maxStoredOutput - len(*target); room > 0 {
		chunk := o.Data
		if len(chunk) > room {
			chunk = chunk[:room] + "\n[stored output truncated]\n"
		}
		*target += chunk
	}
	s.mu.Unlock()
	s.Bus.Publish("run.output", map[string]string{"runId": o.RunID, "appId": r.AppID, "stream": o.Stream, "data": o.Data})
}

// Result records the end of a run.
func (s *Service) Result(serverID string, res proto.ExecResult) {
	s.mu.Lock()
	r := s.running[res.RunID]
	ok := r != nil && r.serverID == serverID
	s.mu.Unlock()
	if ok {
		s.finish(res.RunID, res)
	}
}

func (s *Service) finish(runID string, res proto.ExecResult) {
	s.mu.Lock()
	r := s.running[runID]
	delete(s.running, runID)
	s.mu.Unlock()
	if r == nil {
		return
	}
	switch res.Status {
	case "succeeded", "failed", "timeout", "refused", "error":
	default:
		res.Status = "error"
	}
	r.Status, r.FinishedAt, r.Error = res.Status, s.Now().UnixMilli(), res.Error
	exit := res.ExitCode
	r.ExitCode = &exit
	if res.RunAs != "" {
		r.RunAs = res.RunAs
	}
	s.db.Exec(`UPDATE command_runs SET status = ?, finished_at = ?, exit_code = ?, stdout = ?, stderr = ?, error = ?, run_as = ? WHERE id = ?`,
		r.Status, r.FinishedAt, exit, r.Stdout, r.Stderr, r.Error, r.RunAs, runID)
	s.Bus.Publish("run", r)
}

// AgentGone fails the server's running commands when its agent disconnects.
func (s *Service) AgentGone(serverID string) {
	s.mu.Lock()
	var ids []string
	for id, r := range s.running {
		if r.serverID == serverID {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.finish(id, proto.ExecResult{Status: "error", ExitCode: -1, Error: "agent disconnected before the command finished; it may still have completed on the server"})
	}
}

// History returns an app's runs, newest first.
func (s *Service) History(appID string, limit int) ([]Run, error) {
	rows, err := s.db.Query(`SELECT `+runCols+` FROM command_runs WHERE app_id = ? ORDER BY started_at DESC LIMIT ?`, appID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		s.overlayLive(&r)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) GetRun(id string) (Run, error) {
	r, err := scanRun(s.db.QueryRow(`SELECT `+runCols+` FROM command_runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	s.overlayLive(&r)
	return r, err
}

// overlayLive fills in output collected so far for a run still in progress.
func (s *Service) overlayLive(r *Run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if live := s.running[r.ID]; live != nil {
		r.Stdout, r.Stderr = live.Stdout, live.Stderr
	}
}

func (s *Service) lastRuns(appID string) map[string]*Run {
	out := map[string]*Run{}
	rows, err := s.db.Query(`SELECT `+runCols+` FROM command_runs WHERE app_id = ? AND started_at = (
		SELECT MAX(started_at) FROM command_runs c2 WHERE c2.app_id = command_runs.app_id AND c2.command_id = command_runs.command_id)`, appID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		if r, err := scanRun(rows); err == nil {
			r.Stdout, r.Stderr = "", ""
			out[r.CommandID] = &r
		}
	}
	return out
}
