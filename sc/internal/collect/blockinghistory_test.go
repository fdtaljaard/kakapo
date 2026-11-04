package collect

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

// The blocking history reads are tagged, run with the safe session settings, and write nothing but
// their own table variables; nothing alters, configures or kills.
func TestBlockingHistoryReadsOnly(t *testing.T) {
	for name, q := range map[string]string{"history": blockingHistoryQuery, "names": blockingNamesQuery} {
		if !strings.HasPrefix(q, KakapoTag+" "+safeSession) {
			t.Errorf("%s query is not tagged with the safe session first: %.80s", name, q)
		}
		upper := strings.ToUpper(q)
		for _, verb := range []string{"ALTER ", "TRUNCATE ", "DBCC ", "CREATE ", "DROP ", "SP_CONFIGURE", "KILL ", "SP_ADD", "SP_DELETE",
			"GRANT ", "SHUTDOWN", "RECONFIGURE", "UPDATE ", "MERGE "} {
			if strings.Contains(upper, verb) {
				t.Errorf("%s query contains %q", name, verb)
			}
		}
		for _, m := range blkWrite.FindAllStringSubmatch(q, -1) {
			if target := m[3]; !strings.HasPrefix(target, "@") {
				t.Errorf("%s query writes to %s: %s", name, target, m[0])
			}
		}
		// Every dynamic batch is tagged too.
		if n, tagged := strings.Count(q, "sp_executesql N'")+strings.Count(q, "EXEC @in N'"), strings.Count(q, "N'/* kakapo */"); n != tagged {
			t.Errorf("%s query: %d dynamic batches, %d tagged", name, n, tagged)
		}
	}
}

// The read follows the deadlock read and the block viewer: the file target filtered on object_name (and
// on timestamp_utc from 2017), the ring buffer when the files cannot be read, the blocked-process-report
// XPath, both processes' attributes, each area in its own TRY with an error column.
func TestBlockingHistoryQueryShape(t *testing.T) {
	q := blockingHistoryQuery
	if n := strings.Count(q, "WHERE object_name = N''sp_server_diagnostics_component_result''") +
		strings.Count(q, "WHERE object_name = N'sp_server_diagnostics_component_result'"); n != 2 {
		t.Errorf("%d file reads filtered on object_name, want 2 (2017 and later, and before)", n)
	}
	if n := strings.Count(q, "BEGIN TRY"); n != 5 {
		t.Errorf("%d TRY blocks: the configuration, kakapo's files, the session, the files, the reports", n)
	}
	for _, want := range []string{
		"sys.fn_xe_file_target_read_file(@p, NULL, NULL, NULL)", "AND CONVERT(datetime2, timestamp_utc) >= @s", "system_health*.xel",
		"RingBufferTarget/event[@name=\"sp_server_diagnostics_component_result\"]",
		"//data[@name=\"data\"]/value/queryProcessing/blockingTasks/blocked-process-report",
		"/blocked-process-report/blocked-process", "/blocked-process-report/blocking-process", "@monitorLoop",
		"(process/@waitresource)[1]", "(process/@waittime)[1]", "(process/@lockMode)[1]", "(process/@isolationlevel)[1]",
		"(process/@trancount)[1]", "(process/@status)[1]", "(process/@lastbatchstarted)[1]", "(process/@lasttranstarted)[1]",
		"(process/@xactid)[1]", "(process/@clientoption1)[1]",
		// a head kept with an empty blocking-process when it holds a transaction
		"AND (bg.exist('process/@spid') = 1 OR bd.exist('process[@trancount > 0]') = 1)", "(process/inputbuf/text())[1]", "(process/@currentdbname)[1]",
		"N'blocked process threshold (s)'", "is_read_committed_snapshot_on", "DATEDIFF(MINUTE, GETUTCDATE(), SYSDATETIME())",
		"SELECT @source, @err, @fileErr, @shredErr, @configErr, @threshold, @dbid, @rcsi, @offset, @major, @kread, @kakapoErr, @cut;",
		// Phase 5e: kakapo's files first, filtered the same way; system_health only before them.
		"IF ISNULL(@kpath, N'') <> N''", "WHERE object_name = N''blocked_process_report''", "@p = @kpath, @s = @since",
		"/event/data[@name=\"blocked_process\"]/value/blocked-process-report", "(/event/data[@name=\"duration\"]/value/text())[1]",
		"IF @cut IS NULL OR @cut > @since", "WHERE t.ts >= @since AND (@cut IS NULL OR t.ts < @cut);",
		"CASE WHEN @kstart IS NULL THEN @kfirst WHEN @kfirst IS NULL OR @kstart < @kfirst THEN @kstart ELSE @kfirst END",
		"bg_inputbuf, src\nFROM @pairs ORDER BY event_utc;",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("history query lacks %q", want)
		}
	}
	n := blockingNamesQuery
	if c := strings.Count(n, "BEGIN TRY"); c != 3 {
		t.Errorf("%d TRY blocks in the names query: the names, the pages, the hashes", c)
	}
	for _, want := range []string{"QUOTENAME(@db) + N'.sys.sp_executesql'", "sys.partitions AS p ON p.hobt_id", "sys.dm_db_page_info(DB_ID()",
		"IF @major >= 15", "sys.query_store_query_text AS t ON t.query_sql_text = q2.txt", "CONVERT(varchar(20), q.query_hash, 1)",
		"SELECT @namesErr, @pagesErr, @hashErr;"} {
		if !strings.Contains(n, want) {
			t.Errorf("names query lacks %q", want)
		}
	}
}

