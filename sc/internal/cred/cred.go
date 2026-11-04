// Package cred stores SQL login passwords outside the DuckDB file.
package cred

import "fmt"

// Store keeps secrets per server id.
type Store interface {
	Set(serverID int64, password string) error
	Get(serverID int64) (string, error)
	Delete(serverID int64) error
}

// target names the credential: the install id keeps two data files on one machine apart,
// so a test copy editing "server 1" never touches the real install's "server 1".
func target(installID string, serverID int64) string {
	return fmt.Sprintf("SQLMonitor:%s:server:%d", installID, serverID)
}
