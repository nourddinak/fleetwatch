// Command ops-server is the central Fleetwatch server: API, WebSockets and the embedded UI.
package main

import (
	"bufio"
	"cmp"
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
	"fleetwatch/internal/server/ai"
	"fleetwatch/internal/server/anomaly"
	"fleetwatch/internal/server/api"
	"fleetwatch/internal/server/apps"
	"fleetwatch/internal/server/audit"
	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/commands"
	"fleetwatch/internal/server/db"
	"fleetwatch/internal/server/disk"
	"fleetwatch/internal/server/dns"
	"fleetwatch/internal/server/docker"
	"fleetwatch/internal/server/domains"
	"fleetwatch/internal/server/envfile"
	"fleetwatch/internal/server/fleet"
	"fleetwatch/internal/server/forecast"
	"fleetwatch/internal/server/github"
	"fleetwatch/internal/server/hostadmin"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/logs"
	"fleetwatch/internal/server/maintenance"
	"fleetwatch/internal/server/metrics"
	"fleetwatch/internal/server/mobile"
	"fleetwatch/internal/server/notify"
	"fleetwatch/internal/server/oncall"
	"fleetwatch/internal/server/push"
	"fleetwatch/internal/server/release"
	"fleetwatch/internal/server/secret"
	"fleetwatch/internal/server/security"
	"fleetwatch/internal/server/sites"
	"fleetwatch/internal/server/statuspage"
	"fleetwatch/internal/server/tokens"
	"fleetwatch/internal/server/uptime"
	"fleetwatch/internal/server/vulns"
	"fleetwatch/internal/server/whatsapp"
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
	// The same root helper also sets up Caddy for status pages with their own domain.
	if dir := env("OPS_DATA_DIR", "data"); statuspage.CaddyRequested(dir) {
		return statuspage.CaddySync(dir, env("OPS_LISTEN", "127.0.0.1:8090"))
	}
	// And installs ffmpeg, for WhatsApp voice-note answers.
	if dir := env("OPS_DATA_DIR", "data"); whatsapp.FFmpegRequested(dir) {
		return whatsapp.InstallFFmpeg(dir)
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

	if p, err := authSvc.Profile(); err == nil {
		if n, err := audit.Backfill(d, cmp.Or(p.DisplayName, p.Username)); err == nil && n > 0 {
			slog.Info("named who did past actions", "events", n)
		}
	}
	bus := live.NewBus()
	store := metrics.NewStore(d, metrics.ServerKind)
	appStore := metrics.NewStore(d, metrics.AppKind)
	logStore := logs.NewStore(d)
	var appSvc *apps.Service
	var upSvc *uptime.Service
	admin := hostadmin.New(d, bus)
	hub := fleet.NewHub(fleet.NewRegistry(d), store, bus, version, func(serverID string) proto.Config {
		blocking, blocked := admin.BlockedIPs()
		return proto.Config{
			MetricsIntervalSec:   metrics.LoadSettings(d).IntervalSec,
			InventoryIntervalSec: 30,
			DiscoveryRoots:       appSvc.DiscoverySettings().Roots,
			LogTargets:           appSvc.LogTargets(serverID),
			HealthChecks:         append(appSvc.HealthChecks(serverID), upSvc.AgentChecks(serverID)...),
			StorageTargets:       appSvc.StorageTargets(serverID),
			MaxLogLineBytes:      logStore.Settings().MaxLineKB << 10,
			Blocking:             blocking,
			BlockedIPs:           blocked,
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
	upSvc = uptime.New(d, incStore, bus)
	upSvc.Send, upSvc.OnChange = hub.Send, hub.SendConfig
	upSvc.ServerExists = func(id string) bool {
		_, err := hub.Reg.Get(id)
		return err == nil
	}
	detector := incidents.NewDetector(incStore, d, hub.Connected, serverName)
	notifier := notify.New(d, box, bus)
	pushSvc := push.New(d, box)
	if u := os.Getenv("OPS_PUBLIC_URL"); strings.HasPrefix(u, "https://") {
		pushSvc.Subscriber = u // identifies this dashboard to browser push services
	}
	notifier.Push = pushSvc
	notifier.PublicURL = os.Getenv("OPS_PUBLIC_URL")
	notifier.ServerName = serverName
	notifier.Runbook = func(typ string) string { return incidents.Runbooks(d)[typ] }
	notifier.AppName = func(id string) string {
		if v, err := appSvc.Get(id); err == nil {
			return v.Name
		}
		return id
	}
	maint, err := maintenance.New(d, bus)
	if err != nil {
		return err
	}
	notifier.Overrides = func(serverID, appID string) (string, bool) {
		if maint.Active(serverID, appID) {
			return "muted", true // maintenance: incidents are recorded, alerts are not sent
		}
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
	// Phones running the Fleetwatch Notifications app: every alert that passes the
	// notification rules, limited to the servers each person can see.
	phones := mobile.New(d)
	phones.Scope = func(memberID string) ([]string, bool) {
		if memberID == "" {
			return nil, true
		}
		m, err := authSvc.GetMember(memberID)
		if err != nil || m.Disabled {
			return nil, false
		}
		return m.Servers, true
	}
	notifier.Mirror = func(msg notify.Message, serverID, appID string) {
		kind := "notice"
		switch msg.Details.Event {
		case "incident.opened":
			kind = "incident"
		case "incident.resolved":
			kind = "resolved"
		}
		phones.Alert(mobile.Message{Kind: kind, Title: msg.Title, Body: msg.Body, Severity: msg.Severity, IncidentID: msg.Details.IncidentID, ServerID: serverID, URL: msg.Link})
	}
	go func() {
		for range time.Tick(time.Hour) {
			phones.Prune()
		}
	}()
	onCall := oncall.New(d, nil)
	onCall.Incidents, onCall.Muted = incStore, maint.Active
	onCall.PublicURL = func() string { return os.Getenv("OPS_PUBLIC_URL") }
	onCall.People = func() []oncall.Person {
		var out []oncall.Person
		if p, err := authSvc.Profile(); err == nil {
			out = append(out, oncall.Person{ID: oncall.Owner, Name: cmp.Or(p.DisplayName, p.Username), Username: p.Username, Email: p.Email, Devices: pushSvc.Count("")})
		}
		if list, err := authSvc.ListMembers(); err == nil {
			for _, m := range list {
				if !m.Disabled {
					out = append(out, oncall.Person{ID: m.ID, Name: cmp.Or(m.DisplayName, m.Username), Username: m.Username, Email: m.Email, Devices: pushSvc.Count(m.ID)})
				}
			}
		}
		return out
	}
	incStore.Notify = func(ev incidents.Event) {
		notifier.Incident(ev)
		onCall.OnEvent(ev)
	}
	// The start of someone's on-call week: tell them, with the note the last person left.
	onCall.OnHandover = func(from, to oncall.Person, note *oncall.Note, open int) {
		member := to.ID
		if member == oncall.Owner {
			member = ""
		}
		body := fmt.Sprintf("%d open incident(s).", open)
		if note != nil && note.Text != "" {
			body = "From " + note.By + ": " + note.Text + "\n" + body
		} else if from.Name != "" {
			body = "Taking over from " + from.Name + ". " + body
		}
		phones.ToPerson(member, mobile.Message{Kind: "notice", Title: "You are on call now", Body: body, Severity: "info"})
		pushSvc.SendTo(context.Background(), member, push.Message{Title: "You are on call now", Body: body, Tag: "handoff"})
	}
	status := statuspage.New(d, incStore)
	status.DataDir, status.HelperPath = env("OPS_DATA_DIR", "data"), release.HelperUnit
	status.AppState = func(id string) (string, string, int64, bool) {
		v, err := appSvc.Get(id)
		if err != nil {
			return "", "", 0, false
		}
		state := "operational" // also "unknown": static sites have no process to watch
		switch {
		case maint.Active(v.ServerID, v.ID):
			state = "maintenance"
		case !hub.Connected(v.ServerID), v.HealthStatus == "failed", v.Status == "offline", v.Status == "stopped", v.Status == "crash-loop":
			state = "outage"
		case v.Status == "warning" || v.Status == "starting":
			state = "degraded"
		}
		return state, v.ServerID, v.DiscoveredAt, true
	}
	status.ServerState = func(id string) (string, int64, bool) {
		rec, err := hub.Reg.Get(id)
		if err != nil {
			return "", 0, false
		}
		switch {
		case maint.Active(id, ""):
			return "maintenance", rec.CreatedAt, true
		case hub.Connected(id):
			return "operational", rec.CreatedAt, true
		}
		return "outage", rec.CreatedAt, true
	}
	status.Mail = statuspage.NewMailer(d, box)
	// An on-call page: push to that person's phones (tapping opens the acknowledge
	// button) and an email when they have an address and email is set up.
	// The owner's WhatsApp, linked like WhatsApp Web, pages people there too.
	waSvc := whatsapp.New(d, env("OPS_DATA_DIR", "data"), bus)
	waSvc.DataDir, waSvc.HelperPath = env("OPS_DATA_DIR", "data"), release.HelperUnit
	waSvc.EnsureVoice() // voice-note answers asked for before ffmpeg was there
	if err := waSvc.Start(context.Background()); err != nil {
		slog.Warn("whatsapp", "err", err)
	}
	// Answered calls talk with the AI (Gemini Live), when the owner set it up.
	aiSvc := ai.New(d, box)
	calls := &callDeps{db: d, auth: authSvc, commands: cmdSvc, detector: detector, ai: aiSvc, wa: waSvc, incidents: incStore, hub: hub, apps: appSvc, logs: logStore, uptime: upSvc, admin: admin, people: func() []oncall.Person { return onCall.People() }, oncall: onCall}
	waSvc.Talker = func(person, incident string) whatsapp.Talker { return calls.talker(person, incident) }
	waSvc.Inbound = func(person string) whatsapp.Talker { return calls.inbound(person) }
	waSvc.OnMessage = calls.message // team members can write to it, or send voice notes
	waSvc.Speak = aiSvc.Speak       // and get voice notes back, if they like
	calls.status = status           // the owner posts to status pages from WhatsApp
	onCall.Page = func(ctx context.Context, p oncall.Person, x incidents.Incident, ackURL string, level int) error {
		why := ""
		if level > 0 {
			why = " Nobody acknowledged in time, so it came to you."
		}
		memberID := p.ID
		if p.ID == oncall.Owner {
			memberID = ""
		}
		// The phone app first (it rings even with the browser closed), then browser
		// push and email. The page counts as delivered if any of them got through.
		reached := phones.ToPerson(memberID, mobile.Message{Kind: "oncall", Title: "On call: " + x.Title,
			Body: x.Type + " on " + serverName(x.ServerID) + "." + why, Severity: "critical", IncidentID: x.ID, ServerID: x.ServerID, URL: ackURL}) > 0
		var errs []error
		if err := pushSvc.SendTo(ctx, memberID, push.Message{Title: "On call: " + x.Title, Body: x.Type + " on " + serverName(x.ServerID) + "." + why,
			URL: ackURL, Tag: "oncall-" + x.ID, Severity: "critical"}); err == nil {
			reached = true
		} else {
			errs = append(errs, err)
		}
		if p.Email != "" && status.Mail.Ready() {
			text := fmt.Sprintf("%s\n%s on %s.%s\n\nI'm on it (stops paging): %s\nDashboard: %s/#/incidents\n",
				x.Title, x.Type, serverName(x.ServerID), why, ackURL, strings.TrimRight(os.Getenv("OPS_PUBLIC_URL"), "/"))
			if err := status.Mail.Send(ctx, statuspage.Mail{To: []string{p.Email}, Subject: "[On call] " + x.Title, Text: text}); err == nil {
				reached = true
			} else {
				errs = append(errs, err)
			}
		}
		if wp, ok := waSvc.PersonOf(p.ID); ok && waSvc.Linked() && !wp.Quiet(time.Now()) {
			go func() { // the call rings while the rest of the page goes out
				c, cancel := context.WithTimeout(context.Background(), 8*time.Minute) // ringing, then the AI may talk
				defer cancel()
				if _, err := waSvc.Ring(c, wp.Phone, 45*time.Second, waSvc.TalkerFor(p.ID, x.ID)); err != nil {
					slog.Warn("whatsapp call", "person", p.Name, "err", err)
				}
			}()
			text := fmt.Sprintf("🚨 On call: %s\n%s on %s.%s\n\nI'm on it (stops the paging): %s", x.Title, x.Type, serverName(x.ServerID), why, ackURL)
			if err := waSvc.Send(ctx, wp.Phone, text); err == nil {
				reached = true
			} else {
				errs = append(errs, err)
			}
		}
		if !reached {
			return errors.Join(append(errs, errors.New("no phone, browser or email reaches "+p.Name))...)
		}
		return nil
	}
	status.Latency = func(appID string) []proto.HealthResult {
		list, _ := appSvc.HealthHistory(appID, 3000)
		return list
	}
	status.OnDraft = func(page statuspage.Config, n statuspage.Notice) {
		link := ""
		if u := os.Getenv("OPS_PUBLIC_URL"); u != "" {
			link = strings.TrimRight(u, "/") + "/#/status-page/" + page.ID
		}
		notifier.Notice("Status page draft", notify.Message{Title: "Draft notice waiting on " + page.Title + ": " + n.Title,
			Body: "An outage started. Publish the draft notice on the status page, or delete it.", Severity: "warning", Link: link}, "", "")
	}
	serverOf := func(appID string) string {
		if v, err := appSvc.Get(appID); err == nil {
			return v.ServerID
		}
		return ""
	}
	// The maintenance switch also posts (and later closes) a notice on the status pages.
	maint.OnStart = func(kind, id string, until int64) { status.MaintenanceChanged(kind, id, until, serverOf) }
	// When a window ends, report what is still broken in one message.
	maint.OnEnd = func(kind, id string) {
		status.MaintenanceChanged(kind, id, 0, serverOf)
		f := incidents.Filter{Status: "open"}
		det := notify.Details{Event: "maintenance.ended"}
		name := ""
		if kind == "server" {
			f.ServerID, det.ServerID, det.ServerName = id, id, serverName(id)
			name = det.ServerName
		} else {
			f.AppID, det.AppID = id, id
			if a, err := appSvc.Get(id); err == nil {
				det.AppName, det.ServerID, det.ServerName = a.Name, a.ServerID, serverName(a.ServerID)
				name = a.Name
			}
		}
		open, err := incStore.List(f)
		if err != nil || len(open) == 0 {
			return
		}
		sev, lines := "warning", make([]string, 0, len(open))
		for i, x := range open {
			if x.Severity == "critical" {
				sev = "critical"
			}
			if i < 5 {
				lines = append(lines, "• "+x.Title)
			}
		}
		if len(open) > 5 {
			lines = append(lines, fmt.Sprintf("… and %d more", len(open)-5))
		}
		notifier.Notice("Maintenance ended", notify.Message{
			Title:    fmt.Sprintf("Maintenance on %s ended: %d problem(s) still open", name, len(open)),
			Body:     strings.Join(lines, "\n"),
			Severity: sev, Details: det,
		}, det.ServerID, det.AppID)
	}
	go func() {
		for range time.Tick(30 * time.Second) {
			maint.Sweep()
		}
	}()
	diskSvc := disk.New(bus)
	diskSvc.Send = hub.Send
	diskSvc.AgentVersion = func(id string) string {
		if rec, err := hub.Reg.Get(id); err == nil {
			return rec.AgentVersion
		}
		return ""
	}
	hub.Handlers[proto.TypeDiskReport] = diskSvc.OnReport
	hub.Handlers[proto.TypeDiskCleaned] = diskSvc.OnCleaned
	dockerSvc := docker.New(bus)
	dockerSvc.Send, dockerSvc.AgentVersion = hub.Send, diskSvc.AgentVersion
	hub.Handlers[proto.TypeDockerReport] = dockerSvc.OnReport
	hub.Handlers[proto.TypeDockerResult] = dockerSvc.OnResult
	admin.Send, admin.AgentVersion, admin.BroadcastConfig = hub.Send, diskSvc.AgentVersion, hub.BroadcastConfig
	// Never blocked: the servers' own addresses, and addresses someone signed in from
	// (SSH in the last 30 days, or the dashboard).
	admin.Protected = func() []string {
		var out []string
		if recs, err := hub.Reg.List(); err == nil {
			for _, r := range recs {
				out = append(append(out, r.PublicIP), r.IPs...)
			}
		}
		rows, err := d.Query(`SELECT DISTINCT ip FROM ssh_logins WHERE ts > ? UNION SELECT DISTINCT ip FROM sessions`, time.Now().Add(-30*24*time.Hour).UnixMilli())
		if err == nil {
			for rows.Next() {
				var ip string
				if rows.Scan(&ip) == nil && ip != "" {
					out = append(out, ip)
				}
			}
			rows.Close()
		}
		return out
	}
	admin.Notify = func(title, body, severity string) {
		notifier.Notice("Security", notify.Message{Title: title, Body: body, Severity: severity}, "", "")
		go calls.securityText(title, body) // and the owner on WhatsApp
	}
	hub.Handlers[proto.TypeSSHFailures] = admin.OnFailures
	hub.Handlers[proto.TypeBlockStatus] = admin.OnBlockStatus
	hub.Handlers[proto.TypeFirewallReport] = admin.OnFirewallReport
	hub.Handlers[proto.TypeFirewallResult] = admin.OnFirewallResult
	hub.Handlers[proto.TypeUpdatesReport] = admin.OnUpdatesReport
	hub.Handlers[proto.TypeUpdatesResult] = func(serverID string, data json.RawMessage) { admin.OnUpdatesResult(serverID, data, serverName) }
	vulnSvc := vulns.New(d, bus)
	vulnSvc.Send, vulnSvc.AgentVersion, vulnSvc.Connected = hub.Send, diskSvc.AgentVersion, hub.Connected
	vulnSvc.AppName = notifier.AppName
	vulnSvc.AppFolders = func(serverID string) map[string]string {
		out := map[string]string{}
		if views, err := appSvc.List(serverID); err == nil {
			for _, v := range views {
				if v.Path != "" {
					out[v.ID] = v.Path
				}
			}
		}
		return out
	}
	vulnSvc.Notify = admin.Notify
	hub.Handlers[proto.TypeDepsReport] = vulnSvc.OnReport
	// GitHub and the static sites deployed from it.
	ghSvc := github.New(d, box, bus)
	siteSvc := sites.New(d, box, bus, ghSvc)
	siteSvc.Send, siteSvc.AgentVersion, siteSvc.Notify = hub.Send, diskSvc.AgentVersion, admin.Notify
	siteSvc.PublicIP = func(id string) string {
		if rec, err := hub.Reg.Get(id); err == nil {
			return rec.PublicIP
		}
		return ""
	}
	// The dashboard's own machine: the server whose agent reports this hostname.
	host, _ := os.Hostname()
	siteSvc.IsLocal = func(id string) bool {
		rec, err := hub.Reg.Get(id)
		return err == nil && host != "" && strings.EqualFold(rec.Hostname, host)
	}
	hub.Handlers[proto.TypeSiteResult] = siteSvc.OnResult
	dnsSvc := dns.New(d, box, bus)
	siteSvc.DNS = dnsSvc
	// Cloudflare accounts that ask for it block the dashboard's blocked addresses too:
	// on every change, and every 15 minutes to catch up after errors.
	syncBlocks := func() {
		on, ips := admin.BlockedIPs()
		if !on {
			ips = nil
		}
		c, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		dnsSvc.SyncBlocks(c, ips)
	}
	admin.OnBlocks = syncBlocks
	envSvc := envfile.New()
	envSvc.Send, envSvc.AgentVersion = hub.Send, diskSvc.AgentVersion
	hub.Handlers[proto.TypeEnvFileResult] = envSvc.OnResult
	ghSvc.OnPush = siteSvc.OnPush
	appSvc.IsSite = siteSvc.IsSitePath
	serverIDs := func() []string {
		recs, _ := hub.Reg.List()
		ids := make([]string, 0, len(recs))
		for _, r := range recs {
			ids = append(ids, r.ID)
		}
		return ids
	}
	sec := security.New(d)
	calls.security = sec                                                                                       // the AI on calls reads security too
	calls.docker, calls.sites, calls.disk, calls.maint, calls.dns = dockerSvc, siteSvc, diskSvc, maint, dnsSvc // and acts with these
	waSvc.Alert = func(title, body, severity string) {
		notifier.Notice("WhatsApp", notify.Message{Title: title, Body: body, Severity: severity}, "", "")
	}
	vulnSvc.ServerIDs = serverIDs
	sec.Send, sec.ServerIDs, sec.AgentVersion, sec.Connected, sec.Name = hub.Send, serverIDs, diskSvc.AgentVersion, hub.Connected, serverName
	sec.Ports, sec.Domains = appSvc.ServerPorts, appSvc.Domains
	sec.AppPaths = func(serverID string) []string {
		var paths []string
		if views, err := appSvc.List(serverID); err == nil {
			for _, v := range views {
				if v.Path != "" {
					paths = append(paths, v.Path)
				}
			}
		}
		return paths
	}
	sec.Notify = func(title, body, severity string) {
		notifier.Notice("Security", notify.Message{Title: title, Body: body, Severity: severity}, "", "")
		if severity != "info" {
			go calls.securityText(title, body)
		}
	}
	hub.Handlers[proto.TypeSecurityReport] = sec.OnReport
	hub.Handlers[proto.TypeSSHLogins] = sec.OnLogins
	forecasts := &forecast.Service{Servers: store, AppStore: appStore, Apps: appSvc, Name: serverName, Incidents: incStore, Now: time.Now,
		ServerIDs: serverIDs}
	domainChecks := domains.New(d, appSvc, incStore, detector.Rules, bus)
	calls.domains = domainChecks
	domainChecks.PublicURL = os.Getenv("OPS_PUBLIC_URL")
	domainChecks.Send, domainChecks.AgentVersion = hub.Send, diskSvc.AgentVersion // "renew now", on the server
	hub.Handlers[proto.TypeCertRenewed] = domainChecks.OnRenewed
	appSvc.OnViews = func(serverID string, views []apps.View) { detector.Apps(serverID, views, time.Now()) }
	logStore.OnStored = func(lines []logs.Line) { detector.Logs(lines, time.Now()) }
	hub.OnMetrics = func(serverID string, m proto.Metrics) {
		sample := metrics.FromProto(m)
		detector.ServerSample(serverID, sample, time.Now())
		diskSvc.Watch(serverID, sample.Disk)
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
		if json.Unmarshal(data, &res) != nil {
			return
		}
		// Uptime checks this agent runs for other sites come back as health results too.
		var mine, probes []proto.HealthResult
		for _, r := range res {
			if id, ok := strings.CutPrefix(r.AppID, uptime.Prefix); ok {
				r.AppID = id
				probes = append(probes, r)
			} else {
				mine = append(mine, r)
			}
		}
		appSvc.IngestHealth(serverID, mine)
		upSvc.Ingest(serverID, probes)
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
	unusual := &anomaly.Service{DB: d, Incidents: incStore, ServerIDs: serverIDs, Name: serverName, Now: time.Now,
		Enabled: func() bool { return detector.Rules().Anomalies }}
	detector.Learned = unusual.Learned // "learned limits only" needs to know which servers are learned
	srv := &http.Server{
		Addr: addr,
		Handler: api.New(api.Deps{
			UI: web.Dist(), Version: version, DB: d, Auth: authSvc, DataDir: env("OPS_DATA_DIR", "data"),
			Fleet: hub, Metrics: store, Bus: bus, PublicURL: os.Getenv("OPS_PUBLIC_URL"),
			Apps: appSvc, AppMetrics: appStore, Logs: logStore, Commands: cmdSvc,
			Incidents: incStore, Detector: detector, Notify: notifier, Release: updates, Maint: maint, Domains: domainChecks, Tokens: tokens.New(d), Push: pushSvc, Status: status, Disk: diskSvc, Forecast: forecasts, Security: sec, OnCall: onCall, Mobile: phones, Uptime: upSvc, Docker: dockerSvc, Admin: admin, Vulns: vulnSvc, GitHub: ghSvc, Sites: siteSvc, EnvFiles: envSvc, DNS: dnsSvc, WhatsApp: waSvc, AI: aiSvc, Anomaly: unusual, EnvHistory: envfile.NewHistory(d, box), ServerFileHistory: envfile.NewServerHistory(d, box),
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
	go domainChecks.Run(ctx)
	go status.Run(ctx)
	go sec.Run(ctx)
	go onCall.Run(ctx)
	go upSvc.Run(ctx)
	go vulnSvc.Run(ctx)
	go siteSvc.Run(ctx)
	go func() {
		t := time.NewTicker(15 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				syncBlocks()
				c, cancel := context.WithTimeout(ctx, 5*time.Minute)
				dockerSvc.CheckUpdates(c, false) // asks only about images not checked for 6 hours
				cancel()
				calls.expiryAlerts(ctx) // certificates and domains about to expire, on WhatsApp
			}
		}
	}()
	go func() {
		for range time.Tick(time.Minute) {
			admin.Sweep()
			waSvc.Watch()           // tells the owner when WhatsApp stays disconnected
			calls.followUps(ctx)    // resolved messages, follow-up calls
			calls.briefings(ctx)    // morning briefings, at each person's time
			calls.checkWatches(ctx) // reminders and watches asked for on WhatsApp
			var connected []string
			for _, id := range serverIDs() {
				if hub.Connected(id) {
					connected = append(connected, id)
				}
			}
			admin.Tick(connected)
		}
	}()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Minute):
				unusual.Check()
			}
		}
	}()
	go func() { // forecasts: shortly after start, then hourly
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Minute):
			}
			forecasts.Run()
			select {
			case <-ctx.Done():
				return
			case <-time.After(58 * time.Minute):
			}
		}
	}()
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
