package sites

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fleetwatch/internal/proto"
)

// TestRealDocker builds and runs a small app with the real Docker (FW_REAL_DOCKER=1, as
// root on Linux): it must answer on its port, survive a redeploy, and be gone after Remove.
func TestRealDocker(t *testing.T) {
	if os.Getenv("FW_REAL_DOCKER") != "1" {
		t.Skip("set FW_REAL_DOCKER=1 to use the real Docker")
	}
	root := t.TempDir()
	WWW, Caddyfile, CaddyDir = filepath.Join(root, "www"), filepath.Join(root, "Caddyfile"), filepath.Join(root, "sites")
	os.MkdirAll(WWW, 0o755)
	snap := tarball(t, map[string]string{
		"me-hello-abc/Dockerfile": "FROM busybox:1.36\nCOPY index.html /www/index.html\nEXPOSE 8080\nCMD [\"sh\", \"-c\", \"echo \\\"<p>$GREETING</p>\\\" >> /www/index.html; httpd -f -p 8080 -h /www\"]\n",
		"me-hello-abc/index.html": "<h1>hello</h1>",
	})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(snap) }))
	defer srv.Close()
	client = srv.Client()
	req := proto.SiteDeploy{RunID: "r1", Name: "fwtest-hello", Archive: srv.URL + "/t", Docker: &proto.SiteDocker{Port: 8080, HostPort: 29999, Env: "GREETING=from-env\n"}}
	defer Remove(proto.SiteRemove{Name: req.Name})
	for i := range 2 { // a first deploy, then a redeploy over it
		res := Deploy(context.Background(), req)
		if !res.OK {
			t.Fatalf("deploy %d: %s\n%s", i, res.Error, res.Log)
		}
		time.Sleep(time.Second)
	}
	resp, err := http.Get("http://127.0.0.1:29999/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "hello") || !strings.Contains(string(body), "from-env") {
		t.Fatalf("%s", body)
	}
	Remove(proto.SiteRemove{Name: req.Name})
	out, _ := exec.Command("docker", "ps", "-a", "--filter", "name=fw-fwtest-hello", "-q").Output()
	imgs, _ := exec.Command("docker", "images", "fw-fwtest-hello", "-q").Output()
	if len(bytes.TrimSpace(out)) > 0 || len(bytes.TrimSpace(imgs)) > 0 {
		t.Fatalf("left behind: %s / %s", out, imgs)
	}
}
