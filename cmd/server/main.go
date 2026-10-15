// Command ops-server is the central Fleetwatch server: API, WebSockets and the embedded UI.
package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"fleetwatch/deploy"
	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/agentbin"
	"fleetwatch/internal/server/api"
	"fleetwatch/internal/server/apps"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/commands"
	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/fleet"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/logs"
	"fleetwatch/internal/server/metrics"
	"fleetwatch/internal/server/notify"
	"fleetwatch/internal/server/release"
	"fleetwatch/internal/server/secret"
	"fleetwatch/web"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: ops-server <command>

commands:
  serve           run the server (default)
  hash-password   print an argon2id hash for ADMIN_PASSWORD_HASH
  reset-admin     set a new admin password, disable 2FA, sign out all sessions
  backup FILE     write a consistent copy of the database to FILE (safe while serving)
  caddy-config DOMAIN  print the Caddy site block (deploy/fleetwatch.caddy) for DOMAIN
  self-update     install the release requested from the dashboard (run by
                  ops-server-update.service as root; not meant to be run by hand)
  version         print the version

environment:
  OPS_LISTEN           listen address (default 127.0.0.1:8090)
  OPS_DATA_DIR         database and key directory (default ./data)
  OPS_SECRET_KEY       optional base64 32-byte key; default is DATA_DIR/secret.key
  OPS_UPDATE_SOURCE    where releases come from (default the GitHub releases of
                       nourddinak/fleetwatch); "off" disables update checks
  OPS_PUBLIC_URL       URL agents use to reach this server, e.g. https://ops.example.com
                       (default: derived from each request's Host header)
  ADMIN_USERNAME       optional: create the admin from env on first start
  ADMIN_PASSWORD_HASH  optional: its hash; without both, the browser setup wizard
                       creates the admin using a code printed in the server log
`

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "hash-password":
		err = hashPassword()
	case "reset-admin":
		err = resetAdmin()
	case "backup":
		err = backup(os.Args[2:])
	case "caddy-config":
		err = caddyConfig(os.Args[2:])
	case "self-update":
		err = selfUpdate()
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func openStore() (*sql.DB, *auth.Service, *secret.Box, error) {
	dir := env("OPS_DATA_DIR", "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, nil, err
	}
	key, err := secret.LoadKey(os.Getenv("OPS_SECRET_KEY"), dir)
	if err != nil {
		return nil, nil, nil, err
	}
	box, err := secret.New(key)
	if err != nil {
		return nil, nil, nil, err
	}
	d, err := db.Open(filepath.Join(dir, "ops.db"))
	if err != nil {
		return nil, nil, nil, err
	}
	return d, auth.NewService(d, box), box, nil
}

func caddyConfig(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: ops-server caddy-config DOMAIN")
	}
	site, err := deploy.CaddySite(args[0])
	if err == nil {
		fmt.Print(site)
	}
	return err
}

func selfUpdate() error {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	tag, err := release.Install(ctx, env("OPS_UPDATE_SOURCE", release.DefaultSource), env("OPS_DATA_DIR", "data"), exe)
	if err != nil {
		return fmt.Errorf("update to %s: %w", tag, err)
	}
	slog.Info("installed; restarting ops-server", "version", tag)
	return exec.Command("systemctl", "restart", "ops-server").Run()
}

func backup(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: ops-server backup FILE")
	}
	if _, err := os.Stat(args[0]); err == nil {
		return fmt.Errorf("%s already exists", args[0])
	}
	d, err := db.Open(filepath.Join(env("OPS_DATA_DIR", "data"), "ops.db"))
	if err != nil {
		return err
	}
	defer d.Close()
	if _, err := d.Exec(`VACUUM INTO ?`, args[0]); err != nil {
		return err
	}
	if err := os.Chmod(args[0], 0o600); err != nil { // it holds password and session hashes
		return err
	}
	fmt.Printf("database copied to %s\nalso back up the secret key (OPS_SECRET_KEY or %s): without it, 2FA and notification secrets cannot be read\n",
		args[0], filepath.Join(env("OPS_DATA_DIR", "data"), "secret.key"))
	return nil
}

func serve() error {
	d, authSvc, box, err := openStore()
	if err != nil {
		return err
	}
	defer d.Close()
	created, err := authSvc.EnsureAdmin(os.Getenv("ADMIN_USERNAME"), os.Getenv("ADMIN_PASSWORD_HASH"))
	if err != nil {
		return err
	}
	if created {
		slog.Info("admin account created from environment", "username", os.Getenv("ADMIN_USERNAME"))
	}
	if authSvc.NeedsSetup() {
		code := authSvc.StartSetup()
		slog.Warn("no admin account yet: open the dashboard and enter this one-time setup code", "setup_code", code)
	}

	bus := live.NewBus()
	store := metrics.NewStore(d, metrics.ServerKind)
	appStore := metrics.NewStore(d, metrics.AppKind)
	logStore := logs.NewStore(d)
	var appSvc *apps.Service
	hub := fleet.NewHub(fleet.NewRegistry(d), store, bus, version, func(serverID string) proto.Config {
		return proto.Config{
			MetricsIntervalSec:   metrics.LoadSettings(d).IntervalSec,
			InventoryIntervalSec: 30,
			DiscoveryRoots:       appSvc.DiscoverySettings().Roots,
			LogTargets:           appSvc.LogTargets(serverID),
			HealthChecks:         appSvc.HealthChecks(serverID),
			StorageTargets:       appSvc.StorageTargets(serverID),
			MaxLogLineBytes:      logStore.Settings().MaxLineKB << 10,
		}
	})
	appSvc = apps.New(d, bus, appStore, hub.Connected)
	appSvc.OnChange = hub.SendConfig
	appSvc.ErrorCount = logStore.ErrorCount
	hub.OnInventory = appSvc.Ingest
	hub.OnLogs = func(serverID string, lines []proto.LogLine) { logStore.Submit(serverID, lines) }
	cmdSvc := commands.New(d, appSvc, bus, hub.Send)

	// Incidents and notifications.
	serverName := func(id string) string {
		if rec, err := hub.Reg.Get(id); err == nil {
			return rec.Name
		}
		return id
	}
	incStore := incidents.NewStore(d, bus)
	detector := incidents.NewDetector(incStore, d, hub.Connected, serverName)
	notifier := notify.New(d, box, bus)
	notifier.PublicURL = os.Getenv("OPS_PUBLIC_URL")
	notifier.ServerName = serverName
	notifier.AppName = func(id string) string {
		if v, err := appSvc.Get(id); err == nil {
			return v.Name
		}
		return id
	}
	notifier.Overrides = func(serverID, appID string) (string, bool) {
		override := "default"
		if rec, err := hub.Reg.Get(serverID); err == nil {
			override = rec.NotifyOverride
		}
		if appID == "" {
			return override, true
		}
		v, err := appSvc.Get(appID)
		return override, err != nil || v.Flags.Notifications
	}
	incStore.Notify = notifier.Incident
	appSvc.OnViews = func(serverID string, views []apps.View) { detector.Apps(serverID, views, time.Now()) }
	logStore.OnStored = func(lines []logs.Line) { detector.Logs(lines, time.Now()) }
	hub.OnMetrics = func(serverID string, m proto.Metrics) {
		detector.ServerSample(serverID, metrics.FromProto(m), time.Now())
	}
	// Servers that never reconnect after a restart still get an offline incident.
	if recs, err := hub.Reg.List(); err == nil {
		for _, rec := range recs {
			if rec.EnrolledAt != 0 {
				detector.ServerState(rec.ID, false)
			}
		}
	}
	hub.Handlers[proto.TypeExecOutput] = func(serverID string, data json.RawMessage) {
		var o proto.ExecOutput
		if json.Unmarshal(data, &o) == nil {
			cmdSvc.Output(serverID, o)
		}
	}
	hub.Handlers[proto.TypeExecResult] = func(serverID string, data json.RawMessage) {
		var res proto.ExecResult
		if json.Unmarshal(data, &res) == nil {
			cmdSvc.Result(serverID, res)
		}
	}
	hub.Handlers[proto.TypeHealth] = func(serverID string, data json.RawMessage) {
		var res []proto.HealthResult
		if json.Unmarshal(data, &res) == nil {
			appSvc.IngestHealth(serverID, res)
		}
	}
	hub.Handlers[proto.TypeUpdateResult] = func(serverID string, data json.RawMessage) {
		var msg string
		json.Unmarshal(data, &msg)
		hub.Reg.AddEvent(serverID, "agent", "Agent update failed: "+msg)
		hub.Publish(serverID)
	}
	hub.Handlers[proto.TypeStorageResult] = func(serverID string, data json.RawMessage) {
		var res proto.StorageResult
		if json.Unmarshal(data, &res) == nil {
			appSvc.IngestStorage(serverID, res)
		}
	}
	hub.OnStateChange = func(serverID string) {
		connected := hub.Connected(serverID)
		detector.ServerState(serverID, connected)
		if connected {
			appSvc.PublishServer(serverID)
		} else {
			appSvc.ServerOffline(serverID)
			cmdSvc.AgentGone(serverID)
		}
	}

	addr := env("OPS_LISTEN", "127.0.0.1:8090")
	updates := release.New(os.Getenv("OPS_UPDATE_SOURCE"), version, env("OPS_DATA_DIR", "data"))
	updates.OnChange = func(st release.Status) { bus.Publish("update", st) }
	// "Update the agents too": for an hour after such an update, every agent that
	// connects on an older version is asked once to install this version's agent.
	// With the "always auto-update agents" setting, that applies at any time.
	var agentsUntil time.Time
	if updates.AgentsFollow() {
		agentsUntil = time.Now().Add(time.Hour)
		slog.Info("platform updated; updating agents as they reconnect", "version", version)
	}
	var asked sync.Map
	hub.OnConnect = func(serverID, agentVersion string) {
		if agentVersion == version || (time.Now().After(agentsUntil) && !fleet.LoadAgentSettings(d).AutoUpdate) {
			return
		}
		if _, done := asked.LoadOrStore(serverID, true); done {
			return
		}
		go func() {
			time.Sleep(3 * time.Second) // let the agent finish starting up
			u, ok := agentbin.UpdateMessage(version)
			if !ok {
				return
			}
			if err := hub.Send(serverID, proto.TypeUpdate, u); err == nil {
				hub.Reg.AddEvent(serverID, "agent", "Update to v"+version+" started automatically")
				hub.Publish(serverID)
			}
		}()
	}
	srv := &http.Server{
		Addr: addr,
		Handler: api.New(api.Deps{
			UI: web.Dist(), Version: version, DB: d, Auth: authSvc,
			Fleet: hub, Metrics: store, Bus: bus, PublicURL: os.Getenv("OPS_PUBLIC_URL"),
			Apps: appSvc, AppMetrics: appStore, Logs: logStore, Commands: cmdSvc,
			Incidents: incStore, Detector: detector, Notify: notifier, Release: updates,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go logStore.Run(ctx)
	go notifier.Run(ctx)
	go updates.Run(ctx)
	go runJobs(ctx, d, authSvc, appSvc, logStore, detector, notifier, store, appStore)

	errc := make(chan error, 1)
	go func() {
		slog.Info("ops-server listening", "addr", addr, "version", version)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	hub.Shutdown() // hijacked WebSockets are not closed by Shutdown
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// runJobs does periodic maintenance until ctx ends.
func runJobs(ctx context.Context, d *sql.DB, authSvc *auth.Service, appSvc *apps.Service, logStore *logs.Store, detector *incidents.Detector, notifier *notify.Service, stores ...*metrics.Store) {
	logStore.RefreshErrorCounts(time.Now())
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for n := 0; ; n++ {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			detector.Sweep(now)
			for _, st := range stores {
				if err := st.Rollup(now); err != nil {
					slog.Error("metrics rollup", "err", err)
				}
			}
			if n%5 == 0 {
				if err := logStore.RefreshErrorCounts(now); err != nil {
					slog.Error("log error counts", "err", err)
				}
			}
			if n%10 == 0 {
				authSvc.PurgeExpired()
				detector.Store.Prune(now, 90*24*time.Hour)
				notifier.Prune(now)
				if err := appSvc.PruneHealth(now); err != nil {
					slog.Error("health retention", "err", err)
				}
				if deleted, err := logStore.Prune(ctx, now); err != nil {
					slog.Error("log retention", "err", err)
				} else if deleted > 0 {
					slog.Info("log retention", "deleted", deleted)
				}
				for _, st := range stores {
					if err := st.Prune(now, metrics.LoadSettings(d).RetentionDays); err != nil {
						slog.Error("metrics prune", "err", err)
					}
				}
			}
		}
	}
}

// readNewPassword prompts twice without echo on a terminal, or reads one line from a pipe.
func readNewPassword() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		pw := strings.TrimRight(line, "\r\n")
		return pw, auth.ValidateNewPassword(pw)
	}
	fmt.Fprint(os.Stderr, "New password: ")
	a, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if err := auth.ValidateNewPassword(string(a)); err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Repeat password: ")
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(a) != string(b) {
		return "", errors.New("passwords do not match")
	}
	return string(a), nil
}

func hashPassword() error {
	pw, err := readNewPassword()
	if err != nil {
		return err
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	fmt.Println(h)
	return nil
}

func resetAdmin() error {
	pw, err := readNewPassword()
	if err != nil {
		return err
	}
	h, err := auth.HashPassword(pw)
	if err != nil {
		return err
	}
	d, authSvc, _, err := openStore()
	if err != nil {
		return err
	}
	defer d.Close()
	if err := authSvc.ResetAdmin(h); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "Admin password reset. 2FA is disabled and all sessions were signed out.")
	return nil
}
