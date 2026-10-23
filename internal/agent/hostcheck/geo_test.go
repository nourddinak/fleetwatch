package hostcheck

import (
	"context"
	"strings"
	"testing"

	"fleetwatch/internal/proto"
)

func TestGeoScript(t *testing.T) {
	var got string
	nftRun = func(_ context.Context, s string) error { got = s; return nil }
	st := ApplyGeo(context.Background(), proto.GeoApply{Safe: []string{"203.0.113.9", "2001:db8::1"}, Rules: []proto.GeoRule{
		{Mode: "allow", Ports: []int{22}, V4: []string{"41.92.0.0/16", "105.128.0.0/11"}, V6: []string{"2c0f:f000::/24"}},
		{Mode: "block", V4: []string{"1.0.0.0/24"}},
	}})
	if st.Error != "" || st.Rules != 2 {
		t.Fatal(st)
	}
	for _, want := range []string{
		"add table inet fleetwatch_geo\ndelete table inet fleetwatch_geo\n", // replaced in one step
		"ct state established,related accept",                               // replies to the server's own connections
		"203.0.113.9/32", "2001:db8::1/128", "10.0.0.0/8",                   // safe addresses and local networks
		"ip saddr != @r0_4 meta l4proto { tcp, udp } th dport { 22 } drop", // SSH only from the country
		"ip6 saddr != @r0_6 meta l4proto { tcp, udp } th dport { 22 } drop",
		"ip saddr @r1_4 drop", // blocked everywhere
		"elements = { 41.92.0.0/16, 105.128.0.0/11 }",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "elements = {  }") {
		t.Error("an empty element list (nft refuses it)")
	}
	// No rules: only the table goes.
	ApplyGeo(context.Background(), proto.GeoApply{})
	if got != "add table inet fleetwatch_geo\ndelete table inet fleetwatch_geo\n" {
		t.Fatalf("%q", got)
	}
}