// What the names query is asked: the resources and procedures of this database only, each once, and the
// distinct blocker statements, escaped for XML.
func TestBlockingNamesXML(t *testing.T) {
	reports := []domain.BlockedProcessReport{
		{Blocked: domain.BlockedProcess{WaitResource: "KEY: 7:72057594043039744 (8194443284a0)"},
			Blocking: domain.BlockedProcess{SPID: 83, InputBuffer: "UPDATE dbo.T SET a = 'x<y' WHERE b = 1\x01"}},
		{Blocked: domain.BlockedProcess{WaitResource: "KEY: 7:72057594043039744 (11aa)"},
			Blocking: domain.BlockedProcess{SPID: 83, InputBuffer: "UPDATE dbo.T SET a = 'x<y' WHERE b = 1\x01"}},
		{Blocked: domain.BlockedProcess{WaitResource: "RID: 7:1:12345:2", InputBuffer: "Proc [Database Id = 7 Object Id = 55]"},
			Blocking: domain.BlockedProcess{SPID: 83, InputBuffer: "Proc [Database Id = 7 Object Id = 1234]"}},
		{Blocked: domain.BlockedProcess{WaitResource: "OBJECT: 9:999:0"}, Blocking: domain.BlockedProcess{SPID: 83, InputBuffer: "Proc [Database Id = 9 Object Id = 8]"}},
	}
	res, texts, pages := blockingNamesXML(reports, 7)
	want := `<r><h k="h:72057594043039744" id="72057594043039744"/><p k="p:1:12345" f="1" pg="12345"/><o k="o:55" id="55"/>` +
		`<o k="o:1234" id="1234"/><q i="0">UPDATE dbo.T SET a = &#39;x&lt;y&#39; WHERE b = 1</q></r>`
	if res != want || len(texts) != 1 || !pages {
		t.Errorf("names XML\n got %s\nwant %s\n%q %v", res, want, texts, pages)
	}
	var parsed struct{}
	if err := xml.Unmarshal([]byte(res), &parsed); err != nil {
		t.Errorf("not XML: %v", err)
	}

	applyBlockingNames(reports, 7, map[string]string{"h:72057594043039744": "dbo.T", "p:1:12345": "dbo.Heap", "o:1234": "dbo.PostIntegration"},
		map[int]string{0: "0x1A2B3C4D5E6F7081"}, texts)
	if reports[0].Blocked.Object != "dbo.T" || reports[1].Blocked.Object != "dbo.T" || reports[2].Blocked.Object != "dbo.Heap" ||
		reports[2].Blocking.Procedure != "dbo.PostIntegration" || reports[2].Blocked.Procedure != "" || reports[3].Blocked.Object != "" ||
		reports[3].Blocking.Procedure != "" || reports[0].Blocking.QueryHash != "0x1A2B3C4D5E6F7081" || reports[2].Blocking.QueryHash != "" {
		t.Errorf("names applied %+v", reports)
	}

	if res, _, _ := blockingNamesXML(nil, 7); res != "<r></r>" {
		t.Errorf("nothing to name %s", res)
	}
}

func TestParseReportTime(t *testing.T) {
	if got := ParseReportTime("2026-11-03T14:05:01.233"); !got.Equal(time.Date(2026, 11, 3, 14, 5, 1, 233000000, time.UTC)) {
		t.Errorf("time %v", got)
	}
	if got := ParseReportTime("2026-11-03T14:05:01"); !got.Equal(time.Date(2026, 11, 3, 14, 5, 1, 0, time.UTC)) {
		t.Errorf("time %v", got)
	}
	for _, s := range []string{"", "1900-01-01T00:00:00 junk", "never"} {
		if !ParseReportTime(s).IsZero() {
			t.Errorf("%q is not a time", s)
		}
	}
}

// A head-only report (no blocker) asks Query Store for the head's own statement and gets its hash.
func TestBlockingNamesHeadOnly(t *testing.T) {
	reports := []domain.BlockedProcessReport{{Blocked: domain.BlockedProcess{SPID: 58, TranCount: 1, InputBuffer: "BEGIN TRAN; WAITFOR DELAY '00:40:00';"}}}
	res, texts, _ := blockingNamesXML(reports, 7)
	if len(texts) != 1 || texts[0] != "BEGIN TRAN; WAITFOR DELAY '00:40:00';" || !strings.Contains(res, `<q i="0">`) {
		t.Errorf("head statement %s %q", res, texts)
	}
	applyBlockingNames(reports, 7, map[string]string{}, map[int]string{0: "0x00000000000000AA"}, texts)
	if reports[0].Blocked.QueryHash != "0x00000000000000AA" || reports[0].Blocking.QueryHash != "" || !reports[0].HeadOnly() {
		t.Errorf("head hash %+v", reports[0])
	}
}
