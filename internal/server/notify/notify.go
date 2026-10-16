package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/live"
	"fleetwatch/internal/server/push"
	"fleetwatch/internal/server/secret"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

// Providers lists the supported provider names, in display order.
var Providers = []string{"push", "ntfy", "telegram", "discord", "webhook"}

var ErrUnknownProvider = errors.New("unknown notification provider")

// Rules is the "notifyRules" settings section: which events send notifications.
type Rules struct {
	ServerOffline   bool `json:"serverOffline"`
	ServerRecovered bool `json:"serverRecovered"`
	AppOffline      bool `json:"appOffline"`
	AppRecovered    bool `json:"appRecovered"`
	CrashLoop       bool `json:"crashLoop"`
	HealthFailed    bool `json:"healthFailed"`
	HealthRecovered bool `json:"healthRecovered"`
	Resources       bool `json:"resources"` // CPU, RAM, disk
	RepeatedErrors  bool `json:"repeatedErrors"`
	OtherIncidents  bool `json:"otherIncidents"` // restarts, ports
	OtherResolved   bool `json:"otherResolved"`
}

func DefaultRules() Rules {
	return Rules{true, true, true, true, true, true, true, true, true, true, false}
}

// wanted maps an incident event to its rule.
func (r Rules) wanted(kind, typ string) bool {
	opened := kind == "opened"
	switch typ {
	case "server_offline":
		return pick(opened, r.ServerOffline, r.ServerRecovered)
	case "app_offline", "pm2_errored", "systemd_failed":
		return pick(opened, r.AppOffline, r.AppRecovered)
	case "crash_loop":
		return pick(opened, r.CrashLoop, r.AppRecovered)
	case "health_failed", "uptime_down":
		return pick(opened, r.HealthFailed, r.HealthRecovered)
	case "high_cpu", "high_ram", "disk_full", "anomaly", "uptime_slow":
		return pick(opened, r.Resources, r.OtherResolved)
	case "repeated_errors":
		return pick(opened, r.RepeatedErrors, r.OtherResolved)
	}
	return pick(opened, r.OtherIncidents, r.OtherResolved)
}

func pick(opened, a, b bool) bool {
	if opened {
		return a
	}
	return b
}

// HistoryItem is one delivery attempt.
type HistoryItem struct {
	ID         int64  `json:"id"`
	TS         int64  `json:"ts"`
	Event      string `json:"event"`
	Provider   string `json:"provider"`
	ServerID   string `json:"serverId"`
	AppID      string `json:"appId"`
	IncidentID string `json:"incidentId"`
	Title      string `json:"title"`
	Message    string `json:"message"`
	Result     string `json:"result"`
	Error      string `json:"error,omitempty"`
	LatencyMs  int64  `json:"latencyMs"`
	Read       bool   `json:"read"`
}

type job struct {
	event    string
	msg      Message
	serverID string
	appID    string
	incident string
	only     string // send to just this provider (tests)
}

type Service struct {
	db  *sql.DB
	box *secret.Box
	Bus *live.Bus
	// Overrides returns the server's notify override and whether the app allows
	// notifications. Optional.
	Overrides func(serverID, appID string) (serverOverride string, appAllows bool)
	// Mirror gets every alert that passed the rules, for the phone app.
	Mirror     func(msg Message, serverID, appID string)
	ServerName func(serverID string) string
	AppName    func(appID string) string
	PublicURL  string
	Push       *push.Service                    // browser push; optional
	Runbook    func(incidentType string) string // "how to fix" steps added to alerts; optional
	Now        func() time.Time

	queue chan job
	mu    sync.Mutex
	last  map[string]time.Time // cooldown per incident key + kind
}

func New(d *sql.DB, box *secret.Box, bus *live.Bus) *Service {
	return &Service{db: d, box: box, Bus: bus, Now: time.Now, queue: make(chan job, 500), last: map[string]time.Time{}}
}

// ---------- channels ----------

// ChannelView is what the UI sees: secrets are never returned, only whether set.
type ChannelView struct {
	Provider   string            `json:"provider"`
	Enabled    bool              `json:"enabled"`
	Configured bool              `json:"configured"`
	Fields     map[string]string `json:"fields"`  // non-secret values
	Secrets    map[string]bool   `json:"secrets"` // secret name → is set
}

