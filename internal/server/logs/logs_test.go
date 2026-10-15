package logs

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"fleetwatch/internal/proto"
	"fleetwatch/internal/server/db"
)

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"\x1b[31mred\x1b[0m text":               "red text",
		"title\x1b]0;pwned\x07 after":           "title after",
		"bell\x07 back\bspace\x00null\ttab":     "bell backspacenull\ttab",
		"cursor \x1b[2J\x1b[Hhome":              "cursor home",
		"bad utf8 \xff\xfe end":                 "bad utf8 � end",
		"c1 control \u0085 \u009b31m kept text": "c1 control  31m kept text",
	}
	for in, want := range cases {
		if got := Sanitize(in, 1000); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("é", 100) // 2 bytes each
	got := Sanitize(long, 51)
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "…[truncated]") || len(got) > 51+len(" …[truncated]") {
		t.Fatalf("truncation broke UTF-8 or limit: %q", got)
	}
}

func TestSanitizeOutput(t *testing.T) {
	in := "\x1b[32m[PM2] \x1b[39mApplying action\r\nline two\ttab\x07\x1b]0;title\x07\n"
	if got := SanitizeOutput(in); got != "[PM2] Applying action\nline two\ttab\n" {
		t.Fatalf("%q", got)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		line proto.LogLine
		want string
	}{
		{proto.LogLine{Priority: 3, Stream: "journal", Message: "anything"}, "error"},
		{proto.LogLine{Priority: 6, Stream: "journal", Message: "ERROR in text but journald says info"}, "info"},
		{proto.LogLine{Priority: 0, Message: "x"}, "critical"},
		{proto.LogLine{Priority: 7, Message: "x"}, "debug"},
		{proto.LogLine{Priority: -1, Stream: "stdout", Message: `{"level":50,"msg":"db down"}`}, "error"},
		{proto.LogLine{Priority: -1, Stream: "stderr", Message: `{"level":"info","message":"ready"}`}, "info"},
		{proto.LogLine{Priority: -1, Stream: "stdout", Message: "2026-10-15T10:00:00Z ERROR connect ECONNREFUSED"}, "error"},
		{proto.LogLine{Priority: -1, Stream: "stdout", Message: "[warn] slow query"}, "warning"},
		{proto.LogLine{Priority: -1, Stream: "stdout", Message: "level=debug msg=tick"}, "debug"},
		{proto.LogLine{Priority: -1, Stream: "stderr", Message: "Traceback (most recent call last):"}, "error"},
		{proto.LogLine{Priority: -1, Stream: "stderr", Message: "TypeError: x is undefined"}, "error"},
		{proto.LogLine{Priority: -1, Stream: "stdout", Message: "GET /api/errors 200"}, "info"}, // "errors" is not a level word
		{proto.LogLine{Priority: -1, Stream: "stdout", Message: "listening on 5002"}, "info"},
		{proto.LogLine{Priority: -1, Stream: "stderr", Message: "Starting worker"}, "warning"},
		{proto.LogLine{Priority: -1, Stream: "journal", Message: "plain"}, "unknown"},
	}
	for _, c := range cases {
		if got := Classify(c.line, c.line.Message); got != c.want {
			t.Errorf("%q (%s, prio %d): got %s want %s", c.line.Message, c.line.Stream, c.line.Priority, got, c.want)
		}
	}
}

