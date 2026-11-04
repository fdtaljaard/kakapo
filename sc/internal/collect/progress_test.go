package collect

import (
	"database/sql"
	"regexp"
	"strings"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

// The progress read is one tagged SELECT with the 2-second session, and writes nothing (the command
// names in its IN list are text to match, not statements).
func TestProgressQueryReadOnly(t *testing.T) {
	q := progressQuery
	if !strings.HasPrefix(q, KakapoTag+" "+progressSession) || strings.Count(q, KakapoTag) != 1 {
		t.Errorf("progress query is not tagged once with its session first: %.80s", q)
	}
	if !strings.Contains(q, "SET LOCK_TIMEOUT 2000;") || ProgressTimeout != 2*time.Second {
		t.Error("the read gives way after 2 seconds")
	}
	if n := strings.Count(strings.ToUpper(q), "SELECT "); n != 1 {
		t.Errorf("%d SELECTs, want one", n)
	}
	readOnly(t, "progress", regexp.MustCompile(`N'[^']*'`).ReplaceAllString(q, "N''"))
	for _, want := range []string{
		"FROM sys.dm_exec_requests AS r", "JOIN sys.dm_exec_sessions AS s ON s.session_id = r.session_id",
		"r.session_id <> @@SPID", "OR r.percent_complete > 0", "r.estimated_completion_time", "r.total_elapsed_time",
		"r.database_id = 2", "r.database_id = DB_ID(@db)", "r.database_id IS NULL", "s.program_name = @app",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("progress query lacks %q", want)
		}
	}
	for _, c := range []string{"DBCC", "DBCC TABLE CHECK", "DBCC CHECKDB", "BACKUP DATABASE", "BACKUP LOG", "RESTORE DATABASE",
		"RESTORE LOG", "CREATE INDEX", "ALTER INDEX", "UPDATE STATISTICS", "DbccFilesCompact", "DbccSpaceReclaim", "ALTER DATABASE"} {
		if !strings.Contains(q, "N'"+c+"'") {
			t.Errorf("command %q is not read", c)
		}
	}
}

// A row is trimmed, in UTC, never negative, and marked mine by kakapo's program name.
func TestProgressRow(t *testing.T) {
	sast := time.FixedZone("SAST", 2*3600)
	started := sql.NullTime{Valid: true, Time: time.Date(2026, 11, 4, 10, 0, 0, 0, sast)}
	p := progressRow(Progress{SessionID: 71, Command: "DBCC TABLE CHECK   ", Percent: 42.5, RemainingSeconds: -1},
		"GraniteLive", "", domain.BlockingAppName, started)
	if p.Command != "DBCC TABLE CHECK" || p.Database != "GraniteLive" || !p.Mine || p.RemainingSeconds != 0 ||
		!p.StartedAt.Equal(started.Time) || p.StartedAt.Location() != time.UTC {
		t.Errorf("row %+v", p)
	}
	if p := progressRow(Progress{Command: "CREATE INDEX"}, "", "LCK_M_SCH_M", "SSMS", sql.NullTime{}); p.Mine || !p.StartedAt.IsZero() || p.WaitType != "LCK_M_SCH_M" {
		t.Errorf("someone else's row %+v", p)
	}
}