func (s *Service) load(provider string) (enabled bool, cfg map[string]string, err error) {
	var enc []byte
	err = s.db.QueryRow(`SELECT enabled, config_enc FROM notification_channels WHERE provider = ?`, provider).Scan(&enabled, &enc)
	if errors.Is(err, sql.ErrNoRows) {
		return false, map[string]string{}, nil
	}
	if err != nil {
		return false, nil, err
	}
	cfg = map[string]string{}
	if len(enc) > 0 {
		raw, err := s.box.Open(enc, "notify."+provider)
		if err != nil {
			return false, nil, fmt.Errorf("decrypt %s config: %w", provider, err)
		}
		json.Unmarshal(raw, &cfg)
	}
	return enabled, cfg, nil
}

// Webhook URLs often embed tokens, so they are secrets like Discord's.
// Push has no fields: its devices subscribe from their own browser.
var secretFields = map[string][]string{"push": {}, "ntfy": {"token", "password"}, "telegram": {"botToken"}, "discord": {"webhook"}, "webhook": {"url", "secret"}}
var plainFields = map[string][]string{"push": {}, "ntfy": {"url", "topic", "username"}, "telegram": {"chatId"}, "discord": {}, "webhook": {}}

func (s *Service) Channels() ([]ChannelView, error) {
	out := []ChannelView{}
	for _, p := range Providers {
		enabled, cfg, err := s.load(p)
		if err != nil {
			return nil, err
		}
		v := ChannelView{Provider: p, Enabled: enabled, Configured: s.configured(p, cfg), Fields: map[string]string{}, Secrets: map[string]bool{}}
		if p == "push" && s.Push != nil {
			v.Fields["devices"] = strconv.Itoa(s.Push.Count(""))
		}
		for _, f := range plainFields[p] {
			v.Fields[f] = cfg[f]
		}
		for _, f := range secretFields[p] {
			v.Secrets[f] = cfg[f] != ""
		}
		if p == "discord" && cfg["webhook"] != "" {
			v.Fields["webhookHint"] = "…" + cfg["webhook"][max(0, len(cfg["webhook"])-6):]
		}
		if p == "webhook" && cfg["url"] != "" {
			if u, err := url.Parse(cfg["url"]); err == nil {
				v.Fields["urlHint"] = u.Scheme + "://" + u.Host + "/…" // which endpoint, without its token
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func build(provider string, cfg map[string]string) (Provider, error) {
	raw, _ := json.Marshal(cfg)
	switch provider {
	case "ntfy":
		var n Ntfy
		json.Unmarshal(raw, &n)
		return n, n.validate()
	case "telegram":
		var t Telegram
		json.Unmarshal(raw, &t)
		return t, t.validate()
	case "discord":
		var d Discord
		json.Unmarshal(raw, &d)
		return d, d.validate()
	case "webhook":
		var w Webhook
		json.Unmarshal(raw, &w)
		return w, w.validate()
	}
	return nil, ErrUnknownProvider
}

// SaveChannel updates a provider. Empty secret fields keep their stored value.
func (s *Service) SaveChannel(provider string, enabled bool, fields map[string]string) error {
	if _, ok := secretFields[provider]; !ok {
		return ErrUnknownProvider
	}
	_, cfg, err := s.load(provider)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, f := range append(append([]string{}, plainFields[provider]...), secretFields[provider]...) {
		allowed[f] = true
	}
	for k, v := range fields {
		if !allowed[k] {
			return valid.Error("unknown field " + k)
		}
		v = strings.TrimSpace(v)
		isSecret := false
		for _, sf := range secretFields[provider] {
			isSecret = isSecret || sf == k
		}
		if isSecret && v == "" {
			continue
		}
		cfg[k] = v
	}
	if provider != "push" && (enabled || len(fields) > 0) {
		if _, err := build(provider, cfg); err != nil {
			return err
		}
	}
	raw, _ := json.Marshal(cfg)
	_, err = s.db.Exec(`INSERT INTO notification_channels (provider, enabled, config_enc, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (provider) DO UPDATE SET enabled = excluded.enabled, config_enc = excluded.config_enc, updated_at = excluded.updated_at`,
		provider, enabled, s.box.Seal(raw, "notify."+provider), s.Now().UnixMilli())
	return err
}

// ---------- rules ----------

func (s *Service) Rules() Rules {
	r := DefaultRules()
	settings.Load(s.db, "notifyRules", &r)
	return r
}

func (s *Service) SaveRules(r Rules) error { return settings.Save(s.db, "notifyRules", r) }

// ---------- sending ----------

const cooldown = 10 * time.Minute

// Incident queues notifications for an incident event, applying rules, mutes,
// per-server and per-app overrides and a cooldown against flapping.
func (s *Service) Incident(ev incidents.Event) {
	x := ev.Incident
	appID := ""
	if x.AppID != nil {
		appID = *x.AppID
	}
	if ev.Kind == "opened" && x.Muted(s.Now()) {
		return
	}
	override, appAllows := "default", true
	if s.Overrides != nil {
		override, appAllows = s.Overrides(x.ServerID, appID)
	}
	switch {
	case !appAllows || override == "muted":
		return
	case override == "critical-only" && x.Severity != "critical":
		return
	case override != "all" && !s.Rules().wanted(ev.Kind, x.TypeKey):
		return
	}
	s.mu.Lock()
	ck := x.Key + "|" + ev.Kind
	if t, ok := s.last[ck]; ok && s.Now().Sub(t) < cooldown {
		s.mu.Unlock()
		return
	}
	s.last[ck] = s.Now()
	s.mu.Unlock()

	d := Details{Event: "incident." + ev.Kind, Type: x.TypeKey, TypeLabel: x.Type, IncidentID: x.ID, ServerID: x.ServerID, AppID: appID}
	if s.ServerName != nil {
		d.ServerName = s.ServerName(x.ServerID)
	}
	if appID != "" && s.AppName != nil {
		d.AppName = s.AppName(appID)
	}
	where := d.ServerName
	if d.AppName != "" {
		where = d.AppName + " on " + where
	}
	msg := Message{Title: x.Title, Severity: x.Severity, Details: d}
	if ev.Kind == "resolved" {
		msg.Title, msg.Severity = "Resolved: "+x.Title, "ok"
		msg.Body = fmt.Sprintf("%s\n%s", x.Resolution, where)
	} else {
		msg.Body = fmt.Sprintf("%s · %s · %s", x.Type, strings.ToUpper(x.Severity), where)
		if len(x.Samples) > 0 {
			msg.Body += "\n" + x.Samples[len(x.Samples)-1]
		}
		if s.Runbook != nil {
			if rb := s.Runbook(x.TypeKey); rb != "" {
				if len(rb) > 600 {
					rb = rb[:600] + "…"
				}
				msg.Body += "\n\nHow to fix:\n" + rb
			}
		}
	}
	if s.PublicURL != "" {
		msg.Link = strings.TrimRight(s.PublicURL, "/") + "/#/incidents"
	}
	event := x.Type
	if ev.Kind == "resolved" {
		event = x.Type + " resolved"
	}
	if s.Mirror != nil {
		s.Mirror(msg, x.ServerID, appID)
	}
	s.enqueue(job{event: event, msg: msg, serverID: x.ServerID, appID: appID, incident: x.ID})
}

// Notice sends a message that is not tied to one incident (e.g. "maintenance ended,
// still broken: …") to every enabled channel. Only a muted server override stops it.
func (s *Service) Notice(event string, msg Message, serverID, appID string) {
	if s.Overrides != nil {
		if override, _ := s.Overrides(serverID, ""); override == "muted" {
			return
		}
	}
	if s.PublicURL != "" && msg.Link == "" {
		msg.Link = strings.TrimRight(s.PublicURL, "/") + "/#/incidents"
	}
	if s.Mirror != nil {
		s.Mirror(msg, serverID, appID)
	}
	s.enqueue(job{event: event, msg: msg, serverID: serverID, appID: appID})
}

func (s *Service) enqueue(j job) {
	select {
	case s.queue <- j:
	default:
		slog.Warn("notification queue full; dropping", "event", j.event)
	}
}

// Test sends a test message through one provider synchronously.
// configured reports whether a channel can send: its settings are saved, or for push,
// at least one device is subscribed.
func (s *Service) configured(provider string, cfg map[string]string) bool {
	if provider == "push" {
		return s.Push != nil && s.Push.Count("") > 0
	}
	return len(cfg) > 0
}

func (s *Service) provider(name string, cfg map[string]string) (Provider, error) {
	if name == "push" {
		if s.Push == nil {
			return nil, ErrUnknownProvider
		}
		return pushProvider{s.Push}, nil
	}
	return build(name, cfg)
}

// pushProvider sends to every device subscribed in Settings -> Notifications.
type pushProvider struct{ s *push.Service }

func (p pushProvider) Send(ctx context.Context, m Message) error {
	tag := m.Details.IncidentID
	if tag == "" {
		tag = m.Details.Event
	}
	return p.s.Send(ctx, push.Message{Title: m.Title, Body: m.Body, URL: "/#/incidents", Tag: tag, Severity: m.Severity})
}

func (s *Service) Test(ctx context.Context, provider string) (HistoryItem, error) {
	_, cfg, err := s.load(provider)
	if err != nil {
		return HistoryItem{}, err
	}
	if !s.configured(provider, cfg) {
		return HistoryItem{}, valid.Error("save the " + provider + " settings first")
	}
	return s.deliver(ctx, provider, cfg, job{event: "Test", msg: Message{Title: "Fleetwatch test notification", Body: "If you can read this, " + provider + " notifications work.", Severity: "info", Details: Details{Event: "test"}}}), nil
}

// Run delivers queued notifications until ctx ends.
func (s *Service) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-s.queue:
			for _, p := range Providers {
				enabled, cfg, err := s.load(p)
				if err != nil || !enabled || !s.configured(p, cfg) {
					continue
				}
				if h := s.deliver(ctx, p, cfg, j); h.Result == "failed" {
					// One retry after a short pause covers transient network errors.
					select {
					case <-ctx.Done():
						return
					case <-time.After(20 * time.Second):
					}
					s.deliver(ctx, p, cfg, j)
				}
			}
		}
	}
}

func (s *Service) deliver(ctx context.Context, provider string, cfg map[string]string, j job) HistoryItem {
	h := HistoryItem{TS: s.Now().UnixMilli(), Event: j.event, Provider: provider, ServerID: j.serverID, AppID: j.appID, IncidentID: j.incident,
		Title: j.msg.Title, Message: j.msg.Body, Result: "delivered"}
	p, err := s.provider(provider, cfg)
	if err == nil {
		start := time.Now()
		sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = p.Send(sctx, j.msg)
		cancel()
		h.LatencyMs = time.Since(start).Milliseconds()
	}
	if err != nil {
		h.Result, h.Error = "failed", trunc(err.Error(), 300)
		slog.Warn("notification failed", "provider", provider, "err", h.Error)
	}
	res, _ := s.db.Exec(`INSERT INTO notification_history (ts, event, provider, server_id, app_id, incident_id, title, message, result, error, latency_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		h.TS, h.Event, h.Provider, h.ServerID, h.AppID, h.IncidentID, h.Title, h.Message, h.Result, h.Error, h.LatencyMs)
	if res != nil {
		h.ID, _ = res.LastInsertId()
	}
	s.Bus.Publish("notification", h)
	return h
}

// ---------- history ----------

func (s *Service) History(limit int, before int64) ([]HistoryItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.Query(`SELECT id, ts, event, provider, COALESCE(server_id, ''), COALESCE(app_id, ''), COALESCE(incident_id, ''), title, message, result, error, latency_ms, read
		FROM notification_history WHERE (? = 0 OR id < ?) ORDER BY id DESC LIMIT ?`, before, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryItem{}
	for rows.Next() {
		var h HistoryItem
		if err := rows.Scan(&h.ID, &h.TS, &h.Event, &h.Provider, &h.ServerID, &h.AppID, &h.IncidentID, &h.Title, &h.Message, &h.Result, &h.Error, &h.LatencyMs, &h.Read); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *Service) MarkRead(id int64) error {
	_, err := s.db.Exec(`UPDATE notification_history SET read = 1 WHERE (? = 0 OR id = ?)`, id, id)
	return err
}

// Prune keeps 30 days of history.
func (s *Service) Prune(now time.Time) error {
	_, err := s.db.Exec(`DELETE FROM notification_history WHERE ts < ?`, now.Add(-30*24*time.Hour).UnixMilli())
	return err
}
