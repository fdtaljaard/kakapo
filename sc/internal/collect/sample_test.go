package collect

import (
	"context"
	"os"
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
)

// readOnly fails the test when a query writes anything outside table variables and temporary
// tables (the LIKE patterns are text to match, not statements).
func readOnly(t *testing.T, name, q string) {
	t.Helper()
	upper := strings.ToUpper(likeLiterals.ReplaceAllString(q, "LIKE N''"))
	for _, verb := range []string{"ALTER ", "DROP ", "TRUNCATE ", "DBCC ", "CREATE INDEX", "SP_CONFIGURE", "KILL ", "MERGE "} {
		if strings.Contains(upper, verb) {
			t.Errorf("%s query contains %q", name, verb)
		}
	}
	for _, line := range strings.Split(upper, "\n") {
		for _, verb := range []string{"INSERT ", "UPDATE ", "DELETE "} {
			if i := strings.Index(line, verb); i >= 0 && !strings.Contains(line[i:], "@") && !strings.Contains(line[i:], "#") {
				t.Errorf("%s query writes: %s", name, line)
			}
		}
	}
}

// The sample's and the index fixes' reads are tagged, once, and read-only (docs/phase4c-contract.md, Tests).
func TestSampleAndIndexQueries(t *testing.T) {
	text, err := sampleTextQuery([]string{"0xAAAA000000000001", "0x00ff"})
	if err != nil {
		t.Fatal(err)
	}
	for name, q := range map[string]string{"snapshot": sampleSnapshotQuery, "texts": text, "index batch": indexFactsBatch("Granite]Live"),
		"index facts": Tag(indexFactsQuery)} {
		if !strings.HasPrefix(q, KakapoTag+" ") || strings.Count(q, KakapoTag) < 1 || strings.HasPrefix(q, KakapoTag+" "+KakapoTag) {
			t.Errorf("%s query is not tagged once: %.60s", name, q)
		}
		readOnly(t, name, q)
	}
	if !strings.Contains(text, "FROM (VALUES (0xAAAA000000000001), (0x00ff)) AS h(query_hash)") ||
		!strings.Contains(text, "NOT LIKE N'%/* kakapo */%'") || !strings.Contains(text, noiseFilter("x.stmt")) {
		t.Errorf("text query:\n%s", text)
	}
	for _, bad := range [][]string{{"0xZZ"}, {"1; DROP TABLE x"}, {"0x" + strings.Repeat("A", 17)}, {}} {
		if _, err := sampleTextQuery(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if !strings.Contains(sampleSnapshotQuery, "INSERT @benign (wait_type) VALUES (N'BROKER_EVENTHANDLER')") {
		t.Error("the snapshot leaves out the benign waits")
	}
	if b := indexFactsBatch("Granite]Live"); !strings.Contains(b, "EXEC [Granite]]Live].sys.sp_executesql @stmt, N'@schema nvarchar(128), @table nvarchar(128)', @schema = @schema, @table = @table;") {
		t.Errorf("index batch %s", b)
	}
}

func TestSampleLive(t *testing.T) {
	host, name := os.Getenv("SQLMONITOR_TEST_HOST"), os.Getenv("SQLMONITOR_TEST_DB")
	if host == "" || name == "" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_DB to read a live instance")
	}
	ctx := context.Background()
	db, err := Open(ctx, domain.ServerInput{Host: host, Auth: domain.AuthWindows})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	in, err := Sample(ctx, db, name, 10)
	if err != nil {
		t.Fatal(err)
	}
	s := domain.BuildPerfSample(in, in.Second.At)
	t.Logf("verdict: %s", s.Verdict)
	for _, l := range s.Lines {
		t.Logf("  %s: %s", l.Area, l.Text)
	}
	t.Logf("waits %+v\ncpu %+v\nqueries %+v\nfiles %+v", s.Waits, s.CPU, s.Queries, s.Files)
}
