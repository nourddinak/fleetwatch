package statuspage

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"html"
	"slices"
	"strconv"
	"strings"
	"time"

	"fleetwatch/internal/server/auth"
	"fleetwatch/internal/server/incidents"
	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

// ---------- private pages ----------

const keySetting = "statusPageKey"

func loadKey(d *sql.DB) []byte {
	var h string
	settings.Load(d, keySetting, &h)
	if k, err := hex.DecodeString(h); err == nil && len(k) == 32 {
		return k
	}
	k := make([]byte, 32)
	rand.Read(k)
	settings.Save(d, keySetting, hex.EncodeToString(k))
	return k
}

// CookieName is the cookie that remembers an unlocked password page.
func CookieName(c Config) string { return "fw_sp_" + c.ID }

// UnlockToken is that cookie's value. It changes with the password, so a new
// password locks everyone out again.
func (s *Service) UnlockToken(c Config) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(c.ID + "\x00" + c.PasswordHash))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Service) Unlocked(c Config, cookie string) bool {
	return c.PasswordHash != "" && hmac.Equal([]byte(cookie), []byte(s.UnlockToken(c)))
}

func (s *Service) CheckPassword(c Config, pw string) bool {
	ok, _ := auth.VerifyPassword(c.PasswordHash, pw)
	return c.PasswordHash != "" && ok
}

// ---------- badge ----------

var badgeLook = map[string][2]string{
	"operational": {"operational", "#16a34a"},
	"degraded":    {"degraded", "#d97706"},
	"partial":     {"partial outage", "#ea580c"},
	"major":       {"major outage", "#dc2626"},
	"maintenance": {"maintenance", "#2563eb"},
}

// Badge is a small "status | operational" image for READMEs and websites.
func Badge(overall, label string) []byte {
	look, ok := badgeLook[overall]
	if !ok {
		look = [2]string{"unknown", "#6b7280"}
	}
	label = strings.TrimSpace(label)
	if label == "" || len(label) > 40 {
		label = "status"
	}
	w := func(s string) int { return 10 + int(float64(len([]rune(s)))*6.6) }
	lw, mw := w(label), w(look[0])
	l, m := html.EscapeString(label), html.EscapeString(look[0])
	return []byte(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%[1]d" height="20" role="img" aria-label="%[3]s: %[4]s"><title>%[3]s: %[4]s</title>`+
		`<linearGradient id="s" x2="0" y2="100%%"><stop offset="0" stop-color="#bbb" stop-opacity=".1"/><stop offset="1" stop-opacity=".1"/></linearGradient>`+
		`<clipPath id="r"><rect width="%[1]d" height="20" rx="3" fill="#fff"/></clipPath><g clip-path="url(#r)">`+
		`<rect width="%[2]d" height="20" fill="#555"/><rect x="%[2]d" width="%[5]d" height="20" fill="%[6]s"/><rect width="%[1]d" height="20" fill="url(#s)"/></g>`+
		`<g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" font-size="11">`+
		`<text x="%[7]d" y="14">%[3]s</text><text x="%[8]d" y="14">%[4]s</text></g></svg>`,
		lw+mw, lw, l, m, mw, look[1], lw/2, lw+mw/2))
}

// ---------- monthly report ----------

type ReportRow struct {
	Group       string   `json:"group"`
	Name        string   `json:"name"`
	Uptime      *float64 `json:"uptime"` // nil: did not exist that month
	DownMinutes int      `json:"downMinutes"`
	Outages     int      `json:"outages"`
}

type Report struct {
	Page  string      `json:"page"`
	Month string      `json:"month"` // YYYY-MM
	From  int64       `json:"from"`
	To    int64       `json:"to"` // the end of the month, or now for the running month
	Rows  []ReportRow `json:"rows"`
}

