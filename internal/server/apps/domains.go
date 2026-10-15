package apps

import (
	"strings"

	"fleetwatch/internal/proto"
)

// DomainRef is a site domain the dashboard knows about and where it is served.
type DomainRef struct {
	Host     string
	ServerID string
	AppID    string          // "" when no monitored app serves it
	Origin   *proto.CertInfo // what the server's own web server presents, if checked
}

// Domains lists every site domain: Caddy's sites on each server (matched to an app by
// its port or folder) and the domains set on apps. One entry per host.
func (s *Service) Domains() []DomainRef {
	rows, err := s.db.Query(`SELECT id FROM servers`)
	if err != nil {
		return nil
	}
	var serverIDs []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			serverIDs = append(serverIDs, id)
		}
	}
	rows.Close()

	byHost := map[string]*DomainRef{}
	add := func(r DomainRef) {
		r.Host = strings.ToLower(strings.TrimSpace(r.Host))
		if r.Host == "" {
			return
		}
		if old, ok := byHost[r.Host]; ok && (old.AppID != "" || r.AppID == "") {
			if old.Origin == nil {
				old.Origin = r.Origin
			}
			return
		}
		byHost[r.Host] = &r
	}
	for _, sid := range serverIDs {
		inv := s.inventory(sid)
		list, _ := s.list(sid)
		certs := map[string]*proto.CertInfo{}
		if inv != nil {
			for i := range inv.Certs {
				certs[strings.ToLower(inv.Certs[i].Host)] = &inv.Certs[i]
			}
			views := make([]View, len(list))
			for i, a := range list {
				views[i] = s.view(a, inv)
			}
			for _, r := range inv.Routes {
				ref := DomainRef{Host: r.Host, ServerID: sid, Origin: certs[strings.ToLower(r.Host)]}
				for _, v := range views {
					if r.Port > 0 && hasPort(v, r.Port) || r.Root != "" && v.Path != "" && (r.Root == v.Path || strings.HasPrefix(r.Root, v.Path+"/")) {
						ref.AppID = v.ID
						break
					}
				}
				add(ref)
			}
		}
		for _, a := range list {
			if a.Domain != "" {
				add(DomainRef{Host: a.Domain, ServerID: sid, AppID: a.ID, Origin: certs[strings.ToLower(a.Domain)]})
			}
		}
	}
	out := make([]DomainRef, 0, len(byHost))
	for _, r := range byHost {
		out = append(out, *r)
	}
	return out
}

func hasPort(v View, port int) bool {
	for _, p := range v.Ports {
		if p.Port == port {
			return true
		}
	}
	return false
}
