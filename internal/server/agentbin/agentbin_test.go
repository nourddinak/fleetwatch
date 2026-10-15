package agentbin

import (
	"strings"
	"testing"
)

func TestInstallScriptQuoting(t *testing.T) {
	out, err := InstallScript("https://ops.example.com")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"SERVER_URL='https://ops.example.com'", "ExecStart=/usr/local/bin/ops-agent run", "sha256sum --check"} {
		if !strings.Contains(s, want) {
			t.Errorf("script missing %q", want)
		}
	}
	// A quote in the URL must not break out of the shell string.
	out, _ = InstallScript("https://x'; rm -rf / #")
	if !strings.Contains(string(out), `SERVER_URL='https://x'\''; rm -rf / #'`) {
		t.Fatalf("unsafe quoting:\n%s", out)
	}
}

func TestBinaryPlatforms(t *testing.T) {
	for _, p := range []string{"", "windows-amd64", "../agentbin.go", "linux-amd64/../../x"} {
		if _, ok := Binary(p); ok {
			t.Errorf("platform %q served", p)
		}
	}
}
