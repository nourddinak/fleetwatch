package apps

import (
	"encoding/json"

	"fleetwatch/internal/proto"
)

// StorageTargets lists the folders of a server's monitored apps (agent config).
func (s *Service) StorageTargets(serverID string) []proto.StorageTarget {
	out := []proto.StorageTarget{}
	list, err := s.list(serverID)
	if err != nil {
		return out
	}
	inv := s.inventory(serverID)
	for _, a := range list {
		if !a.Flags.Monitoring {
			continue
		}
		if path := s.view(a, inv).Path; path != "" {
			out = append(out, proto.StorageTarget{AppID: a.ID, Path: path})
		}
	}
	return out
}

// StorageTarget returns one app's folder, for an on-demand scan.
func (s *Service) StorageTarget(appID string) (string, proto.StorageTarget, error) {
	v, err := s.Get(appID)
	if err != nil {
		return "", proto.StorageTarget{}, err
	}
	return v.ServerID, proto.StorageTarget{AppID: appID, Path: v.Path}, nil
}

// IngestStorage stores a scan result from a server's agent.
func (s *Service) IngestStorage(serverID string, r proto.StorageResult) {
	a, err := s.get(r.AppID)
	if err != nil || a.ServerID != serverID {
		return
	}
	if len(r.Items) > 20 {
		r.Items = r.Items[:20]
	}
	raw, _ := json.Marshal(r)
	s.db.Exec(`INSERT INTO app_storage (app_id, scanned_at, data) VALUES (?, ?, ?)
		ON CONFLICT (app_id) DO UPDATE SET scanned_at = excluded.scanned_at, data = excluded.data`, r.AppID, r.ScannedAt, string(raw))
	s.Bus.Publish("storage", map[string]string{"appId": r.AppID})
	s.PublishServer(serverID)
}

// Storage returns an app's latest scan, or nil.
func (s *Service) Storage(appID string) *proto.StorageResult {
	var raw string
	if s.db.QueryRow(`SELECT data FROM app_storage WHERE app_id = ?`, appID).Scan(&raw) != nil {
		return nil
	}
	var r proto.StorageResult
	if json.Unmarshal([]byte(raw), &r) != nil {
		return nil
	}
	return &r
}
