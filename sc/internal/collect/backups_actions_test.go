package collect

import (
	"errors"
	"strings"
	"testing"

	mssql "github.com/microsoft/go-mssqldb"
)

// The phase 4d Storage and Backups reads (docs/phase4d-contract.md, part 2) are tagged, start with
// the safe session and only read: the fix statements are built in domain and run through ExecFix
// (and the CHECKDB run through RunCheckdb).
func TestStorageAndBackupActionReads(t *testing.T) {
	reads := map[string]string{
		"file state": storageFileStateQuery, "integrity facts": checkdbJobFactsQuery, "integrity job": checkdbJobQuery,
		"recovery model": recoveryModelQuery, "suspect pages": suspectPagesQuery, "last good CHECKDB": lastGoodCheckdbQuery,
	}
	for name, q := range reads {
		if !strings.HasPrefix(Tag(q), KakapoTag+" ") || !strings.HasPrefix(q, safeSession) {
			t.Errorf("%s query is not tagged and safe", name)
		}
		for _, write := range []string{"ALTER ", "DELETE", "UPDATE ", "DROP ", "DBCC ", "sp_add", "sp_delete", "sp_update", "sp_start",
			"INSERT INTO", "INSERT msdb", "TRUNCATE", "RECONFIGURE", "sp_configure"} {
			if strings.Contains(q, write) {
				t.Errorf("%s query writes: %q", name, write)
			}
		}
		// The only INSERTs are into table variables.
		for _, line := range strings.Split(q, "\n") {
			if i := strings.Index(line, "INSERT "); i >= 0 && !strings.HasPrefix(line[i:], "INSERT @") {
				t.Errorf("%s query inserts into a table: %q", name, line)
			}
		}
	}
	if n := strings.Count(checkdbJobFactsQuery, "BEGIN TRY"); n != 2 {
		t.Errorf("integrity facts: %d TRY blocks", n)
	}
	for _, want := range []string{"WHERE j.name = @job", "s.command LIKE N'%CHECKDB%'", "s.command LIKE N'%DatabaseIntegrityCheck%'",
		"CONVERT(int, SERVERPROPERTY('EngineEdition')) <> 4", "mf.type = 0"} {
		if !strings.Contains(checkdbJobFactsQuery, want) {
			t.Errorf("integrity facts lack %q", want)
		}
	}
	if !strings.Contains(checkdbJobQuery, "WHERE j.name = @name") || !strings.Contains(suspectPagesQuery, "WHERE database_id = DB_ID(@db)") {
		t.Error("read-backs filter by name and database")
	}
	// The CHECKDB run's session waits for locks rather than giving up after 10 seconds.
	if checkdbSession != "SET LOCK_TIMEOUT -1; SET DEADLOCK_PRIORITY LOW;" {
		t.Errorf("checkdb session %q", checkdbSession)
	}
}

func TestCheckdbMessages(t *testing.T) {
	e := mssql.Error{Number: 8989, Class: 16, Message: "CHECKDB found 0 allocation errors and 2 consistency errors in database 'X'."}
	if m := checkdbError(e); m.Number != 8989 || m.Class != 16 || !m.Error || m.Text != e.Message {
		t.Errorf("error %+v", m)
	}
	if m := checkdbError(errors.New("connection reset")); m.Number != 0 || !m.Error || m.Text != "connection reset" {
		t.Errorf("plain error %+v", m)
	}
	if m := checkdbNotice(mssql.Error{Number: 2528, Class: 0, Message: "DBCC execution completed."}); m.Error || m.Number != 2528 || m.Text != "DBCC execution completed." {
		t.Errorf("notice %+v", m)
	}
	// Phase 4f: CHECKDB runs WITH TABLERESULTS (the message queue hangs on a long check), added once.
	if got := withTableResults("DBCC CHECKDB ([X]) WITH NO_INFOMSGS, ALL_ERRORMSGS;"); got != "DBCC CHECKDB ([X]) WITH NO_INFOMSGS, ALL_ERRORMSGS, TABLERESULTS;" {
		t.Errorf("table results %q", got)
	}
	if got := withTableResults("DBCC CHECKDB ([X])"); got != "DBCC CHECKDB ([X]) WITH TABLERESULTS;" {
		t.Errorf("table results without WITH %q", got)
	}
	if got := withTableResults("DBCC CHECKDB ([X]) WITH TABLERESULTS;"); got != "DBCC CHECKDB ([X]) WITH TABLERESULTS;" {
		t.Errorf("table results twice %q", got)
	}
	if !strings.Contains(lastGoodCheckdbQuery, "is_auto_close_on = 1") {
		t.Error("the last-good read must open an AUTO_CLOSE database first, or it reads NULL")
	}
}
