package proto

import "time"

// HealthReport is one report in the health space's reports/ (docs/assistant/health.md"Health reports"): an original, its record, or both.
type HealthReport struct {
	Name       string    `json:"name"`                  // the file: the key to delete it
	Title      string    `json:"title"`                 // the name without the upload's time
	Path       string    `json:"path,omitempty"`        // the original; empty for a record alone
	RecordPath string    `json:"record_path,omitempty"` // the .md the health conversation wrote; empty until it has
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
}

// HealthReports is health.reports.list's answer, the newest first.
type HealthReports struct {
	Reports []HealthReport `json:"reports"`
	// HealthDir is the health space they are of when the request named one (another manager's, health-<id>).
	HealthDir string `json:"health_dir,omitempty"`
}
