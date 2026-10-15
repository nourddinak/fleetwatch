package metrics

import (
	"database/sql"
	"slices"

	"fleetwatch/internal/server/settings"
	"fleetwatch/internal/server/valid"
)

// Settings is the "metrics" settings section.
type Settings struct {
	IntervalSec   int `json:"intervalSec"`   // agent sampling interval
	RetentionDays int `json:"retentionDays"` // hourly data retention
}

var (
	Intervals  = []int{5, 10, 15, 30, 60}
	Retentions = []int{7, 14, 30, 60, 90, 180, 365}
)

func DefaultSettings() Settings { return Settings{IntervalSec: 15, RetentionDays: 30} }

func (s Settings) Validate() error {
	if !slices.Contains(Intervals, s.IntervalSec) {
		return valid.Error("collection interval must be one of 5, 10, 15, 30 or 60 seconds")
	}
	if !slices.Contains(Retentions, s.RetentionDays) {
		return valid.Error("retention must be one of 7, 14, 30, 60, 90, 180 or 365 days")
	}
	return nil
}

func LoadSettings(d *sql.DB) Settings {
	s := DefaultSettings()
	if settings.Load(d, "metrics", &s) != nil || s.Validate() != nil {
		return DefaultSettings()
	}
	return s
}

func SaveSettings(d *sql.DB, s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	return settings.Save(d, "metrics", s)
}
