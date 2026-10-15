package deploy

import (
	"strings"
	"testing"
)

func TestCaddySite(t *testing.T) {
	s, err := CaddySite("ops.my-site.com")
	if err != nil || !strings.Contains(s, "\nops.my-site.com {\n") || strings.Contains(s, "\nops.example.com {") {
		t.Fatalf("site: %v\n%s", err, s)
	}
	for _, bad := range []string{"", "localhost", "a b.com", "x.com {\n}\nevil.com", "-x.com", "x.com/", strings.Repeat("a", 64) + ".com"} {
		if _, err := CaddySite(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
