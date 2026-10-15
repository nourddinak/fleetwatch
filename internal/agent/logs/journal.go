package logs

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"time"

	"fleetwatch/internal/proto"
)

// JournalEntry is the subset of `journalctl -o json` fields we use.
type JournalEntry struct {
	Cursor   string
	TS       int64 // unix ms
	Unit     string
	Priority int
	Message  string
}

// ParseJournal decodes one `journalctl -o json` line. MESSAGE is a string, or an
// array of bytes when it is not valid UTF-8; both are handled.
func ParseJournal(line []byte) (JournalEntry, bool) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(line, &raw) != nil {
		return JournalEntry{}, false
	}
	str := func(k string) string {
		var s string
		json.Unmarshal(raw[k], &s)
		return s
	}
	e := JournalEntry{Cursor: str("__CURSOR"), Priority: -1}
	if us, err := strconv.ParseInt(str("__REALTIME_TIMESTAMP"), 10, 64); err == nil {
		e.TS = us / 1000
	}
	if p, err := strconv.Atoi(str("PRIORITY")); err == nil {
		e.Priority = p
	}
	// systemd's own messages about a unit carry UNIT; the service's output carries _SYSTEMD_UNIT.
	if e.Unit = str("UNIT"); e.Unit == "" {
		e.Unit = str("_SYSTEMD_UNIT")
	}
	if m := raw["MESSAGE"]; len(m) > 0 {
		if json.Unmarshal(m, &e.Message) != nil {
			var b []byte
			var ints []int
			if json.Unmarshal(m, &ints) == nil {
				for _, x := range ints {
					b = append(b, byte(x))
				}
			}
			e.Message = string(b)
		}
	}
	return e, e.Cursor != ""
}

// follow runs journalctl for the given units until ctx ends or journalctl exits.
// It resumes after cursor when one is known, otherwise it starts with recent lines.
func follow(ctx context.Context, units map[string]string, cursor string, maxLine int, emit func(proto.LogLine, string)) error {
	args := []string{"-f", "-o", "json", "--no-pager", "--output-fields=MESSAGE,PRIORITY,UNIT,_SYSTEMD_UNIT,__REALTIME_TIMESTAMP,__CURSOR"}
	if cursor != "" {
		args = append(args, "--after-cursor="+cursor)
	} else {
		args = append(args, "--lines=100")
	}
	for u := range units {
		args = append(args, "-u", u)
	}
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "SYSTEMD_COLORS=0"}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	readJournal(out, units, maxLine, emit)
	return cmd.Wait()
}

func readJournal(r io.Reader, units map[string]string, maxLine int, emit func(proto.LogLine, string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		e, ok := ParseJournal(sc.Bytes())
		if !ok {
			continue
		}
		appID, known := units[e.Unit]
		if !known {
			continue
		}
		msg := e.Message
		if len(msg) > maxLine {
			msg = msg[:maxLine]
		}
		if e.TS == 0 {
			e.TS = time.Now().UnixMilli()
		}
		emit(proto.LogLine{TS: e.TS, AppID: appID, Source: "systemd", Stream: "journal", Priority: e.Priority, Message: msg}, e.Cursor)
	}
	if err := sc.Err(); err != nil {
		slog.Warn("journal read", "err", err)
	}
}
