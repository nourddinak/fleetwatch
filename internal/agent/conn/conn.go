// Package conn keeps the agent's outbound WebSocket to ops-server alive.
package conn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"fleetwatch/internal/agent/backup"
	"fleetwatch/internal/agent/caddy"
	"fleetwatch/internal/agent/config"
	"fleetwatch/internal/agent/dbtool"
	"fleetwatch/internal/agent/docker"
	"fleetwatch/internal/agent/envfile"
	agentexec "fleetwatch/internal/agent/exec"
	"fleetwatch/internal/agent/health"
	"fleetwatch/internal/agent/host"
	"fleetwatch/internal/agent/hostcheck"
	"fleetwatch/internal/agent/inventory"
	"fleetwatch/internal/agent/logs"
	"fleetwatch/internal/agent/sites"
	"fleetwatch/internal/agent/storage"
	"fleetwatch/internal/proto"
	"fleetwatch/internal/selfupdate"
)

var (
	// ErrRestart asks the caller to exit so systemd restarts the agent.
	ErrRestart        = errors.New("restart requested by server")
	errUnauthorized   = errors.New("server rejected the agent credential (revoked? re-run install.sh with a new token)")
	maxBackoff        = time.Minute
	unauthorizedRetry = 5 * time.Minute
)

type Runner struct {
	Cfg         config.Config
	Version     string
	StatePath   string       // log offsets and journal cursor
	interval    atomic.Int64 // metrics, seconds
	invEvery    atomic.Int64 // inventory, seconds
	roots       atomic.Pointer[[]string]
	sampler     host.Sampler
	collector   *inventory.Collector
	logs        *logs.Manager
	exec        *agentexec.Runner
	health      *health.Scheduler
	storage     *storage.Scanner
	scanNow     chan struct{}
	cur         atomic.Pointer[websocket.Conn] // live connection, nil while disconnected
	loginsSince atomic.Int64                   // newest SSH login sent (unix ms)
	blocks      atomic.Pointer[string]         // the block list last applied ("" = none yet)
	blockMu     sync.Mutex
}

