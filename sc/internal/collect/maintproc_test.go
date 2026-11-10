package collect

import (
	"strings"
	"testing"
)

// The reads of kakapo's maintenance procedure are tagged once with the safe session first and read-only
// (docs/phase5p-contract.md, Tests): nothing creates, alters or drops; the tables are read through tagged
// sp_executesql batches in their own TRY, so a server without them answers.
func TestMaintProcReads(t *testing.T) {
	for name, q := range map[string]string{"procedure": Tag(maintProcQuery), "runs": Tag(maintRunsQuery), "mark": Tag(maintMarkQuery)} {
		suReadOnly(t, name, q)
		upper := strings.ToUpper(q)
		for _, verb := range []string{"CREATE ", "DROP ", "EXEC MASTER.DBO.KAKAPO_MAINTENANCE", "SP_GETAPPLOCK"} {
			if strings.Contains(upper, verb) {
				t.Errorf("%s query contains %q", name, verb)
			}
		}
		if n, tagged := strings.Count(q, "sp_executesql N'"), strings.Count(q, "sp_executesql N'/* kakapo */"); n != tagged || n == 0 {
			t.Errorf("%s query: %d dynamic batches, %d tagged", name, n, tagged)
		}
	}
	if n := strings.Count(maintProcQuery, "BEGIN TRY"); n != 3 {
		t.Errorf("%d TRY blocks in the read: the objects, the backup folder, the tables", n)
	}
	for _, want := range []string{"OBJECT_ID(N'master.dbo.kakapo_Maintenance', N'P')", "CHARINDEX(N'kakapo maintenance procedure, version ', m.definition)",
		"IF @settingsExists = 1 AND @logExists = 1", "CONVERT(char(5), s.FullTime, 108)", "o.Outcome = ''OK''", "o.Outcome = ''Failed''",
		"WHERE l.ID = (SELECT MAX(x.ID)", "WHERE l.Outcome = ''Failed'' ORDER BY l.ID DESC", "master.dbo.IndexOptimize", "master.dbo.DatabaseIntegrityCheck",
		"master.dbo.DatabaseBackup", "xp_instance_regread"} {
		if !strings.Contains(maintProcQuery, want) {
			t.Errorf("the read lacks %q", want)
		}
	}
}

func TestMaintVersion(t *testing.T) {
	for header, want := range map[string]int{"kakapo maintenance procedure, version 1. Created": 1, "kakapo maintenance procedure, version 12.": 12, "": -1, "other": -1} {
		v, ok := maintVersion(header)
		if (want < 0 && ok) || (want >= 0 && (!ok || v != want)) {
			t.Errorf("%q: %d %v", header, v, ok)
		}
	}
	if maintDayName(1) != "Sunday" || maintDayName(7) != "Saturday" || maintDayName(0) != "Sunday" {
		t.Error("day names")
	}
}
