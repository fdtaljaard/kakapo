package collect

import (
	"strings"
	"testing"
	"time"
)

// kakapo's change log on the server (docs/phase5w-contract.md, B5-B6): the reads are SELECT-only; the one
// write touches only kakapo_ChangeLog, only when it is there and only a row it lacks.
func TestChangeLogReadsAndTheWrite(t *testing.T) {
	for name, q := range map[string]string{"facts": changeLogFactsQuery, "installed": changeLogInstalledQuery, "keys": changeLogKeysQuery,
		"rows": changeLogRowsQuery} {
		readOnly(t, name, q)
	}
	for _, want := range []string{"IF OBJECT_ID(N'master.dbo.kakapo_ChangeLog', N'U') IS NULL\n    SELECT 0;",
		"INSERT INTO master.dbo.kakapo_ChangeLog (", "WHERE NOT EXISTS (SELECT 1 FROM master.dbo.kakapo_ChangeLog",
		"ISNULL(@SqlLogin, ORIGINAL_LOGIN())", "CONVERT(datetime2(3), @StartedAt, 126)"} {
		if !strings.Contains(changeLogInsert, want) {
			t.Errorf("the write lacks %q", want)
		}
	}
	upper := strings.ToUpper(changeLogInsert)
	for _, verb := range []string{"UPDATE ", "DELETE ", "TRUNCATE ", "DROP ", "ALTER "} {
		if strings.Contains(upper, verb) {
			t.Errorf("the write contains %q", verb)
		}
	}
	if strings.Count(upper, "INSERT ") != 1 {
		t.Error("the write inserts more than once")
	}
	if got := changeLogTime(time.Date(2026, 11, 12, 12, 20, 30, 123456000, time.FixedZone("SAST", 7200))); got != "2026-11-12T10:20:30.123" {
		t.Errorf("time %q", got)
	}
	if cut("ab€d", 3) != "ab€" || cut("abc", 5) != "abc" {
		t.Error("cut")
	}
}
