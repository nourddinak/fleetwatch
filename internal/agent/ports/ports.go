// Package ports lists listening sockets with their owning process.
package ports

import (
	"sort"

	gonet "github.com/shirou/gopsutil/v4/net"

	"fleetwatch/internal/agent/procs"
	"fleetwatch/internal/proto"
)

// List returns TCP listeners and bound, unconnected UDP sockets.
func List(tab *procs.Table) ([]proto.Port, error) {
	conns, err := gonet.Connections("inet")
	if err != nil {
		return nil, err
	}
	seen := map[proto.Port]bool{}
	out := []proto.Port{}
	for _, c := range conns {
		var protoName string
		switch {
		case c.Type == 1 && c.Status == "LISTEN": // SOCK_STREAM
			protoName = "TCP"
		case c.Type == 2 && c.Raddr.IP == "" && c.Laddr.Port != 0: // SOCK_DGRAM, not connected
			protoName = "UDP"
		default:
			continue
		}
		p := proto.Port{Proto: protoName, Addr: c.Laddr.IP, Port: int(c.Laddr.Port), PID: int(c.Pid)}
		if x := tab.ByPID[p.PID]; x != nil {
			p.Process = x.Name
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Proto < out[j].Proto
	})
	return out, nil
}
