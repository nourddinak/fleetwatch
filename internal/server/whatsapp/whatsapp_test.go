package whatsapp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"fleetwatch/internal/server/db"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"google.golang.org/protobuf/proto"
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

func TestCallerOf(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := New(d, dir, nil)
	s.SetPerson("mem_1", Person{Phone: "+212 6 12 34 56 78"})
	if who, num := s.callerOf(t.Context(), nil, types.NewJID("212612345678", types.DefaultUserServer)); who != "mem_1" || num != "212612345678" {
		t.Fatal("a saved number is not recognised", who, num)
	}
	if who, _ := s.callerOf(t.Context(), nil, types.NewJID("15551234567", types.DefaultUserServer)); who != "" {
		t.Fatal("a stranger was taken for someone", who)
	}
}

func TestWatchdogAndOptions(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := New(d, dir, nil)
	now := time.Now()
	s.Now = func() time.Time { return now }
	alerts := make(chan string, 4)
	s.Alert = func(title, _, _ string) { alerts <- title }
	s.client = &whatsmeow.Client{} // linked, as far as the watchdog cares
	s.onEvent(&events.Disconnected{})
	now = now.Add(time.Minute)
	s.Watch()
	now = now.Add(3 * time.Minute)
	s.Watch()
	s.Watch()
	if got := <-alerts; got != "WhatsApp is disconnected" || len(alerts) != 0 {
		t.Fatal("watchdog:", got, len(alerts))
	}
	s.onEvent(&events.Connected{})
	if got := <-alerts; got != "WhatsApp is back" {
		t.Fatal(got)
	}
	if o := s.Options(); !o.ResolvedMessages || o.FollowUpMinutes != 20 {
		t.Fatal("defaults", o)
	}
	if _, err := s.SetOptions(Options{FollowUpMinutes: -1}); err == nil {
		t.Fatal("a negative follow-up was accepted")
	}
	if o, _ := s.SetOptions(Options{FollowUpMinutes: 0}); s.Options() != o {
		t.Fatal("not saved")
	}
}

func TestBriefingDue(t *testing.T) {
	loc, _ := time.LoadLocation("Africa/Casablanca")
	at := func(hm string) time.Time {
		h, _ := time.ParseInLocation("15:04", hm, loc)
		return time.Date(2026, 10, 19, h.Hour(), h.Minute(), 0, 0, loc)
	}
	p := Person{Briefing: "08:00", TimeZone: "Africa/Casablanca"}
	if _, due := p.BriefingDue(at("07:59"), ""); due {
		t.Fatal("before its time")
	}
	day, due := p.BriefingDue(at("08:00"), "")
	if !due || day != "2026-10-19" {
		t.Fatal("at its time", day, due)
	}
	if _, due := p.BriefingDue(at("08:05"), day); due {
		t.Fatal("twice the same day")
	}
	if _, due := p.BriefingDue(at("09:00"), ""); due {
		t.Fatal("hours late: a missed briefing is skipped, not sent at lunch")
	}
	if _, due := (Person{}).BriefingDue(at("08:00"), ""); due {
		t.Fatal("no briefing chosen")
	}
	if late := (Person{Briefing: "23:50"}); func() bool { _, d := late.BriefingDue(time.Date(2026, 10, 19, 23, 55, 0, 0, time.UTC), ""); return d }() == false {
		t.Fatal("a late-evening briefing")
	}
}

func TestPINRules(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := New(d, dir, nil)
	for _, bad := range []string{"0000", "5555", "123", "12345", "12a4", "1234", "4321", "6789", "0123"} {
		if s.SetPIN("owner", bad) == nil {
			t.Fatal("accepted", bad)
		}
	}
	if err := s.SetPIN("owner", "4821"); err != nil || !s.HasPIN("owner") {
		t.Fatal(err)
	}
}

func TestWeeklyDue(t *testing.T) {
	mon := time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC) // a Monday
	p := Person{Weekly: true}
	if day, due := p.WeeklyDue(mon, ""); !due || day != "2026-10-19" {
		t.Fatal("Monday 09:00", day, due)
	}
	if _, due := p.WeeklyDue(mon.AddDate(0, 0, 1), ""); due {
		t.Fatal("only on Mondays")
	}
	if _, due := (Person{Weekly: true, Briefing: "07:30"}).WeeklyDue(mon, ""); due {
		t.Fatal("at the briefing's time when there is one")
	}
	if _, due := (Person{}).WeeklyDue(mon, ""); due {
		t.Fatal("not asked for")
	}
}

func TestToOpus(t *testing.T) {
	if !CanSpeak() {
		t.Skip("no ffmpeg here")
	}
	ogg, err := toOpus(context.Background(), make([]byte, 2*24000), 24000)
	if err != nil || !bytes.HasPrefix(ogg, []byte("OggS")) {
		t.Fatal(err, len(ogg))
	}
}

