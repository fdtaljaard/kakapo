package collect

import (
	"context"
	"strings"
	"testing"
)

// The Upkeep actions' reads are tagged and read-only (docs/phase4d-contract.md, Tests): msdb through
// sp_executesql in its own TRY, nothing that adds, updates or deletes a job, an alert or an operator.
func TestUpkeepActionReads(t *testing.T) {
	for name, q := range map[string]string{"alerts": Tag(alertFactsQuery), "maintenance jobs": Tag(maintenanceJobQuery),
		"jobs by name": Tag(jobsByNameQuery), "job steps": Tag(jobStepsQuery)} {
		suReadOnly(t, name, q)
	}
	// Operators, alerts and notifications; Database Mail; the Agent's mail profile; the Agent.
	if n := strings.Count(alertFactsQuery, "BEGIN TRY"); n != 4 {
		t.Errorf("alerts query: %d TRY blocks", n)
	}
	for _, want := range []string{"msdb.dbo.sysoperators", "msdb.dbo.sysalerts", "msdb.dbo.sysnotifications", "msdb.dbo.sysmail_profile",
		"N'DatabaseMailProfile'", "event_description_keyword", "sys.dm_server_services"} {
		if !strings.Contains(alertFactsQuery, want) {
			t.Errorf("alerts query lacks %q", want)
		}
	}
	for _, want := range []string{"OBJECT_ID(N'master.dbo.IndexOptimize', N'P')", "OBJECT_ID(N'master.dbo.CommandExecute', N'P')",
		"WHERE name IN (@a, @b)", "o.enabled = 1 AND o.email_address IS NOT NULL", "GETDATE()"} {
		if !strings.Contains(maintenanceJobQuery, want) {
			t.Errorf("maintenance query lacks %q", want)
		}
	}
	for _, want := range []string{"start_step_id", "on_success_step_id", "on_fail_step_id", "msdb.dbo.sysproxies", "command nvarchar(max)"} {
		if !strings.Contains(jobStepsQuery, want) {
			t.Errorf("job steps query lacks %q", want)
		}
	}
	// Reading back more jobs than the query names is refused before anything is sent.
	if _, err := ReadJobsByName(context.Background(), nil, []string{"a", "b", "c"}); err == nil {
		t.Error("three jobs")
	}
	if _, err := ReadJobsByName(context.Background(), nil, nil); err == nil {
		t.Error("no job")
	}
}
