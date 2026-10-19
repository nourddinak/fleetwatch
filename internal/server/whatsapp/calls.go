package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/purpshell/meowcaller"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// Calls go through meowcaller, WhatsApp's voice calls in pure Go. Audio is 16 kHz mono,
// in frames of meowcaller.FrameSamples.

// Media is an answered call's audio, for whoever speaks on it.
type Media struct {
	In    <-chan []float32 // the person's voice, 16 kHz frames; closed when the call ends
	Say   func([]float32)  // queue 16 kHz audio for them
	Hush  func()           // drop what is queued (they talked over it)
	Alert func()           // queue the alert sound
}

// Talker speaks on an answered call; the call ends when it returns.
type Talker func(ctx context.Context, m Media)

// TalkerFor is how a call to a person is answered (set by the dashboard: the AI when it
// is set up); nil, or a nil Talker, plays the alert sound.
func (s *Service) TalkerFor(person, incident string) Talker {
	if s.Talker == nil {
		return nil
	}
	return s.Talker(person, incident)
}

// Ring calls a phone number on WhatsApp for up to wait. When they answer, talk speaks
// with them, or without it the alert sound plays; then the call ends. It reports
// whether they answered.
func (s *Service) Ring(ctx context.Context, phone string, wait time.Duration, talk Talker) (bool, error) {
	digits, err := CleanPhone(phone)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	caller, cl := s.caller, s.client
	s.mu.Unlock()
	if caller == nil || cl == nil || cl.Store.ID == nil {
		return false, ErrNotLinked
	}
	if digits == cl.Store.ID.User {
		return false, ErrSelf
	}
	if !cl.IsConnected() {
		return false, errors.New("WhatsApp is not connected right now")
	}
	call, err := caller.Call(ctx, "+"+digits)
	if err != nil {
		return false, fmt.Errorf("the call did not go out: %w", err)
	}
	var acceptOnce, readyOnce sync.Once
	accepted, ready := make(chan struct{}), make(chan struct{})
	ended := make(chan struct{})
	var endOnce sync.Once
	call.OnPeerAccept(func() { acceptOnce.Do(func() { close(accepted) }) })
	call.OnReady(func() { readyOnce.Do(func() { close(ready) }) })
	call.OnEnd(func(string) { endOnce.Do(func() { close(ended) }) })
	defer call.Hangup()

	select {
	case <-accepted:
	case <-ended: // declined or busy
		return false, nil
	case <-time.After(wait):
		return false, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
	select { // sound sent before the media flows would be lost
	case <-ready:
	case <-ended:
		return true, nil
	case <-time.After(8 * time.Second):
	}
	if talk != nil {
		talkOn(ctx, call, ended, talk)
		return true, nil
	}
	done := make(chan struct{})
	var doneOnce sync.Once
	player := call.Play(alertSound())
	player.OnFinish(func() { doneOnce.Do(func() { close(done) }) })
	select {
	case <-done:
		time.Sleep(300 * time.Millisecond) // the last frames leave
	case <-ended:
	case <-time.After(20 * time.Second):
	}
	return true, nil
}

// samples plays a buffer of 16 kHz audio.
type samples struct {
	buf []float32
	pos int
}

func (a *samples) ReadFrame() ([]float32, error) {
	if a.pos >= len(a.buf) {
		return nil, io.EOF
	}
	f := make([]float32, meowcaller.FrameSamples)
	a.pos += copy(f, a.buf[a.pos:])
	return f, nil
}

func (a *samples) Close() error { return nil }

// alertSound is a two-note chime, four times: what is heard until the AI speaks.
func alertSound() meowcaller.AudioSource {
	const rate = meowcaller.SampleRate
	var buf []float32
	silence := func(ms int) { buf = append(buf, make([]float32, rate*ms/1000)...) }
	tone := func(hz float64, ms int) {
		n := rate * ms / 1000
		fade := rate / 100 // 10 ms in and out, so it does not click
		for i := 0; i < n; i++ {
			v := 0.35 * math.Sin(2*math.Pi*hz*float64(i)/rate)
			if i < fade {
				v *= float64(i) / float64(fade)
			} else if n-i < fade {
				v *= float64(n-i) / float64(fade)
			}
			buf = append(buf, float32(v))
		}
	}
	silence(400)
	for range 4 {
		tone(880, 220)
		silence(60)
		tone(660, 320)
		silence(500)
	}
	return &samples{buf: buf}
}

// stream is the audio a talker queues; silence when there is none, so the call holds.
type stream struct {
	mu  sync.Mutex
	buf []float32
}

func (st *stream) push(f []float32) {
	st.mu.Lock()
	st.buf = append(st.buf, f...)
	st.mu.Unlock()
}

func (st *stream) flush() {
	st.mu.Lock()
	st.buf = nil
	st.mu.Unlock()
}

func (st *stream) ReadFrame() ([]float32, error) {
	f := make([]float32, meowcaller.FrameSamples)
	st.mu.Lock()
	st.buf = st.buf[copy(f, st.buf):]
	st.mu.Unlock()
	return f, nil
}

func (st *stream) Close() error { return nil }

// talkOn lets talk speak on an answered call until it returns or the call ends.
func talkOn(ctx context.Context, call *meowcaller.Call, ended <-chan struct{}, talk Talker) {
	out := &stream{}
	call.Play(out)
	in := make(chan []float32, 64)
	var inMu sync.Mutex
	inClosed := false
	closeIn := func() {
		inMu.Lock()
		if !inClosed {
			inClosed = true
			close(in)
		}
		inMu.Unlock()
	}
	call.Receive(meowcaller.SinkFunc(func(f []float32) {
		inMu.Lock()
		defer inMu.Unlock()
		if !inClosed {
			select {
			case in <- slices.Clone(f):
			default: // the AI is behind: drop a frame rather than block the call
			}
		}
	}))
	tctx, cancel := context.WithCancel(ctx)
	defer cancel()
	talked := make(chan struct{})
	go func() {
		select {
		case <-ended:
			closeIn()
			cancel()
		case <-talked:
		}
	}()
	talk(tctx, Media{In: in, Say: out.push, Hush: out.flush, Alert: func() {
		out.push(alertSound().(*samples).buf)
	}})
	close(talked)
	closeIn()
}

// newCaller sets up calls on a client, before it connects.
func (s *Service) newCaller(cl *whatsmeow.Client) *meowcaller.Client {
	c := meowcaller.NewClient(cl)
	c.OnIncomingCall(func(call *meowcaller.Call) { go s.incoming(cl, call) })
	return c
}

// caller is who rings: the person whose saved number it is, or "".
func (s *Service) callerOf(ctx context.Context, cl *whatsmeow.Client, peer types.JID) (string, string) {
	digits := peer.User
	if peer.Server == types.HiddenUserServer { // a private ID: its phone number, if WhatsApp told us
		digits = ""
		if pn, err := cl.Store.LIDs.GetPNForLID(ctx, peer); err == nil && !pn.IsEmpty() {
			digits = pn.User
		}
	}
	if digits == "" {
		return "", ""
	}
	for id, p := range s.People() {
		if p.Phone == digits {
			return id, digits
		}
	}
	return "", digits
}

// incoming answers a call to the linked number from someone of the team, with the AI.
func (s *Service) incoming(cl *whatsmeow.Client, call *meowcaller.Call) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	person, digits := s.callerOf(ctx, cl, call.Peer())
	var talk Talker
	if person != "" && s.Inbound != nil {
		talk = s.Inbound(person)
	}
	if talk == nil {
		call.Reject()
		if person == "" {
			slog.Info("whatsapp: declined a call from a number nobody saved", "from", digits)
		}
		return
	}
	var readyOnce, endOnce sync.Once
	ready, ended := make(chan struct{}), make(chan struct{})
	call.OnReady(func() { readyOnce.Do(func() { close(ready) }) })
	call.OnEnd(func(string) { endOnce.Do(func() { close(ended) }) })
	if err := call.Answer(); err != nil {
		slog.Warn("whatsapp: answer", "err", err)
		return
	}
	defer call.Hangup()
	select {
	case <-ready:
	case <-ended:
		return
	case <-time.After(10 * time.Second):
	}
	talkOn(ctx, call, ended, talk)
}