// Report is the uptime of every component in a month (UTC).
func (s *Service) Report(id, month string) (Report, error) {
	c, ok := s.Page(id)
	if !ok {
		return Report{}, ErrNotFound
	}
	start, err := time.Parse("2006-01", month)
	if err != nil {
		return Report{}, valid.Error("month must look like 2026-09")
	}
	now := s.Now()
	if start.After(now) {
		return Report{}, valid.Error("that month has not started yet")
	}
	end := min(start.AddDate(0, 1, 0).UnixMilli(), now.UnixMilli())
	r := Report{Page: c.Title, Month: month, From: start.UnixMilli(), To: end, Rows: []ReportRow{}}
	for _, g := range c.Groups {
		for _, x := range g.Components {
			row := ReportRow{Group: g.Name, Name: x.Name}
			related, created := s.related(x)
			row.Uptime, row.DownMinutes, row.Outages = rangeUptime(related, created, r.From, r.To, now)
			r.Rows = append(r.Rows, row)
		}
	}
	return r, nil
}

// related is the incidents that count against a component, and when it appeared.
func (s *Service) related(x Component) ([]incidents.Incident, int64) {
	var related []incidents.Incident
	var created int64
	switch x.Kind {
	case "app":
		if s.AppState == nil {
			return nil, 0
		}
		_, serverID, cr, ok := s.AppState(x.ID)
		if !ok {
			return nil, 0
		}
		created = cr
		related = s.incidents(incidents.Filter{AppID: x.ID})
		related = append(related, s.incidents(incidents.Filter{ServerID: serverID, Type: "server_offline"})...)
	case "server":
		if s.ServerState == nil {
			return nil, 0
		}
		_, cr, ok := s.ServerState(x.ID)
		if !ok {
			return nil, 0
		}
		created = cr
		for _, i := range s.incidents(incidents.Filter{ServerID: x.ID}) {
			if i.AppID == nil {
				related = append(related, i)
			}
		}
	}
	return related, created
}

// rangeUptime is the uptime between from and to: percent (nil if the component did
// not exist), minutes down and the number of separate outages.
func rangeUptime(list []incidents.Incident, created, from, to int64, now time.Time) (*float64, int, int) {
	var down []span
	for _, i := range list {
		if !downTypes[i.TypeKey] {
			continue
		}
		end := i.ResolvedAt
		if end == 0 {
			end = now.UnixMilli()
		}
		down = append(down, span{i.FirstAt, end})
	}
	down = merge(down)
	from = max(from, created)
	if to <= from {
		return nil, 0, 0
	}
	lost := overlap(down, from, to)
	outages := 0
	for _, d := range down {
		if d.to > from && d.from < to {
			outages++
		}
	}
	pct := 100 * float64(to-from-lost) / float64(to-from)
	pct = float64(int(pct*1000)) / 1000
	return &pct, int((lost + 59_999) / 60_000), outages
}

func (r Report) CSV() []byte {
	var b bytes.Buffer
	w := csv.NewWriter(&b)
	w.Write([]string{"Group", "Component", "Uptime %", "Minutes down", "Outages"})
	for _, x := range r.Rows {
		up := "no data"
		if x.Uptime != nil {
			up = strconv.FormatFloat(*x.Uptime, 'f', 3, 64)
		}
		w.Write([]string{x.Group, x.Name, up, strconv.Itoa(x.DownMinutes), strconv.Itoa(x.Outages)})
	}
	w.Flush()
	return b.Bytes()
}

// EmailReport sends a month's report, with the CSV attached.
func (s *Service) EmailReport(ctx context.Context, id, month, to string) error {
	if s.Mail == nil {
		return valid.Error("email is not set up")
	}
	addrs, err := parseAddresses(to)
	if err != nil {
		return err
	}
	r, err := s.Report(id, month)
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Uptime of %s in %s (UTC):\n\n", r.Page, month)
	rows := slices.Clone(r.Rows)
	for _, x := range rows {
		up := "no data"
		if x.Uptime != nil {
			up = strconv.FormatFloat(*x.Uptime, 'f', 3, 64) + "%"
		}
		name := x.Name
		if x.Group != "" {
			name = x.Group + " / " + x.Name
		}
		fmt.Fprintf(&b, "%s: %s (%d min down, %d outage(s))\n", name, up, x.DownMinutes, x.Outages)
	}
	b.WriteString("\nThe same figures are attached as a CSV file.\n")
	return s.Mail.Send(ctx, Mail{To: addrs, Subject: fmt.Sprintf("Uptime report %s: %s", month, r.Page), Text: b.String(),
		Attached: &Attachment{Name: fmt.Sprintf("uptime-%s.csv", month), Type: "text/csv; charset=utf-8", Data: r.CSV()}})
}
