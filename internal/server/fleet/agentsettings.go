package fleet

import (
	"database/sql"

	"fleetwatch/internal/server/settings"
)

// AgentSettings is the "agents" settings section.
type AgentSettings struct {
	// AutoUpdate updates every agent that connects on an older version than the
	// agent built into this server.
	AutoUpdate bool `json:"autoUpdate"`
}

func LoadAgentSettings(d *sql.DB) AgentSettings {
	var s AgentSettings
	settings.Load(d, "agents", &s)
	return s
}

func SaveAgentSettings(d *sql.DB, s AgentSettings) error { return settings.Save(d, "agents", s) }
