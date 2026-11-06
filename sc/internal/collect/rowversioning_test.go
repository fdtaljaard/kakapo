package collect

import (
	"strings"
	"testing"
)

// The row versioning read (docs/phase5m-contract.md, section 5) is tagged, runs with the safe session
// settings, writes nothing but its table variable, reads each area in its own TRY, and guards what
// older servers lack: ADR's column (2019) with COL_LENGTH, the version store and PVS views with OBJECT_ID.
func TestRowVersioningReadOnly(t *testing.T) {
	q := rowVersioningQuery
	if !strings.HasPrefix(q, KakapoTag+" "+safeSession) {
		t.Errorf("not tagged with the safe session first: %.80s", q)
	}
	upper := strings.ToUpper(q)
	for _, verb := range []string{"ALTER ", "TRUNCATE ", "DBCC ", "CREATE ", "DROP ", "SP_CONFIGURE", "KILL ", "GRANT ", "RECONFIGURE", "UPDATE ", "MERGE ", "DELETE "} {
		if strings.Contains(upper, verb) {
			t.Errorf("contains %q", verb)
		}
	}
	for _, m := range blkWrite.FindAllStringSubmatch(q, -1) {
		if target := m[3]; !strings.HasPrefix(target, "@") {
			t.Errorf("writes to %s: %s", target, m[0])
		}
	}
	if n, tagged := strings.Count(q, "sp_executesql N'"), strings.Count(q, "N'/* kakapo */"); n != tagged || n != 3 {
		t.Errorf("%d dynamic batches, %d tagged, want 3", n, tagged)
	}
	if n := strings.Count(q, "BEGIN TRY"); n != 5 {
		t.Errorf("%d TRY blocks: settings, version store, PVS, open transactions, sessions", n)
	}
	for _, want := range []string{
		"IF COL_LENGTH(N'sys.databases', N'is_accelerated_database_recovery_on') IS NOT NULL",
		"IF OBJECT_ID(N'sys.dm_tran_version_store_space_usage') IS NOT NULL",
		"IF OBJECT_ID(N'sys.dm_tran_persistent_version_store_stats') IS NOT NULL",
		"is_read_committed_snapshot_on", "snapshot_isolation_state_desc", "compatibility_level", "persistent_version_store_size_kb",
		"SUM(CONVERT(bigint, version_store_reserved_page_count)) * 8 FROM tempdb.sys.dm_db_file_space_usage", "reserved_space_kb",
		"@openMinutes int = 0", "WHERE dt.database_id = @dbid", "s.session_id <> @@SPID AND s.is_user_process = 1",
		"SELECT TOP (1) " + openTransactionColumns + " FROM @open ORDER BY started",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("lacks %q", want)
		}
	}
}

// ACT02 and the row versioning read share one fragment: the Performance card's batch runs it with ACT02's
// 5 minutes, filtered on this database.
func TestOpenTransactionsFragmentShared(t *testing.T) {
	for name, q := range map[string]string{"performance": performanceInstanceQuery, "row versioning": rowVersioningQuery} {
		if !strings.Contains(q, openTransactionsFragment) {
			t.Errorf("%s does not run the open transactions fragment", name)
		}
	}
	if !strings.Contains(performanceInstanceQuery, "DECLARE @openMinutes int = 5;") ||
		!strings.Contains(openTransactionsFragment, "dt.database_transaction_begin_time < DATEADD(MINUTE, -@openMinutes, GETDATE())") {
		t.Error("ACT02 reads transactions open 5 minutes or more")
	}
	if strings.Count(performanceInstanceQuery, "sys.dm_tran_database_transactions") != 1 {
		t.Error("the open transactions are read once, in the fragment")
	}
}

// The deadlock graph's isolationlevel is read for each side, so a reader at read committed is known.
func TestParseDeadlockIsolation(t *testing.T) {
	x := strings.NewReplacer(`<process id="process1" taskpriority="0" lockMode="U"`, `<process id="process1" taskpriority="0" lockMode="U" isolationlevel="read committed (2)"`,
		`<process id="process2" taskpriority="0" lockMode="X"`, `<process id="process2" taskpriority="0" lockMode="S" isolationlevel="read committed (2)"`).Replace(deadlockEventXML)
	e, err := ParseDeadlockEvent(x)
	if err != nil {
		t.Fatal(err)
	}
	if e.Victim.Isolation != "read committed (2)" || e.Winner.Isolation != "read committed (2)" || !e.WithReader() || e.Winner.Reader() {
		t.Errorf("isolation %+v %+v", e.Victim, e.Winner)
	}
}
