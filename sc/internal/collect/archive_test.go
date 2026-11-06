package collect

import (
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
)

// The archive reads (docs/phase5a-contract.md): every batch is tagged and safe, only reads, and each
// area sits in its own TRY with its error column.
func TestArchiveReadsTaggedAndReadOnly(t *testing.T) {
	estimate, err := archiveEstimateStatement(archiveCollectFacts(), "LoggedAt", "", false)
	if err != nil {
		t.Fatal(err)
	}
	sample, err := archiveEstimateStatement(archiveCollectFacts(), "LoggedAt", "", true)
	if err != nil {
		t.Fatal(err)
	}
	reads := map[string]string{
		"instance": Tag(archiveInstanceQuery), "table": Tag(archiveTableQuery), "target": Tag(archiveTargetQuery),
		"jobs": Tag(archiveJobsQuery), "list": Tag(listTablesQuery), "count": estimate, "sample": sample,
		"in database": inDatabaseWithTable("Shop"),
	}
	for name, q := range reads {
		if !strings.HasPrefix(q, KakapoTag+" ") {
			t.Errorf("%s is not tagged", name)
		}
		upper := strings.ToUpper(q)
		for _, write := range []string{"SP_ADD_JOB", "SP_DELETE_JOB", "SP_START_JOB", "SP_UPDATE_JOB", "INSERT INTO MSDB", "UPDATE MSDB",
			"DELETE ", "DROP ", "ALTER ", "CREATE ", "TRUNCATE", "MERGE INTO", "SELECT TOP (0)", " INTO [", "IDENTITY_INSERT", "UPDATE STATISTICS"} {
			if strings.Contains(upper, write) {
				t.Errorf("%s writes: %q", name, write)
			}
		}
	}
	for name, q := range map[string]string{"instance": archiveInstanceQuery, "jobs": archiveJobsQuery, "in database": inDatabaseWithTable("Shop")} {
		if !strings.Contains(q, safeSession) {
			t.Errorf("%s does not set the safe session", name)
		}
	}
	for _, c := range []struct {
		name string
		q    string
		trys int
		errs []string
	}{
		{"instance", archiveInstanceQuery, 3, []string{"@agentError", "@logError", "@roomError"}},
		{"table", archiveTableQuery, 5, []string{"@tableError", "@colsError", "@keysError", "@trgError", "@featError"}},
		{"target", archiveTargetQuery, 1, []string{"@error"}},
		{"jobs", archiveJobsQuery, 1, []string{"@jobsError"}},
		{"list", listTablesQuery, 2, []string{"@tablesError", "@jobsError"}},
	} {
		if n := strings.Count(c.q, "BEGIN TRY"); n != c.trys || strings.Count(c.q, "BEGIN CATCH") != c.trys {
			t.Errorf("%s: %d TRY blocks, want %d", c.name, n, c.trys)
		}
		for _, e := range c.errs {
			if !strings.Contains(c.q, "SET "+e+" = ERROR_MESSAGE();") {
				t.Errorf("%s: %s is not an error column", c.name, e)
			}
		}
	}
	// The dynamic SQL inside the reads carries the tag too.
	for _, want := range []string{"N'/* kakapo */ SELECT @m = is_memory_optimized", "N'/* kakapo */ SELECT @t = CAST(temporal_type_desc"} {
		if !strings.Contains(archiveTableQuery, want) {
			t.Errorf("table read lacks %q", want)
		}
	}
	for _, want := range []string{
		"sys.foreign_keys AS fk WHERE fk.referenced_object_id = @obj", "fk.parent_object_id = @obj", "is_tracked_by_cdc",
		"sys.change_tracking_tables", "is_merge_published", "is_replicated", "o.is_published", "tr.is_instead_of_trigger",
		"ic.key_ordinal = 1", "i.has_filter = 0", "TYPE_NAME(c.system_type_id) IN (N'datetime', N'datetime2', N'date')",
		"COL_LENGTH(N'sys.tables', N'temporal_type_desc')",
	} {
		if !strings.Contains(archiveTableQuery, want) {
			t.Errorf("table read lacks %q", want)
		}
	}
	for _, want := range []string{"jh.step_id = 1 ORDER BY jh.instance_id DESC", "LEFT(j.name, LEN(@prefix)) = @prefix", "N'kakapo: archive ' + @db + N'.'"} {
		if !strings.Contains(archiveJobsQuery, want) {
			t.Errorf("jobs read lacks %q", want)
		}
	}
}

