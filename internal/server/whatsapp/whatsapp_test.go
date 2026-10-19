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

func TestAlertSound(t *testing.T) {
	src, frames := alertSound(), 0
	for {
		f, err := src.ReadFrame()
		if err != nil {
			break
		}
		if len(f) != 960 {
			t.Fatal("frame size", len(f))
		}
		frames++
	}
	if frames < 40 || frames > 100 { // about 4 seconds of 60 ms frames
		t.Fatal(frames)
	}
	s := New(nil, t.TempDir(), nil)
	if _, err := s.Ring(t.Context(), "+212612345678", time.Second, nil); err != ErrNotLinked {
		t.Fatal(err)
	}
}

func TestStream(t *testing.T) {
	st := &stream{}
	if f, err := st.ReadFrame(); err != nil || len(f) != 960 || f[0] != 0 {
		t.Fatal("silence when empty")
	}
	st.push(make([]float32, 1000))
	st.push([]float32{0.5})
	st.ReadFrame()
	if f, _ := st.ReadFrame(); f[40] != 0.5 || f[41] != 0 {
		t.Fatal("frames out of order")
	}
	st.push(make([]float32, 5000))
	st.flush()
	if f, _ := st.ReadFrame(); len(f) != 960 || len(st.buf) != 0 {
		t.Fatal("not flushed")
	}
}