// Voice answers ask the root helper for ffmpeg; a failure shows, and is not retried at once.
func TestFFmpegRequest(t *testing.T) {
	had := CanSpeak
	CanSpeak = func() bool { return false }
	defer func() { CanSpeak = had }()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := New(d, dir, nil)
	s.DataDir, s.HelperPath = dir, filepath.Join(dir, "helper.path")
	if s.VoiceSetup().State != "manual" {
		t.Fatal("no helper: install it by hand")
	}
	os.WriteFile(s.HelperPath, nil, 0o644)
	if _, err := s.SetPerson("owner", Person{Phone: "+212600000001", Language: "en", Replies: "voice"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40 && !FFmpegRequested(dir); i++ {
		time.Sleep(50 * time.Millisecond)
	}
	if !FFmpegRequested(dir) || s.VoiceSetup().State != "installing" {
		t.Fatal("asked the helper", s.VoiceSetup())
	}
	installer = func(context.Context) ([]byte, error) {
		return []byte("E: Unable to locate package ffmpeg"), errors.New("exit status 100")
	}
	if InstallFFmpeg(dir) == nil || FFmpegRequested(dir) {
		t.Fatal("the helper reports and clears the request")
	}
	if v := s.VoiceSetup(); v.State != "failed" || !strings.Contains(v.Error, "Unable to locate") {
		t.Fatal(v)
	}
	s.EnsureVoice()
	time.Sleep(100 * time.Millisecond)
	if FFmpegRequested(dir) {
		t.Fatal("no retry loop after a failure")
	}
}

// In the team group, only mentions of the linked number (or replies to it) are for the AI.
func TestForBot(t *testing.T) {
	me := types.NewJID("212600000001", types.DefaultUserServer)
	lid := types.NewJID("123456789", types.HiddenUserServer)
	cl := &whatsmeow.Client{Store: &store.Device{ID: &me, LID: lid}}
	msg := func(ci *waE2E.ContextInfo) *waE2E.Message {
		return &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{Text: proto.String("@123456789 is the API down?"), ContextInfo: ci}}
	}
	if !forBot(cl, msg(&waE2E.ContextInfo{MentionedJID: []string{lid.String()}})) {
		t.Fatal("a mention of its LID")
	}
	if !forBot(cl, msg(&waE2E.ContextInfo{Participant: proto.String(me.String()), QuotedMessage: &waE2E.Message{Conversation: proto.String("🚨 down")}})) {
		t.Fatal("a reply to one of its messages")
	}
	if forBot(cl, msg(&waE2E.ContextInfo{MentionedJID: []string{"212600000002@s.whatsapp.net"}})) || forBot(cl, &waE2E.Message{Conversation: proto.String("hi all")}) {
		t.Fatal("someone else, or nobody")
	}
	if got := strings.TrimSpace(mentionRe.ReplaceAllString("@123456789 is the API down?", "")); got != "is the API down?" {
		t.Fatal(got)
	}
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := New(d, t.TempDir(), nil)
	if _, err := s.SetGroupOptions("all", true); err == nil {
		t.Fatal("no group yet")
	}
}

// Paused: nothing goes out; someone off the team is not reached; Forget drops them.
func TestPauseAndTeam(t *testing.T) {
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := New(d, dir, nil)
	if s.Paused() || s.SetPaused(true) != nil || !s.Paused() || !s.Status().Paused {
		t.Fatal("pause")
	}
	if s.Send(t.Context(), "+212612345678", "hi") != ErrPaused {
		t.Fatal("a paused WhatsApp sends nothing")
	}
	if _, err := s.Dial(t.Context(), "+212612345678", time.Second, nil); err != ErrPaused {
		t.Fatal("nor calls", err)
	}
	s.SetPaused(false)
	for id, phone := range map[string]string{"owner": "+212600000001", "mem_a": "+212600000002", "mem_b": "+212600000003"} {
		if _, err := s.SetPerson(id, Person{Phone: phone}); err != nil {
			t.Fatal(err)
		}
	}
	s.Team = func(id string) bool { return id != "mem_b" } // mem_b was disabled
	if _, ok := s.PersonOf("mem_b"); ok || len(s.People()) != 2 {
		t.Fatal("a disabled member is not reached")
	}
	s.Team = nil
	if _, ok := s.PersonOf("mem_b"); !ok {
		t.Fatal("their number is kept, for when they are back")
	}
	note, _ := s.LeaveNote("owner", "mem_a", "hi", "later", false)
	s.Forget("mem_a")
	if _, ok := s.PersonOf("mem_a"); ok || len(s.Waiting("mem_a")) != 0 || s.Deliver(note) {
		t.Fatal("a removed member is forgotten")
	}
}
