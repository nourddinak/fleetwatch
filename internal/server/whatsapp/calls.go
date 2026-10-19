package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	"github.com/purpshell/meowcaller"
)

// Calls go through meowcaller, WhatsApp's voice calls in pure Go. Audio is 16 kHz mono,
// in frames of meowcaller.FrameSamples.

// Ring calls a phone number on WhatsApp for up to wait. When they answer, the alert
// sound plays and the call ends. It reports whether they answered.
func (s *Service) Ring(ctx context.Context, phone string, wait time.Duration) (bool, error) {
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
