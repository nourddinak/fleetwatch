package logs

import "sync"

// Sub receives newly stored lines for one app or server (or everything when both
// filters are empty). A slow subscriber loses lines instead of slowing ingestion;
// Dropped tells the browser to show a gap.
type Sub struct {
	AppID, ServerID string
	C               chan []Line
	mu              sync.Mutex
	dropped         int
}

// TakeDropped returns and resets the number of lines lost since the last call.
func (s *Sub) TakeDropped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.dropped
	s.dropped = 0
	return n
}

type Live struct {
	mu   sync.Mutex
	subs map[*Sub]struct{}
}

func NewLive() *Live { return &Live{subs: map[*Sub]struct{}{}} }

func (l *Live) Subscribe(appID, serverID string) *Sub {
	s := &Sub{AppID: appID, ServerID: serverID, C: make(chan []Line, 64)}
	l.mu.Lock()
	l.subs[s] = struct{}{}
	l.mu.Unlock()
	return s
}

func (l *Live) Unsubscribe(s *Sub) {
	l.mu.Lock()
	delete(l.subs, s)
	l.mu.Unlock()
}

func (l *Live) Publish(lines []Line) {
	if len(lines) == 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for s := range l.subs {
		var mine []Line
		for _, x := range lines {
			if (s.AppID == "" || x.AppID == s.AppID) && (s.ServerID == "" || x.ServerID == s.ServerID) {
				mine = append(mine, x)
			}
		}
		if len(mine) == 0 {
			continue
		}
		select {
		case s.C <- mine:
		default:
			s.mu.Lock()
			s.dropped += len(mine)
			s.mu.Unlock()
		}
	}
}
