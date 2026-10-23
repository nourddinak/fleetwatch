package hostcheck

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"fleetwatch/internal/agent/sysexec"
	"fleetwatch/internal/proto"
)

// Country rules live in nftables table inet fleetwatch_geo, replaced in one step each
// time, so the firewall (ufw, iptables) and the address blocks stay untouched. Local
// networks, replies to the server's own connections and the safe addresses always get in.

const geoTable = "fleetwatch_geo"

var localNets = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "127.0.0.0/8", "::1/128", "fc00::/7", "fe80::/10"}

// nftRun applies an nft script (tests replace it).
var nftRun = func(ctx context.Context, script string) error {
	if _, err := exec.LookPath("nft"); err != nil {
		return errors.New("nftables is not installed on this server (apt install nftables)")
	}
	f, err := os.CreateTemp("", "fw-geo-*.nft")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	f.WriteString(script)
	f.Close()
	_, err = sysexec.Run(ctx, sysexec.Opts{Timeout: 60 * time.Second}, "nft", "-f", f.Name())
	return err
}

// ApplyGeo puts the country rules in place (or takes them away when there are none).
func ApplyGeo(ctx context.Context, req proto.GeoApply) proto.GeoStatus {
	st := proto.GeoStatus{At: time.Now().UnixMilli()}
	script := geoScript(req)
	if err := nftRun(ctx, script); err != nil {
		if len(req.Rules) == 0 && strings.Contains(err.Error(), "nftables is not installed") {
			return st // nothing to take away
		}
		st.Error = err.Error()
		return st
	}
	st.Rules = len(req.Rules)
	return st
}

// geoScript is the nft script: the table made empty, then filled.
func geoScript(req proto.GeoApply) string {
	var b strings.Builder
	b.WriteString("add table inet " + geoTable + "\ndelete table inet " + geoTable + "\n")
	if len(req.Rules) == 0 {
		return b.String()
	}
	split := func(list []string) (v4, v6 []string) {
		for _, a := range list {
			p, err := netip.ParsePrefix(a)
			if err != nil {
				ip, err := netip.ParseAddr(a)
				if err != nil {
					continue
				}
				p = netip.PrefixFrom(ip, ip.BitLen())
			}
			p = p.Masked()
			if p.Addr().Unmap().Is4() {
				v4 = append(v4, p.String())
			} else {
				v6 = append(v6, p.String())
			}
		}
		return
	}
	set := func(name, typ string, elems []string) {
		fmt.Fprintf(&b, "\tset %s {\n\t\ttype %s\n\t\tflags interval\n\t\tauto-merge\n", name, typ)
		if len(elems) > 0 {
			fmt.Fprintf(&b, "\t\telements = { %s }\n", strings.Join(elems, ", "))
		}
		b.WriteString("\t}\n")
	}
	fmt.Fprintf(&b, "table inet %s {\n", geoTable)
	safe4, safe6 := split(append(append([]string{}, localNets...), req.Safe...))
	set("safe4", "ipv4_addr", safe4)
	set("safe6", "ipv6_addr", safe6)
	for i, r := range req.Rules {
		v4, _ := split(r.V4)
		_, v6 := split(r.V6)
		set(fmt.Sprintf("r%d_4", i), "ipv4_addr", v4)
		set(fmt.Sprintf("r%d_6", i), "ipv6_addr", v6)
	}
	b.WriteString("\tchain input {\n\t\ttype filter hook input priority -5; policy accept;\n")
	b.WriteString("\t\tiif \"lo\" accept\n\t\tct state established,related accept\n\t\tip saddr @safe4 accept\n\t\tip6 saddr @safe6 accept\n")
	for i, r := range req.Rules {
		ports := ""
		if len(r.Ports) > 0 {
			list := make([]string, 0, len(r.Ports))
			for _, p := range r.Ports {
				if p > 0 && p < 65536 {
					list = append(list, strconv.Itoa(p))
				}
			}
			if len(list) > 0 {
				ports = " meta l4proto { tcp, udp } th dport { " + strings.Join(list, ", ") + " }"
			}
		}
		op := ""
		if r.Mode == "allow" {
			op = "!= "
		}
		fmt.Fprintf(&b, "\t\tip saddr %s@r%d_4%s drop\n", op, i, ports)
		fmt.Fprintf(&b, "\t\tip6 saddr %s@r%d_6%s drop\n", op, i, ports)
	}
	b.WriteString("\t}\n}\n")
	return b.String()
}