// Run connects and reconnects until ctx ends or the server requests a restart.
func (r *Runner) Run(ctx context.Context) error {
	r.interval.Store(15)
	r.invEvery.Store(30)
	r.roots.Store(&[]string{"/var/www"})
	r.collector = inventory.NewCollector()
	r.logs = logs.NewManager(r.StatePath, r.collector.LogFiles)
	// Log collection outlives connections: lines buffer while disconnected.
	go r.logs.Run(ctx)
	r.scanNow = make(chan struct{}, 1)
	r.health = health.NewScheduler(ctx)
	r.storage = storage.NewScanner()
	go r.storage.Run(ctx)
	r.exec = &agentexec.Runner{AllowCustom: r.Cfg.AllowCustomCommands, Lookup: r.collector.Process, Send: r.sendCurrent, Rescan: r.rescan}
	backoff := time.Second
	for {
		start := time.Now()
		err := r.session(ctx)
		if errors.Is(err, ErrRestart) {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second // the connection was healthy; reconnect quickly
		}
		wait := backoff + time.Duration(rand.Int64N(int64(backoff/2)+1))
		if errors.Is(err, errUnauthorized) {
			wait = unauthorizedRetry
		}
		slog.Warn("disconnected from server", "err", err, "retry_in", wait.Round(time.Second))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

func wsURL(serverURL string) string {
	if rest, ok := strings.CutPrefix(serverURL, "https://"); ok {
		return "wss://" + rest + "/agent/ws"
	}
	return "ws://" + strings.TrimPrefix(serverURL, "http://") + "/agent/ws"
}

func (r *Runner) session(ctx context.Context) error {
	rootCtx := ctx
	dialCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	c, resp, err := websocket.Dial(dialCtx, wsURL(r.Cfg.ServerURL), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + r.Cfg.Credential}, "User-Agent": {"ops-agent/" + r.Version}},
	})
	cancel()
	if resp != nil && resp.StatusCode == http.StatusUnauthorized {
		return errUnauthorized
	}
	if err != nil {
		return err
	}
	defer c.CloseNow()
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)

	if err := send(ctx, c, proto.TypeHello, host.Hello(r.Version)); err != nil {
		return err
	}
	slog.Info("connected to server", "url", r.Cfg.ServerURL)

	r.cur.Store(c)
	defer r.cur.CompareAndSwap(c, nil)
	go r.inventoryLoop(ctx, c, r.scanNow)
	go r.logLoop(ctx, c)
	go r.resultLoop(ctx, c)
	go r.loginLoop(ctx)
	go r.failureLoop(ctx)
	go r.cronLoop(ctx)

	// Reader: handles server frames (and answers pings, which needs an active reader).
	go func() {
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				stop(err)
				return
			}
			var env proto.Envelope
			if json.Unmarshal(data, &env) != nil {
				continue
			}
			switch env.Type {
			case proto.TypeConfig:
				var cfg proto.Config
				if json.Unmarshal(env.Data, &cfg) != nil {
					continue
				}
				if cfg.MetricsIntervalSec >= 5 && cfg.MetricsIntervalSec <= 3600 {
					r.interval.Store(int64(cfg.MetricsIntervalSec))
				}
				if cfg.InventoryIntervalSec >= 10 && cfg.InventoryIntervalSec <= 3600 {
					r.invEvery.Store(int64(cfg.InventoryIntervalSec))
				}
				r.logs.SetTargets(cfg.LogTargets, cfg.MaxLogLineBytes)
				r.health.Set(cfg.HealthChecks)
				r.storage.Set(cfg.StorageTargets)
				go r.applyBlocks(rootCtx, cfg.Blocking, cfg.BlockedIPs)
				if cfg.DiscoveryRoots != nil {
					roots := cfg.DiscoveryRoots
					r.roots.Store(&roots)
				}
			case proto.TypeScan:
				r.rescan()
			case proto.TypeExec:
				var req proto.Exec
				if json.Unmarshal(env.Data, &req) == nil && req.RunID != "" {
					// Runs use the agent's lifetime context: a dropped connection must not kill a build.
					r.exec.Handle(rootCtx, req)
				}
			case proto.TypeHealthNow:
				var appID string
				if json.Unmarshal(env.Data, &appID) == nil {
					r.health.Now(appID)
				}
			case proto.TypeStorageScan:
				var t proto.StorageTarget
				if json.Unmarshal(env.Data, &t) == nil && t.AppID != "" {
					r.storage.Request(t)
				}
			case proto.TypeUpdate:
				var u proto.Update
				if json.Unmarshal(env.Data, &u) != nil {
					continue
				}
				go func() {
					slog.Info("update requested by server", "from", r.Version, "to", u.Version)
					exe, err := os.Executable()
					if err == nil {
						exe, err = filepath.EvalSymlinks(exe)
					}
					if err == nil {
						p := selfupdate.Platform()
						err = selfupdate.Apply(ctx, r.Cfg.ServerURL+"/agent/download/"+p, u.SHA256[p], exe)
					}
					if err != nil {
						slog.Error("update failed", "err", err)
						send(ctx, c, proto.TypeUpdateResult, err.Error())
						return
					}
					slog.Info("agent binary updated; restarting", "version", u.Version)
					stop(ErrRestart) // before Close: the reader would otherwise record "connection closed" first
					c.Close(websocket.StatusNormalClosure, "updating")
				}()
			case proto.TypeSecurityScan:
				var req proto.SecurityScan
				if json.Unmarshal(env.Data, &req) == nil {
					go func() { r.sendCurrent(proto.TypeSecurityReport, hostcheck.SecurityScan(rootCtx, req)) }()
				}
			case proto.TypeDockerList:
				go func() { r.sendCurrent(proto.TypeDockerReport, docker.Report(rootCtx)) }()
			case proto.TypeDockerAction:
				var req proto.DockerAction
				if json.Unmarshal(env.Data, &req) == nil && req.RunID != "" {
					go func() {
						r.sendCurrent(proto.TypeDockerResult, docker.Act(rootCtx, req))
						if req.Action != "logs" {
							r.sendCurrent(proto.TypeDockerReport, docker.Report(rootCtx))
						}
					}()
				}
			case proto.TypeDBRequest:
				var req proto.DBRequest
				if json.Unmarshal(env.Data, &req) == nil && req.RunID != "" {
					go func() { r.sendCurrent(proto.TypeDBResult, dbtool.Handle(rootCtx, req)) }()
				}
			case proto.TypeBackupScan:
				go func() { r.sendCurrent(proto.TypeBackupFound, backup.Find(rootCtx)) }()
			case proto.TypeBackupRun:
				var req proto.BackupRun
				if json.Unmarshal(env.Data, &req) == nil && req.RunID != "" {
					go func() { r.sendCurrent(proto.TypeBackupResult, backup.Run(rootCtx, req)) }()
				}
			case proto.TypeBackupRestore:
				var req proto.BackupRestore
				if json.Unmarshal(env.Data, &req) == nil && req.RunID != "" {
					go func() { r.sendCurrent(proto.TypeBackupRestored, backup.Restore(rootCtx, req)) }()
				}
			case proto.TypeCronChange:
				var req proto.CronChange
				if json.Unmarshal(env.Data, &req) == nil && req.RunID != "" {
					go func() {
						r.sendCurrent(proto.TypeCronResult, hostcheck.ChangeCron(rootCtx, req, r.Cfg.AllowCustomCommands))
						r.sendCurrent(proto.TypeCronReport, hostcheck.Cron(rootCtx))
					}()
				}
			case proto.TypeFirewallScan:
				go func() { r.sendCurrent(proto.TypeFirewallReport, hostcheck.Firewall(rootCtx)) }()
			case proto.TypeFirewallChange:
				var req proto.FirewallChange
				if json.Unmarshal(env.Data, &req) == nil && req.RunID != "" {
					go func() {
						r.sendCurrent(proto.TypeFirewallResult, hostcheck.ChangeFirewall(rootCtx, req))
						r.sendCurrent(proto.TypeFirewallReport, hostcheck.Firewall(rootCtx))
					}()
				}
			case proto.TypeUpdatesScan:
				var req proto.UpdatesScan
				json.Unmarshal(env.Data, &req)
				go func() { r.sendCurrent(proto.TypeUpdatesReport, hostcheck.Updates(rootCtx, req.Refresh)) }()
			case proto.TypeUpdatesInstall:
				var req proto.UpdatesInstall
				if json.Unmarshal(env.Data, &req) == nil && req.RunID != "" {
					go func() {
						r.sendCurrent(proto.TypeUpdatesResult, hostcheck.InstallUpdates(rootCtx, req))
						r.sendCurrent(proto.TypeUpdatesReport, hostcheck.Updates(rootCtx, false))
					}()
				}
			case proto.TypeDepsScan:
				var req proto.DepsScan
				if json.Unmarshal(env.Data, &req) == nil {
					go func() { r.sendCurrent(proto.TypeDepsReport, hostcheck.Deps(req)) }()
				}
			case proto.TypeSiteDeploy:
				var req proto.SiteDeploy
				if json.Unmarshal(env.Data, &req) == nil && req.RunID != "" {
					go func() { r.sendCurrent(proto.TypeSiteResult, sites.Deploy(rootCtx, req)) }()
				}
			case proto.TypeCertRenew:
				var req proto.CertRenew
				if json.Unmarshal(env.Data, &req) == nil && req.RunID != "" {
					go func() { r.sendCurrent(proto.TypeCertRenewed, caddy.Renew(rootCtx, req)) }()
				}
			case proto.TypeEnvFile:
				var req proto.EnvFile
				if json.Unmarshal(env.Data, &req) == nil && req.RunID != "" {
					go func() { r.sendCurrent(proto.TypeEnvFileResult, envfile.Handle(req)) }()
				}
			case proto.TypeSiteRemove:
				var req proto.SiteRemove
				if json.Unmarshal(env.Data, &req) == nil && req.RunID != "" {
					go func() { r.sendCurrent(proto.TypeSiteResult, sites.Remove(req)) }()
				}
			case proto.TypeDiskScan:
				go func() { r.sendCurrent(proto.TypeDiskReport, hostcheck.DiskScan(rootCtx)) }()
			case proto.TypeDiskClean:
				var req proto.DiskClean
				if json.Unmarshal(env.Data, &req) == nil && req.RunID != "" {
					go func() { r.sendCurrent(proto.TypeDiskCleaned, hostcheck.DiskClean(rootCtx, req)) }()
				}
			case proto.TypeRestart:
				slog.Info("restart requested by server")
				c.Close(websocket.StatusNormalClosure, "restarting")
				stop(ErrRestart)
				return
			}
		}
	}()

	for {
		if err := send(ctx, c, proto.TypeMetrics, r.sampler.Sample()); err != nil {
			if cause := context.Cause(ctx); cause != nil {
				return cause
			}
			return err
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(time.Duration(r.interval.Load()) * time.Second):
		}
	}
}

