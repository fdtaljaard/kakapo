// Package domain holds the model of SQLMonitor and the rules that need no I/O.
package domain

import (
	"errors"
	"strings"
	"time"
)

// AuthType says how we log in to a SQL Server.
type AuthType string

const (
	AuthWindows AuthType = "windows" // SSPI, the current Windows user
	AuthSQL     AuthType = "sql"     // SQL Server login and password
)

// Server is a monitored SQL Server instance as the user registered it.
// It never carries a password; that lives in the credential store.
type Server struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Host      string    `json:"host"` // HOST, HOST\INSTANCE or HOST,PORT, as typed in SSMS
	Auth      AuthType  `json:"auth"`
	Username  string    `json:"username,omitempty"`
	Database  string    `json:"database,omitempty"` // optional default database
	Encrypt   bool      `json:"encrypt"`
	TrustCert bool      `json:"trustCert"`
	CreatedAt time.Time `json:"createdAt"`

	// Result of the last successful connection test, if any.
	LastVersion  string     `json:"lastVersion,omitempty"`
	LastEdition  string     `json:"lastEdition,omitempty"`
	LastTestedAt *time.Time `json:"lastTestedAt,omitempty"`
}

// ServerInput is what the setup screen sends. Password is kept out of Server on purpose.
// For a SQL login an empty password means "use the stored one"; the service decides whether
// there is one (a new registration needs a password, an edit may keep the stored one).
type ServerInput struct {
	// ID names a registered server whose stored password a connection test may use. Only the
	// test route reads it; registering ignores it and editing takes the id from the path.
	ID        int64    `json:"id,omitempty"`
	Name      string   `json:"name"`
	Host      string   `json:"host"`
	Auth      AuthType `json:"auth"`
	Username  string   `json:"username"`
	Password  string   `json:"password"`
	Database  string   `json:"database"`
	Encrypt   bool     `json:"encrypt"`
	TrustCert bool     `json:"trustCert"`
}

// ConnectionInfo is what a successful connection test tells us about the instance.
type ConnectionInfo struct {
	ServerName     string `json:"serverName"`
	ProductVersion string `json:"productVersion"` // 16.0.4105.2
	ProductLevel   string `json:"productLevel"`   // RTM, SP1, CU12
	Edition        string `json:"edition"`        // Express Edition (64-bit)
	Version        string `json:"version"`        // first line of @@VERSION
}

// Validate checks the input the way a careful colleague would before trying to connect.
func (in *ServerInput) Validate() error {
	in.Name = strings.TrimSpace(in.Name)
	in.Host = strings.TrimSpace(in.Host)
	in.Username = strings.TrimSpace(in.Username)
	in.Database = strings.TrimSpace(in.Database)

	if in.Host == "" {
		return errors.New("server is required, as you would type it in SSMS (e.g. CLIENTSQL01\\WMS)")
	}
	if in.Name == "" {
		in.Name = in.Host
	}
	switch in.Auth {
	case AuthWindows:
		in.Username, in.Password = "", ""
	case AuthSQL:
		if in.Username == "" {
			return errors.New("a SQL login needs a user name")
		}
	default:
		return errors.New("auth must be 'windows' or 'sql'")
	}
	return nil
}
