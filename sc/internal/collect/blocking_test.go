package collect

import (
	"regexp"
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
)

// blkWrite finds a statement that writes and the token after it.
var blkWrite = regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|MERGE)\s+(INTO\s+)?(\S+)`)

// The blocking reads are tagged, run with the safe session settings, and write nothing but their own
// temporary tables and table variables; nothing alters, kills or changes a setting.
func TestBlockingReadsOnly(t *testing.T) {
	for name, q := range map[string]string{"chain": blockingQuery, "kill target": killTargetQuery,
		"kill status": Tag(killStatusQuery), "row versioning": rowVersioningQuery} {
		if !strings.HasPrefix(q, KakapoTag+" "+safeSession) {
			t.Errorf("%s query is not tagged with the safe session first: %.80s", name, q)
		}
		upper := strings.ToUpper(likeLiterals.ReplaceAllString(q, "LIKE N''"))
		for _, verb := range []string{"ALTER ", "TRUNCATE ", "DBCC ", "CREATE INDEX", "SP_CONFIGURE", "KILL ", "SP_ADD", "SP_DELETE", "GRANT ", "SHUTDOWN"} {
			if strings.Contains(upper, verb) {
				t.Errorf("%s query contains %q", name, verb)
			}
		}
		for _, m := range blkWrite.FindAllStringSubmatch(q, -1) {
			if target := m[3]; !strings.HasPrefix(target, "#blk_") && !strings.HasPrefix(target, "@") &&
				!strings.HasPrefix(target, "\"") && !strings.HasPrefix(target, "`") {
				t.Errorf("%s query writes to %s: %s", name, target, m[0])
			}
		}
		for _, m := range regexp.MustCompile(`DROP TABLE (\S+)`).FindAllStringSubmatch(q, -1) {
			if !strings.HasPrefix(m[1], "#blk_") {
				t.Errorf("%s query drops %s", name, m[1])
			}
		}
	}
	// The status read is the one KILL that is allowed, and it only reports.
	if s := domain.KillStatusOnlyStatement(83); s != "KILL 83 WITH STATUSONLY;" {
		t.Errorf("status only %q", s)
	}
}

// Each area of the chain read has its own TRY and error column; the reads the classification needs
// are there.
func TestBlockingQueryShape(t *testing.T) {
	if n := strings.Count(blockingQuery, "BEGIN TRY"); n != 4 {
		t.Errorf("%d TRY blocks: sessions, job names, locks and the names in each database", n)
	}
	for _, want := range []string{
		"@sessionsError, @jobsError, @locksError, @namesError",
		// who blocks whom, without a parallel query's own threads
		"b.blocking_session_id > 0 AND b.blocking_session_id <> b.session_id",
		// the classification: status, transaction name, isolation level, wait resource; the statement now or the last one
		"s.status", "at.name", "s.transaction_isolation_level", "r.wait_resource", "c.most_recent_sql_handle", "r.statement_start_offset",
		"database_transaction_log_bytes_used",
		// the lock waited on and the blocker's mode on it, named in the lock's database
		"w.request_status = N'WAIT'", "h.request_status = N'GRANT'", "sys.partitions AS p ON p.hobt_id", ".sys.sp_executesql",
		// the job id in the program name, through msdb
		"SUBSTRING(x.program_name, CHARINDEX(N'(Job 0x', x.program_name) + 5, 34), 1)", "msdb.dbo.sysjobs",
	} {
		if !strings.Contains(blockingQuery, want) {
			t.Errorf("blocking query lacks %q", want)
		}
	}
	if !strings.Contains(killTargetQuery, "WHERE s.session_id = @session") || !strings.Contains(killTargetQuery, "msdb.dbo.sysjobs") {
		t.Error("the kill target query reads the one session and its job")
	}
	if !strings.Contains(rowVersioningQuery, "is_read_committed_snapshot_on") || !strings.Contains(rowVersioningQuery, "s.session_id <> @@SPID") {
		t.Error("the rcsi query reads the setting and the other sessions")
	}
}

// The kill refuses kakapo's own connections by their program name, which ConnString sets.
func TestBlockingAppName(t *testing.T) {
	if !strings.Contains(ConnString(domain.ServerInput{Host: "h"}), "app name="+domain.BlockingAppName+";") {
		t.Errorf("ConnString's app name is not %q", domain.BlockingAppName)
	}
}
