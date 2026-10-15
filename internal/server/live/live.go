// Package live fans server-side events out to connected browsers.
package live

import (
	"encoding/json"
	"sync"
)

// Sub is one browser subscription. Messages that do not fit in C are dropped rather
// than blocking publishers: a slow browser must never stall agent ingestion.
type Sub struct {
	C chan []byte
}

type Bus struct {
	mu   sync.Mutex
	subs map[*Sub]struct{}
}

func NewBus() *Bus { return &Bus{subs: map[*Sub]struct{}{}} }

func (b *Bus) Subscribe() *Sub {
	s := &Sub{C: make(chan []byte, 256)}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

func (b *Bus) Unsubscribe(s *Sub) {
	b.mu.Lock()
	delete(b.subs, s)
	b.mu.Unlock()
}

// Message is the JSON frame browsers receive.
type Message struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

func (b *Bus) Publish(typ string, data any) {
	raw, err := json.Marshal(Message{typ, data})
	if err != nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subs {
		select {
		case s.C <- raw:
		default: // subscriber is behind; it will catch up with the next update
		}
	}
}
