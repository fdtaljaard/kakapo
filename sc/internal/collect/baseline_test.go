package collect

import (
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
)

// The baseline read (docs/phase5w-contract.md, A1) is tagged, SELECT-only, each area in its own TRY.
func TestBaselineQuery(t *testing.T) {
	readOnly(t, "baseline", baselineQuery)
	q := Tag(baselineQuery)
	if !strings.HasPrefix(q, KakapoTag) {
		t.Error("the baseline read is not tagged")
	}
	if n := strings.Count(baselineQuery, "BEGIN TRY"); n < 7 {
		t.Errorf("%d TRY blocks: every area (configuration, server, memory, databases, files, jobs, kakapo) needs its own", n)
	}
	for _, want := range []string{"sys.configurations", "sys.databases", "sys.master_files", "msdb.dbo.sysjobs", "msdb.dbo.sysjobsteps",
		"msdb.dbo.sysjobschedules", "kakapo_ChangeLog", "VIEW SERVER STATE", "physical_memory_in_use_kb", "committed_target_kb",
		"sql_memory_model_desc", "ProductUpdateLevel", "sqlserver_start_time"} {
		if !strings.Contains(baselineQuery, want) {
			t.Errorf("the baseline read lacks %q", want)
		}
	}
	if strings.Contains(strings.ToUpper(baselineQuery), "CONNECTION TIMEOUT") || strings.Contains(baselineQuery, "SUM(") {
		t.Error("no socket deadline, no SUM")
	}
}

// The configuration changes come from 5f's @trace: one fn_trace_gettable in the Upkeep read, and the event
// 22 rows kept at any age by their own branch after MNT06's.
func TestTraceReadKeepsConfigurationChanges(t *testing.T) {
	if n := strings.Count(strings.ToLower(upkeepInstanceQuery), "fn_trace_gettable"); n != 1 {
		t.Fatalf("%d reads of the default trace, want one", n)
	}
	mnt := strings.Index(upkeepInstanceQuery, "x.EventClass IN (22, 92, 93, 94, 95, 116) THEN 1")
	cfg := strings.Index(upkeepInstanceQuery, "x.EventClass = 22 AND CONVERT(nvarchar(400), x.TextData) LIKE N'%Configuration option ''%'' changed from % to %' THEN 3")
	oldest := strings.Index(upkeepInstanceQuery, "WHEN x.StartTime = x.Oldest THEN 0")
	if mnt < 0 || cfg < 0 || !(mnt < cfg && cfg < oldest) {
		t.Fatalf("CASE order: MNT06 %d, configuration %d, oldest %d", mnt, cfg, oldest)
	}
	if !strings.Contains(upkeepInstanceQuery, "FROM @trace AS t\nWHERE t.EventClass = 22 AND t.TextData LIKE N'%Configuration option") {
		t.Error("the configuration changes are not read from @trace")
	}
}

func TestBaselineValues(t *testing.T) {
	if TraceFlagList(nil) != "none" || TraceFlagList([]int{3226, 1117}) != "1117, 3226" {
		t.Error(TraceFlagList([]int{3226, 1117}))
	}
	_ = domain.Str
}
