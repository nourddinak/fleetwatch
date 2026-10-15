package logs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fleetwatch/internal/proto"
)

func TestRingDropsOldestAndReports(t *testing.T) {
	r := NewRing(3, 1<<20)
	for i := 0; i < 5; i++ {
		r.Push(proto.LogLine{Message: string(rune('a' + i))})
	}
	batch, notice := r.Peek(10)
	if !notice || len(batch) != 4 || !strings.Contains(batch[0].Message, "dropped 2") || batch[1].Message != "c" {
		t.Fatalf("peek: %v %+v", notice, batch)
	}
	r.Commit(1, true)
	batch, notice = r.Peek(10)
	if notice || len(batch) != 2 || batch[0].Message != "d" {
		t.Fatalf("after commit: %v %+v", notice, batch)
	}
	// Byte bound applies too.
	b := NewRing(100, 10)
	b.Push(proto.LogLine{Message: "0123456789"})
	b.Push(proto.LogLine{Message: "x"})
	if b.Len() != 1 {
		t.Fatalf("byte bound: %d lines", b.Len())
	}
}

func messages(lines []proto.LogLine) []string {
	var out []string
	for _, l := range lines {
		out = append(out, l.Message)
	}
	return out
}

func TestFileTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app-out.log")
	write := func(s string, flag int) {
		f, err := os.OpenFile(path, flag|os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s)
		f.Close()
	}
	write("old line\n", os.O_TRUNC)
	ft := &fileTail{appID: "a1", stream: "stdout", path: path, maxLine: 20}

	// First sight of a small file: read it from the start.
	if got := messages(ft.poll(0, false)); len(got) != 1 || got[0] != "old line" {
		t.Fatalf("initial: %v", got)
	}
	write("one\ntwo\npart", os.O_APPEND)
	if got := messages(ft.poll(ft.off, true)); strings.Join(got, "|") != "one|two" {
		t.Fatalf("append: %v", got)
	}
	write("ial\r\n", os.O_APPEND)
	if got := messages(ft.poll(ft.off, true)); len(got) != 1 || got[0] != "partial" {
		t.Fatalf("partial line: %v", got)
	}
	// copytruncate rotation.
	write("after truncate\n", os.O_TRUNC)
	if got := messages(ft.poll(ft.off, true)); len(got) != 1 || got[0] != "after truncate" {
		t.Fatalf("truncate: %v", got)
	}
	// Overlong line is cut to maxLine.
	write(strings.Repeat("x", 50)+"\n", os.O_APPEND)
	if got := messages(ft.poll(ft.off, true)); len(got) != 1 || len(got[0]) != 20 {
		t.Fatalf("long line: %v", got)
	}
	ft.close()

	// A restart resumes at the saved offset instead of re-reading.
	saved := ft.off
	write("new after restart\n", os.O_APPEND)
	ft2 := &fileTail{appID: "a1", stream: "stdout", path: path, maxLine: 1000}
	if got := messages(ft2.poll(saved, true)); len(got) != 1 || got[0] != "new after restart" {
		t.Fatalf("resume: %v", got)
	}
	ft2.close()

	// A large unseen file starts near its end and skips the cut first line.
	big := filepath.Join(t.TempDir(), "big.log")
	os.WriteFile(big, []byte(strings.Repeat("filler line\n", 10000)+"last\n"), 0o644)
	ft3 := &fileTail{path: big, maxLine: 1000}
	got := messages(ft3.poll(0, false))
	if len(got) == 0 || len(got) > 6000 || got[len(got)-1] != "last" || got[0] != "filler line" {
		t.Fatalf("backfill: %d lines, first %q", len(got), got[0])
	}
	ft3.close()
}

func TestParseJournal(t *testing.T) {
	e, ok := ParseJournal([]byte(`{"__CURSOR":"s=1;i=2","__REALTIME_TIMESTAMP":"1792062050291000","PRIORITY":"3","_SYSTEMD_UNIT":"demo-worker.service","MESSAGE":"boom"}`))
	if !ok || e.TS != 1792062050291 || e.Priority != 3 || e.Unit != "demo-worker.service" || e.Message != "boom" {
		t.Fatalf("%+v", e)
	}
	e, ok = ParseJournal([]byte(`{"__CURSOR":"c","UNIT":"demo-worker.service","_SYSTEMD_UNIT":"init.scope","MESSAGE":[104,105,255]}`))
	if !ok || e.Unit != "demo-worker.service" || e.Message != "hi\xff" || e.Priority != -1 {
		t.Fatalf("byte message / UNIT: %+v", e)
	}
	if _, ok := ParseJournal([]byte(`not json`)); ok {
		t.Fatal("garbage parsed")
	}

	var got []proto.LogLine
	in := strings.NewReader(`{"__CURSOR":"1","_SYSTEMD_UNIT":"a.service","MESSAGE":"keep","PRIORITY":"6"}
{"__CURSOR":"2","_SYSTEMD_UNIT":"other.service","MESSAGE":"skip"}
{"__CURSOR":"3","_SYSTEMD_UNIT":"a.service","MESSAGE":"` + strings.Repeat("y", 40) + `"}
`)
	readJournal(in, map[string]string{"a.service": "app1"}, 10, func(l proto.LogLine, cur string) { got = append(got, l) })
	if len(got) != 2 || got[0].AppID != "app1" || got[0].Stream != "journal" || len(got[1].Message) != 10 {
		t.Fatalf("readJournal: %+v", got)
	}
}
