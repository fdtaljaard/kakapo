// Package collect is the SQL Server side: connection strings, the script runner and, later, samplers.
package collect

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"time"

	_ "github.com/microsoft/go-mssqldb"

	"sqlmonitor/internal/domain"
)

// ConnString builds a go-mssqldb connection string from the setup input, in the ODBC
// "odbc:key=value;" form, which takes HOST\INSTANCE as SSMS does and lets a value be
// quoted in braces. Host accepts HOST, HOST\INSTANCE or HOST,PORT.
func ConnString(in domain.ServerInput) string {
	host, port := splitHostPort(in.Host)

	parts := []string{
		"server=" + adoValue(host),
		"app name=" + domain.BlockingAppName,
		"dial timeout=10",
		// No socket deadline: go-mssqldb applies "connection timeout" to every read and write, so a
		// statement that sends nothing for that long (a CHECKDB, an index build) dies with an i/o
		// timeout; it is the bug the sandbox showed (docs/phase4f-contract.md). Every read and fix
		// carries its own context timeout instead, and Test bounds the connection check itself.
		"connection timeout=0",
		// TCP keepalive every 30 seconds (go-mssqldb's default, stated so it stays): a long fix holds
		// one connection idle for minutes or hours while SQL Server works, and a firewall or VPN drops
		// an idle connection without telling either side (docs/phase4f-contract.md).
		"keepAlive=30",
	}
	if port != "" {
		parts = append(parts, "port="+port)
	}
	if in.Database != "" {
		parts = append(parts, "database="+adoValue(in.Database))
	}
	if in.Encrypt {
		parts = append(parts, "encrypt=true", "TrustServerCertificate="+fmt.Sprint(in.TrustCert))
	} else {
		// Login packet still encrypted; data is not. The driver would still verify the
		// certificate, and most instances run on a self-signed one, so trust it here.
		// Same behaviour as the PowerShell runners without -Encrypt.
		parts = append(parts, "encrypt=false", "TrustServerCertificate=true")
	}
	if in.Auth == domain.AuthSQL {
		parts = append(parts, "user id="+adoValue(in.Username), "password="+adoValue(in.Password))
	}
	// With no user id, the driver authenticates with SSPI as the current Windows user.
	return "odbc:" + strings.Join(parts, ";")
}

// adoValue wraps a value in braces so ';' and '}' inside it survive the ODBC parser.
func adoValue(v string) string {
	return "{" + strings.ReplaceAll(v, "}", "}}") + "}"
}

// splitHostPort turns "HOST,1433" into ("HOST", "1433") and leaves "HOST\INSTANCE" alone.
// Instance names are resolved by the driver through the SQL Browser service.
func splitHostPort(h string) (host, port string) {
	h = strings.TrimSpace(h)
	if i := strings.LastIndex(h, ","); i > 0 {
		return strings.TrimSpace(h[:i]), strings.TrimSpace(h[i+1:])
	}
	return h, ""
}

// Test opens a connection, asks the instance who it is, and closes it.
func Test(ctx context.Context, in domain.ServerInput) (domain.ConnectionInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	db, err := sql.Open("sqlserver", ConnString(in))
	if err != nil {
		return domain.ConnectionInfo{}, fmt.Errorf("connection string: %w", err)
	}
	defer db.Close()

	var info domain.ConnectionInfo
	row := db.QueryRowContext(ctx, Tag(`
		SELECT CAST(@@SERVERNAME AS nvarchar(256)),
		       CAST(SERVERPROPERTY('ProductVersion') AS nvarchar(64)),
		       CAST(SERVERPROPERTY('ProductLevel') AS nvarchar(64)),
		       CAST(SERVERPROPERTY('Edition') AS nvarchar(128)),
		       CAST(@@VERSION AS nvarchar(512))`))
	if err := row.Scan(&info.ServerName, &info.ProductVersion, &info.ProductLevel, &info.Edition, &info.Version); err != nil {
		return domain.ConnectionInfo{}, friendlyError(err)
	}
	if i := strings.IndexAny(info.Version, "\r\n"); i > 0 {
		info.Version = strings.TrimSpace(info.Version[:i])
	}
	return info, nil
}

// friendlyError keeps the driver's message but says what it usually means.
func friendlyError(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Login failed"):
		return fmt.Errorf("login failed: check the user name and password, or whether the login exists on this server (%v)", err)
	case strings.Contains(msg, "unable to open tcp connection"), strings.Contains(msg, "no such host"),
		strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "connectex"):
		return fmt.Errorf("cannot reach the server: check the name, the instance, the firewall, and that TCP/IP and the SQL Browser service are enabled (%v)", err)
	case strings.Contains(msg, "certificate"):
		return fmt.Errorf("the server's certificate is not trusted: tick 'Trust server certificate' or turn encryption off (%v)", err)
	}
	return err
}

// KakapoTag starts every batch kakapo sends (docs/phase3f-contract.md, item 2), so the plan cache
// and Query Store reads can leave kakapo's own statements out of the costliest queries.
const KakapoTag = "/* kakapo */"

// Tag prefixes a batch with KakapoTag, once. Every query string in collect goes through it,
// including the fix statements.
func Tag(batch string) string {
	if strings.HasPrefix(batch, KakapoTag) {
		return batch
	}
	return KakapoTag + " " + batch
}

// statementStart is a statement Query Store keeps on its own line: SELECT, INSERT, UPDATE, DELETE,
// MERGE or WITH at the start of a line, upper case as kakapo writes them, not yet tagged.
var statementStart = regexp.MustCompile(`(?m)^([ \t]*)(SELECT|INSERT|UPDATE|DELETE|MERGE|WITH)\b( /\* kakapo \*/)?`)

// TagStatements tags a read batch that runs inside a user database (EXEC [db].sys.sp_executesql
// @stmt) and each statement in it (phase 5k). Query Store keeps the text of each statement of a batch
// from the statement's first keyword, so the batch's leading tag reaches only its first statement and
// kakapo's other statements showed among the costliest and the changed queries. A comment after the
// keyword is part of the statement's text. Only kakapo's own reads: never a fix or a job command,
// whose statements are real work.
func TagStatements(batch string) string {
	return Tag(statementStart.ReplaceAllString(batch, "${1}${2} "+KakapoTag))
}