func setup(t *testing.T) *Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	for _, q := range []string{
		`INSERT INTO servers (id, name, created_at) VALUES ('s1', 'one', 0), ('s2', 'two', 0)`,
		`INSERT INTO apps (id, server_id, name, manager, process_key, created_at) VALUES ('a1', 's1', 'api', 'pm2', 'api', 0), ('a2', 's2', 'other', 'pm2', 'other', 0)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return NewStore(d)
}

func TestStoreWriteSearchPrune(t *testing.T) {
	s := setup(t)
	sub := s.Live.Subscribe("a1", "")
	now := time.Now().UnixMilli()
	var lines []proto.LogLine
	for i := 0; i < 30; i++ {
		lines = append(lines, proto.LogLine{TS: now - int64(30-i)*1000, AppID: "a1", Source: "pm2", Stream: "stdout", Priority: -1, Message: "GET /api/player 200 12ms"})
	}
	lines = append(lines,
		proto.LogLine{TS: now, AppID: "a1", Source: "pm2", Stream: "stderr", Priority: -1, Message: "Error: connect ECONNREFUSED 127.0.0.1:6379"},
		proto.LogLine{TS: now, AppID: "a2", Source: "pm2", Stream: "stdout", Priority: -1, Message: "spoofed line for another server's app"},
		proto.LogLine{TS: now, Source: "agent", Stream: "stderr", Priority: 4, Message: "agent dropped 5 log lines"},
		proto.LogLine{TS: now + 3_600_000, AppID: "a1", Source: "evil", Stream: "evil", Priority: -1, Message: "\x1b[31mfrom the future\x1b[0m"},
	)
	stored, err := s.write([]batch{{"s1", lines}})
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 33 {
		t.Fatalf("stored %d lines, want 33 (the other server's app line is refused)", len(stored))
	}
	last := stored[len(stored)-1]
	if last.TS > now+60_000 || last.Source != "pm2" || last.Stream != "stdout" || last.Message != "from the future" {
		t.Fatalf("not normalized: %+v", last)
	}
	s.Live.Publish(stored)
	select {
	case got := <-sub.C:
		if len(got) != 32 { // the agent notice has no app
			t.Fatalf("live got %d lines", len(got))
		}
	default:
		t.Fatal("no live delivery")
	}

	search := func(q Query) []Line {
		t.Helper()
		out, err := s.Search(q)
		if err != nil {
			t.Fatalf("%+v: %v", q, err)
		}
		return out
	}
	if r := search(Query{Text: "ECONNREFUSED"}); len(r) != 1 || r[0].Severity != "error" {
		t.Fatalf("full text: %+v", r)
	}
	if r := search(Query{Text: "econnref"}); len(r) != 1 {
		t.Fatalf("prefix search: %d", len(r))
	}
	if r := search(Query{Text: "6379"}); len(r) != 1 {
		t.Fatalf("port search: %d", len(r))
	}
	// FTS5 syntax in user input is treated as plain text, never as a query error.
	for _, q := range []string{`"`, `a" OR "b`, `NEAR(a b)`, `*`, `-x`, `col:val`, `') ; DROP TABLE logs; --`, "nul\x00byte", `^start`} {
		search(Query{Text: q})
	}
	// Hostile FTS input must not error or match everything.
	for _, q := range []string{`"`, `OR`, `NEAR(a b)`, `*`, `a" OR "b`, `message:x`, `-`, `)`} {
		if _, err := s.Search(Query{Text: q}); err != nil {
			t.Errorf("query %q: %v", q, err)
		}
	}
	if r := search(Query{AppID: "a1", Severities: []string{"error", "critical"}}); len(r) != 1 {
		t.Fatalf("severity filter: %d", len(r))
	}
	if r := search(Query{ServerID: "s1", Sources: []string{"agent"}}); len(r) != 1 || r[0].AppID != "" {
		t.Fatalf("agent notices: %+v", r)
	}
	// Cursor pagination walks every line exactly once, newest first.
	seen := map[int64]bool{}
	var before int64
	for page := 0; page < 10; page++ {
		r := search(Query{AppID: "a1", Limit: 7, Before: before})
		for i, l := range r {
			if seen[l.ID] || (i > 0 && l.ID >= r[i-1].ID) {
				t.Fatalf("page %d not strictly descending / duplicate", page)
			}
			seen[l.ID] = true
		}
		if len(r) < 7 {
			break
		}
		before = r[len(r)-1].ID
	}
	if len(seen) != 32 {
		t.Fatalf("paged over %d lines, want 32", len(seen))
	}

	if err := s.RefreshErrorCounts(time.Now()); err != nil || s.ErrorCount("a1") != 1 {
		t.Fatalf("error count: %d %v", s.ErrorCount("a1"), err)
	}
	// Retention removes old lines and their full-text entries.
	n, err := s.Prune(context.Background(), time.Now().Add(8*24*time.Hour))
	if err != nil || n != 33 {
		t.Fatalf("prune: %d %v", n, err)
	}
	if r := search(Query{Text: "ECONNREFUSED"}); len(r) != 0 {
		t.Fatal("full-text index kept a deleted line")
	}
}

func TestSettingsValidation(t *testing.T) {
	if DefaultSettings().Validate() != nil {
		t.Fatal("defaults invalid")
	}
	for _, c := range []Settings{{RetentionDays: 2, MaxLineKB: 16}, {RetentionDays: 7, MaxLineKB: 1000}} {
		if c.Validate() == nil {
			t.Errorf("%+v accepted", c)
		}
	}
}
