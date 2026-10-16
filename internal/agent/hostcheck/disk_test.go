package hostcheck

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fleetwatch/internal/proto"
)

func write(t *testing.T, rel string, n int) string {
	t.Helper()
	path := filepath.Join(Root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(strings.Repeat("x", n)), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDiskScanAndClean(t *testing.T) {
	Root = t.TempDir()
	defer func() { Root = "/" }()
	current := write(t, "var/log/syslog", 100)
	write(t, "var/log/syslog.1", 1000)
	write(t, "var/log/nginx/access.log.2.gz", 500)
	write(t, "root/.npm/_cacache/content/a", 2000)
	write(t, "home/bob/.cache/pip/b", 300)
	pm2 := write(t, "home/bob/.pm2/logs/api-out.log", 700)
	app := write(t, "srv/app/node_modules/big.js", 5000)
	ctx := context.Background()

	rep := DiskScan(ctx)
	got := map[string]proto.DiskItem{}
	for _, it := range rep.Items {
		got[it.ID] = it
	}
	if got["oldlogs"].Bytes != 1500 || got["caches"].Bytes != 2300 || got["pm2logs"].Bytes != 700 || got["oldlogs"].Files != 2 {
		t.Fatalf("scan: %+v", rep.Items)
	}
	if len(rep.Top) == 0 || rep.Top[0].Path != "/srv/app" || rep.Top[0].Bytes != 5000 {
		t.Fatalf("top folders: %+v", rep.Top)
	}

	for _, action := range []string{"oldlogs", "caches", "pm2logs"} {
		res := DiskClean(ctx, proto.DiskClean{RunID: "r", Action: action})
		if res.Error != "" || res.Freed != got[action].Bytes {
			t.Fatalf("%s: %+v", action, res)
		}
	}
	if _, err := os.Stat(current); err != nil {
		t.Fatal("the current log was removed")
	}
	if fi, err := os.Stat(pm2); err != nil || fi.Size() != 0 {
		t.Fatal("PM2 log should be emptied, not removed")
	}
	if _, err := os.Stat(app); err != nil {
		t.Fatal("an app's node_modules was touched")
	}
	if res := DiskClean(ctx, proto.DiskClean{RunID: "r", Action: "rm -rf /"}); res.Error == "" {
		t.Fatal("unknown action accepted")
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"1.5GB": 1_500_000_000, "512kB": 512_000, "0B": 0, "2MiB": 2 << 20, "junk": 0} {
		if got := parseSize(in); got != want {
			t.Errorf("%s: %d", in, got)
		}
	}
}