// inventoryLoop sends an inventory right away, then periodically or when asked.
// Collection can take seconds (git, pm2), so it runs apart from the metrics loop.
func (r *Runner) inventoryLoop(ctx context.Context, c *websocket.Conn, scanNow <-chan struct{}) {
	for {
		inv := r.collector.Collect(ctx, *r.roots.Load())
		if ctx.Err() != nil {
			return
		}
		if err := send(ctx, c, proto.TypeInventory, inv); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-scanNow:
		case <-time.After(time.Duration(r.invEvery.Load()) * time.Second):
		}
	}
}

func (r *Runner) rescan() {
	select {
	case r.scanNow <- struct{}{}:
	default:
	}
}

// sendCurrent writes to whichever connection is live; it fails while disconnected.
func (r *Runner) sendCurrent(typ string, v any) error {
	c := r.cur.Load()
	if c == nil {
		return errors.New("not connected")
	}
	return send(context.Background(), c, typ, v)
}

// resultLoop forwards health results and finished storage scans every second.
func (r *Runner) resultLoop(ctx context.Context, c *websocket.Conn) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if res := r.health.Take(); len(res) > 0 {
			if send(ctx, c, proto.TypeHealth, res) != nil {
				return
			}
		}
		for _, res := range r.storage.Take() {
			if send(ctx, c, proto.TypeStorageResult, res) != nil {
				return
			}
		}
	}
}

