package whatsapp

import (
	"path/filepath"
	"testing"
	"time"

	"fleetwatch/internal/server/db"
)

func TestPeopleAndQuietHours(t *testing.T) {
	for in, want := range map[string]string{"+212 6 12-34-56-78": "212612345678", "00212612345678": "212612345678", "0612345678": "", "12": ""} {
		if got, _ := CleanPhone(in); got != want {
			t.Errorf("%s: %q", in, got)
		}
	}
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := New(d, dir, nil)
	if s.Linked() || s.Send(t.Context(), "+212612345678", "hi") != ErrNotLinked {
		t.Fatal("sends without a link")
	}
	if _, err := s.SetPerson("mem_1", Person{Phone: "+212612345678", Language: "xx"}); err == nil {
		t.Fatal("unknown language accepted")
	}
	if _, err := s.SetPerson("mem_1", Person{Phone: "+212612345678", QuietFrom: "22:00"}); err == nil {
		t.Fatal("half quiet hours accepted")
	}
	p, err := s.SetPerson("mem_1", Person{Phone: "+212612345678", Language: "fr", QuietFrom: "22:00", QuietTo: "07:00", TimeZone: "Africa/Casablanca"})
	if err != nil || p.Phone != "212612345678" {
		t.Fatal(p, err)
	}
	if got, ok := s.PersonOf("mem_1"); !ok || got.Language != "fr" || len(s.People()) != 1 {
		t.Fatal(got, ok)
	}
	loc, _ := time.LoadLocation("Africa/Casablanca")
	for at, quiet := range map[string]bool{"23:30": true, "03:00": true, "07:00": false, "12:00": false} {
		h, _ := time.ParseInLocation("15:04", at, loc)
		if p.Quiet(time.Date(2026, 10, 19, h.Hour(), h.Minute(), 0, 0, loc)) != quiet {
			t.Errorf("%s quiet = %v", at, !quiet)
		}
	}
	if _, err := s.SetPerson("mem_1", Person{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.PersonOf("mem_1"); ok {
		t.Fatal("not removed")
	}
}
