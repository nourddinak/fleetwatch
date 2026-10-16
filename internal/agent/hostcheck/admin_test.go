package hostcheck

import (
	"os"
	"path/filepath"
	"testing"

	"fleetwatch/internal/proto"
)

func TestDeps(t *testing.T) {
	dir := t.TempDir()
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package-lock.json", `{"lockfileVersion":3,"packages":{"":{"name":"app"},"node_modules/lodash":{"version":"4.17.20"},
		"node_modules/a/node_modules/@scope/b":{"version":"1.0.0"},"node_modules/local":{"link":true}}}`)
	write("requirements.txt", "# web\nDjango==3.2.1\nrequests[socks] == 2.25.0 ; python_version > '3'\nflask>=2\n-e .\n")
	write("go.mod", "module x\n\ngo 1.22\n\nrequire github.com/gin-gonic/gin v1.6.0\n\nrequire (\n\tgolang.org/x/net v0.7.0 // indirect\n)\n")
	rep := Deps(proto.DepsScan{Apps: map[string]string{"a1": dir, "rel": "relative/path"}})
	got := map[string]string{}
	for _, d := range rep.Apps["a1"] {
		got[d.Ecosystem+":"+d.Name] = d.Version
	}
	want := map[string]string{"npm:lodash": "4.17.20", "npm:@scope/b": "1.0.0", "PyPI:django": "3.2.1", "PyPI:requests": "2.25.0",
		"Go:github.com/gin-gonic/gin": "v1.6.0", "Go:golang.org/x/net": "v0.7.0"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("extra deps: %v", got)
	}
	if _, ok := rep.Apps["rel"]; ok {
		t.Error("relative app folder scanned")
	}
}

func TestUFWRule(t *testing.T) {
	m := ufwRule.FindStringSubmatch("[ 3] 8080/tcp (v6)               DENY IN     Anywhere (v6)")
	if m == nil || m[1] != "3" || m[2] != "8080/tcp (v6)" || m[3] != "DENY" || m[5] != "Anywhere (v6)" {
		t.Fatalf("%q", m)
	}
	if m := ufwRule.FindStringSubmatch("[ 1] 22/tcp                     ALLOW IN    203.0.113.4"); m == nil || m[5] != "203.0.113.4" {
		t.Fatalf("%q", m)
	}
}

func TestAptInst(t *testing.T) {
	m := aptInst.FindStringSubmatch("Inst libssl3 [3.0.2-0ubuntu1.15] (3.0.2-0ubuntu1.18 Ubuntu:22.04/jammy-updates, Ubuntu:22.04/jammy-security [amd64])")
	if m == nil || m[1] != "libssl3" || m[2] != "3.0.2-0ubuntu1.15" || m[3] != "3.0.2-0ubuntu1.18" || !secRepo.MatchString(m[4]) {
		t.Fatalf("%q", m)
	}
	if m := aptInst.FindStringSubmatch("Inst newpkg (1.0 Ubuntu:22.04/jammy [amd64])"); m == nil || m[2] != "" {
		t.Fatalf("%q", m)
	}
}