// logLoop sends buffered log lines in batches. Lines leave the buffer only after a
// successful send, so a dropped connection loses nothing that fits in the buffer.
func (r *Runner) logLoop(ctx context.Context, c *websocket.Conn) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for {
			batch, notice := r.logs.Buf.Peek(1000)
			if len(batch) == 0 {
				break
			}
			if err := send(ctx, c, proto.TypeLogs, batch); err != nil {
				return
			}
			n := len(batch)
			if notice {
				n--
			}
			r.logs.Buf.Commit(n, notice)
			if n < 1000 {
				break
			}
		}
	}
}

func send(ctx context.Context, c *websocket.Conn, typ string, v any) error {
	frame, err := proto.Encode(typ, "", v)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return c.Write(wctx, websocket.MessageText, frame)
}

// Enroll trades a one-time token for this agent's permanent credential.
func Enroll(ctx context.Context, serverURL, token string) (proto.EnrollResponse, error) {
	var out proto.EnrollResponse
	body, _ := json.Marshal(proto.EnrollRequest{Token: token})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/agent/enroll", bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return out, err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode != http.StatusOK {
		var e struct{ Error string }
		json.Unmarshal(raw, &e)
		if e.Error == "" {
			e.Error = res.Status
		}
		return out, fmt.Errorf("enrollment refused: %s", e.Error)
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Credential == "" {
		return out, errors.New("enrollment: unexpected server response")
	}
	return out, nil
}

// applyBlocks keeps the firewall chain of blocked addresses in step with the server's
// list, touching iptables only when the list changed.
func (r *Runner) applyBlocks(ctx context.Context, enabled bool, list []string) {
	r.blockMu.Lock()
	defer r.blockMu.Unlock()
	key := fmt.Sprint(enabled, list)
	if prev := r.blocks.Load(); prev != nil && *prev == key {
		return // nothing changed
	}
	// Also when off at start: a chain left by an earlier agent process must go.
	st := hostcheck.ApplyBlocks(ctx, enabled, list)
	r.blocks.Store(&key)
	if enabled {
		r.sendCurrent(proto.TypeBlockStatus, st)
	}
}

// failureLoop reports failed SSH logins every minute, so the server can block an
// address that guesses passwords on any of its servers.
func (r *Runner) failureLoop(ctx context.Context) {
	since := time.Now().Add(-10 * time.Minute)
	for {
		list, newest := hostcheck.Failures(ctx, since)
		if len(list) == 0 || r.sendCurrent(proto.TypeSSHFailures, list) == nil {
			since = newest.Add(time.Microsecond)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

// cronLoop reports the server's cron jobs and timers every 5 minutes.
func (r *Runner) cronLoop(ctx context.Context) {
	for {
		r.sendCurrent(proto.TypeCronReport, hostcheck.Cron(ctx))
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Minute):
		}
	}
}

// loginLoop reports new successful SSH logins every few minutes. After a start it
// sends the last week; the server ignores logins it already has.
func (r *Runner) loginLoop(ctx context.Context) {
	if r.loginsSince.Load() == 0 {
		r.loginsSince.Store(time.Now().Add(-7 * 24 * time.Hour).UnixMilli())
	}
	for {
		since := r.loginsSince.Load()
		list := hostcheck.Logins(ctx, time.UnixMilli(since+1))
		if len(list) > 0 && r.sendCurrent(proto.TypeSSHLogins, list) == nil {
			r.loginsSince.Store(list[len(list)-1].TS)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Minute):
		}
	}
}