func archiveCollectFacts() domain.ArchiveFacts {
	return domain.ArchiveFacts{Database: "Shop", Schema: "dbo", Table: "Mail]Log", Rows: 500000,
		Columns: []domain.ArchiveColumn{{Name: "Id", Type: "int"}, {Name: "LoggedAt", Type: "datetime2", DateIndexed: true},
			{Name: "Status", Type: "nvarchar"}, {Name: "Sent]On", Type: "datetime"}}}
}

// The estimate's two paths, and the condition: checked before any statement is built, and only its
// canonical form in the statement.
func TestArchiveEstimateStatement(t *testing.T) {
	f := archiveCollectFacts()
	count, err := archiveEstimateStatement(f, "loggedat", "status = 'Sent'", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"COUNT_BIG(*)", "MIN([LoggedAt])", "FROM [dbo].[Mail]]Log] WITH (READUNCOMMITTED) WHERE [LoggedAt] < @cutoff AND ([Status] = 'Sent');",
		"DATEADD(MONTH, -@keep, SYSDATETIME())"} {
		if !strings.Contains(count, want) {
			t.Errorf("count lacks %q:\n%s", want, count)
		}
	}
	if strings.Contains(count, "TABLESAMPLE") {
		t.Error("the indexed path samples")
	}
	sample, err := archiveEstimateStatement(f, "Sent]On", "", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"COUNT_BIG(CASE WHEN [Sent]]On] < @cutoff THEN 1 END), COUNT_BIG(*)", "FROM [dbo].[Mail]]Log] TABLESAMPLE SYSTEM (1 PERCENT) WITH (READUNCOMMITTED);"} {
		if !strings.Contains(sample, want) {
			t.Errorf("sample lacks %q:\n%s", want, sample)
		}
	}
	if !strings.Contains(sample, "COUNT_BIG(*), CAST(NULL AS datetime2)\n") {
		t.Errorf("sample without a clustered identity reads an oldest:\n%s", sample)
	}
	// Clustered on an ascending identity: the sample reads the oldest through that key (a seek), as
	// IDX10 does, so the preview does not contradict it.
	keyed := archiveCollectFacts()
	keyed.Columns[0].ClusteredKey = true
	sample, err = archiveEstimateStatement(keyed, "Sent]On", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if want := "COUNT_BIG(*), (SELECT TOP (1) CONVERT(datetime2, [Sent]]On]) FROM [dbo].[Mail]]Log] WITH (READUNCOMMITTED) ORDER BY [Id])\nFROM [dbo].[Mail]]Log] TABLESAMPLE"; !strings.Contains(sample, want) {
		t.Errorf("sample lacks %q:\n%s", want, sample)
	}
	for _, bad := range []string{"Status = 'x'; DROP TABLE dbo.T", "Status = 'x' --", "Id IN (SELECT Id FROM T)", "Nope = 1", "@x = 1"} {
		if stmt, err := archiveEstimateStatement(f, "LoggedAt", bad, false); err == nil || stmt != "" {
			t.Errorf("condition %q reached a statement: %q", bad, stmt)
		}
	}
	if _, err := archiveEstimateStatement(f, "Status", "", false); err == nil {
		t.Error("a column that is not a date was counted on")
	}
	// The estimate refuses before it touches the server (db is nil).
	if e := ArchiveEstimate(t.Context(), nil, f, "LoggedAt", 12, "Id = @x"); e.Known || !strings.Contains(e.Error, "variable") {
		t.Errorf("estimate %+v", e)
	}
}

func TestRowsMovedLine(t *testing.T) {
	msg := "Executed as user: NT SERVICE\\SQLAgent. rows moved: 15000 [SQLSTATE 01000] (Message 0).  The step succeeded."
	if m := rowsMovedLine.FindStringSubmatch(msg); m == nil || m[1] != "15000" {
		t.Errorf("rows moved %v", m)
	}
}
