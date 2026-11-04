package domain

import "time"

// Change is one row of the change log: something the app changed on a server through a fix.
type Change struct {
	ID          int64     `json:"id"`
	ServerID    int64     `json:"serverId"`
	Database    string    `json:"database,omitempty"`
	ChangedAt   time.Time `json:"changedAt"`
	Object      string    `json:"object"`     // e.g. "Query Store"
	ChangeType  string    `json:"changeType"` // e.g. "SETTING"
	Description string    `json:"description"`
	Statement   string    `json:"statement"`
	Rollback    string    `json:"rollback,omitempty"`
	Result      string    `json:"result,omitempty"`
}
