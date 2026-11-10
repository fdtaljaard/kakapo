package domain

import (
	"strings"
	"testing"
	"time"
)

func mnt09Findings(f UpkeepFacts) []Finding {
	_, fs, _ := runUpkeep(f)
	out := []Finding{}
	for _, x := range fs {
		if x.CheckID == "MNT09" {
			out = append(out, x)
		}
	}
	return out
}

func TestMNT09(t *testing.T) {
	night := time.Date(2026, 11, 10, 2, 0, 0, 0, time.UTC)
	proc := IndexProcedure{Schema: "dbo", Name: "RebuildIndexes", Rebuilds: true, Reorganizes: true}

	// A job runs it, on Standard: rank 3 (offline rebuilds).
	f := goodUpkeep()
	p := proc
	p.Jobs = []string{"Nightly indexes"}
	f.IndexProcedures, f.RebuildHour, f.RebuildCount = []IndexProcedure{p}, &night, 41
	fs := mnt09Findings(f)
	if len(fs) != 1 {
		t.Fatalf("findings %+v", fs)
	}
	x := fs[0]
	if x.Finding != `dbo.RebuildIndexes rebuilds and reorganises indexes on a schedule (job "Nightly indexes"; last seen at work 2026-11-10 02:00, 41 indexes).` ||
		x.Severity != SeverityFromRank(3) || x.Object != "dbo.RebuildIndexes" || x.FixID != "" || x.Owner != OwnerUs {
		t.Errorf("%+v", x)
	}
	if strings.Contains(x.Evidence, "outside the maintenance plan's night") {
		t.Errorf("02:00 is in the night: %s", x.Evidence)
	}

	// Enterprise without ONLINE = ON still rebuilds offline: rank 3.
	f.EngineEdition = 3
	if fs := mnt09Findings(f); len(fs) != 1 || fs[0].Severity != SeverityFromRank(3) {
		t.Errorf("enterprise offline %+v", fs)
	}
	// Enterprise with ONLINE = ON: rank 4. A daytime hour is said.
	f.IndexProcedures[0].Online = true
	day := time.Date(2026, 11, 10, 11, 0, 0, 0, time.UTC)
	f.RebuildHour = &day
	if fs := mnt09Findings(f); len(fs) != 1 || fs[0].Severity != SeverityFromRank(4) || !strings.Contains(fs[0].Evidence, "outside the maintenance plan's night") {
		t.Errorf("enterprise %+v", fs)
	}

	// Express: no job, the fingerprint alone.
	f = goodUpkeep()
	f.EngineEdition = 4
	f.IndexProcedures, f.RebuildHour, f.RebuildCount = []IndexProcedure{proc}, &night, 5
	if fs := mnt09Findings(f); len(fs) != 1 || !strings.Contains(fs[0].Finding, "called from outside SQL Server Agent") {
		t.Errorf("express %+v", fs)
	}
	// Too few in the hour, or Ola's IndexOptimize maintaining indexes: silent.
	f.RebuildCount = 2
	if fs := mnt09Findings(f); len(fs) != 0 {
		t.Errorf("two indexes %+v", fs)
	}
	f.RebuildCount = 5
	f.IndexJobSteps = []IndexJobStep{{Job: "IndexOptimize - USER_DATABASES", Command: "EXECUTE dbo.IndexOptimize @Databases = 'USER_DATABASES'"}}
	if fs := mnt09Findings(f); len(fs) != 0 {
		t.Errorf("ola index job %+v", fs)
	}
	// Ola's statistics-only job does not explain a rebuild.
	f.IndexJobSteps = []IndexJobStep{{Job: "stats", Command: "EXECUTE dbo.IndexOptimize @Databases = 'X', @FragmentationLow = NULL, @FragmentationMedium = NULL, @FragmentationHigh = NULL, @UpdateStatistics = 'ALL'"}}
	if fs := mnt09Findings(f); len(fs) != 1 {
		t.Errorf("ola statistics job %+v", fs)
	}
	// No procedure: silent, whatever the statistics say.
	f.IndexProcedures = nil
	if fs := mnt09Findings(f); len(fs) != 0 {
		t.Errorf("no procedure %+v", fs)
	}
	// A reorganise-only procedure with a job, no fingerprint.
	f = goodUpkeep()
	p = IndexProcedure{Schema: "maint", Name: "Defrag", Reorganizes: true, Jobs: []string{"Defrag"}}
	f.IndexProcedures = []IndexProcedure{p}
	if fs := mnt09Findings(f); len(fs) != 1 || fs[0].Finding != `maint.Defrag reorganises indexes on a schedule (job "Defrag").` || fs[0].Severity != SeverityFromRank(4) {
		t.Errorf("reorganise only %+v", fs)
	}
	// The read failed: skipped.
	f = goodUpkeep()
	f.IndexJobError = "permission denied"
	if _, _, cs := runUpkeep(f); statusOf(cs, "MNT09").Status != CheckSkipped {
		t.Errorf("error: %+v", statusOf(cs, "MNT09"))
	}
}
