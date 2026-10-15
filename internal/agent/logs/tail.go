package logs

import (
	"bytes"
	"io"
	"os"
	"time"

	"fleetwatch/internal/proto"
)

const (
	backfill    = 64 << 10 // on first sight of a file, start this far before its end
	readPerPoll = 1 << 20  // cap per poll so one noisy file cannot starve the others
)

// fileTail follows one PM2 output file by polling. It survives truncation
// (copytruncate rotation) and replacement (rename rotation).
type fileTail struct {
	appID, stream, path string
	maxLine             int
	f                   *os.File
	fi                  os.FileInfo
	off                 int64
	partial             []byte
	skipFirst           bool // started mid-file: the first line is probably cut
}

// poll reads new complete lines and returns them with the offset to persist.
func (t *fileTail) poll(saved int64, hasSaved bool) []proto.LogLine {
	fi, err := os.Stat(t.path)
	if err != nil || !fi.Mode().IsRegular() {
		t.close()
		return nil
	}
	var out []proto.LogLine
	if t.f != nil && !os.SameFile(t.fi, fi) {
		out = append(out, t.read()...) // drain the rotated-away file first
		t.close()
		t.off, hasSaved = 0, true
		saved = 0
	}
	if t.f == nil {
		f, err := os.Open(t.path)
		if err != nil {
			return out
		}
		t.f, t.fi = f, fi
		switch {
		case hasSaved && saved <= fi.Size():
			t.off = saved
		default:
			t.off = max(0, fi.Size()-backfill)
			t.skipFirst = t.off > 0
		}
		t.partial = nil
	}
	if fi.Size() < t.off { // truncated
		t.off, t.partial, t.skipFirst = 0, nil, false
	}
	t.fi = fi
	return append(out, t.read()...)
}

func (t *fileTail) read() []proto.LogLine {
	if _, err := t.f.Seek(t.off, io.SeekStart); err != nil {
		return nil
	}
	buf := make([]byte, readPerPoll)
	n, _ := io.ReadFull(t.f, buf)
	if n <= 0 {
		return nil
	}
	t.off += int64(n)
	data := append(t.partial, buf[:n]...)
	var out []proto.LogLine
	now := time.Now().UnixMilli()
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimRight(data[:i], "\r")
		data = data[i+1:]
		if t.skipFirst {
			t.skipFirst = false
			continue
		}
		out = append(out, t.line(now, line))
	}
	// Keep the unfinished tail, but never let one endless line grow without bound.
	if len(data) > t.maxLine {
		out = append(out, t.line(now, data))
		data = nil
	}
	t.partial = append([]byte(nil), data...)
	return out
}

func (t *fileTail) line(ts int64, b []byte) proto.LogLine {
	if len(b) > t.maxLine {
		b = b[:t.maxLine]
	}
	return proto.LogLine{TS: ts, AppID: t.appID, Source: "pm2", Stream: t.stream, Priority: -1, Message: string(b)}
}

func (t *fileTail) close() {
	if t.f != nil {
		t.f.Close()
		t.f = nil
	}
}
