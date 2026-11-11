package domain

import (
	"fmt"
	"regexp"
	"strings"
)

// MNT09 (docs/phase5q-contract.md, section 1): a home-grown procedure that decides for itself which
// indexes to rebuild or reorganise, run on a schedule. kakapo sees it when an enabled Agent job names
// it, or (no Agent, or no job names it) when index statistics show a rebuild's fingerprint: three or
// more big indexes whose statistics read every row, in one hour of the last 8 days, with no other
// index job to explain them. "Three in one hour" is kakapo's detection rule, not advice.
//
// Sources: Microsoft Learn, "Maintain indexes optimally" (don't assume index maintenance noticeably
// improves the workload or must run on a fixed schedule; an offline rebuild holds object-level locks
// for its duration; a rebuild updates the index's statistics as with FULLSCAN; "the same benefit can
// often be achieved at a much lower resource cost by updating statistics"); "Editions and supported
// features of SQL Server 2022" (online index rebuild is Enterprise only).

// IndexProcedure is a procedure of the database whose code alters indexes it picks from
// sys.dm_db_index_physical_stats (or DBCC SHOWCONTIG), with the enabled jobs that name it.
type IndexProcedure struct {
	Schema      string
	Name        string
	Rebuilds    bool     // REBUILD or DBREINDEX in its code
	Reorganizes bool     // REORGANIZE or INDEXDEFRAG in its code
	Online      bool     // ONLINE = ON in its code (a rebuild is offline without it, on every edition)
	Jobs        []string // enabled Agent jobs whose step names it
}

// IndexJobStep is an enabled job step that maintains indexes another way (IndexOptimize, ALTER INDEX,
// DBREINDEX) and names none of the procedures: it explains a rebuild's fingerprint on its own.
type IndexJobStep struct {
	Job     string
	Command string
}

// Mnt09FingerprintMin is how many big indexes rebuilt in one hour count as a scheduled rebuild.
const Mnt09FingerprintMin = 3

// Mnt09FingerprintPages is the floor of a big index: measured on SQL Server 2022 on 2026-11-10,
// sp_updatestats read every row of a clustered index up to 1,105 pages and sampled from 1,507 pages,
// so above 2,000 pages a statistics update that read every row is a rebuild (or a FULLSCAN).
const Mnt09FingerprintPages = 2000

var olaIndexWorkRe = regexp.MustCompile(`(?i)IndexOptimize`)

// indexJobExplains says whether another enabled job maintains indexes: Ola's IndexOptimize with any
// fragmentation work (not both levels NULL), or plain ALTER INDEX / DBREINDEX.
func indexJobExplains(steps []IndexJobStep) string {
	for _, s := range steps {
		if olaIndexWorkRe.MatchString(s.Command) {
			if !(olaNoMediumRe.MatchString(s.Command) && olaNoHighRe.MatchString(s.Command)) {
				return s.Job
			}
			continue
		}
		return s.Job
	}
	return ""
}

const (
	mnt09Why = "A rebuild is offline unless it asks for ONLINE = ON, and on Standard and Express it always is: each table is locked while its indexes are rebuilt. Rebuilding or reorganising every index that crosses a small threshold costs log, I/O and time, and Microsoft advises against index maintenance on a fixed schedule. The speed-up people see afterwards usually comes from the statistics a rebuild refreshes, which a statistics update gives for much less."
	mnt09Do  = "Stop it (the job, or the scheduled task that calls it), and use the maintenance plan's statistics job instead; on Express, the maintenance procedure. Leave index work off unless a query shows the need: the Indexes page on this card lists the indexes your slowest queries read in full, measures them and reorganises the ones you tick."
)

// mnt09: one finding per procedure that is at work. Rank 3 when its rebuilds are offline (no ONLINE =
// ON in its code, or an edition that cannot rebuild online), else 4.
func (b *upkeepBuild) mnt09() {
	f := b.f
	if f.IndexJobError != "" {
		b.skip("Could not look for a home-grown index job: "+f.IndexJobError, "MNT09")
		return
	}
	other := indexJobExplains(f.IndexJobSteps)
	fingerprint := f.RebuildCount >= Mnt09FingerprintMin && f.RebuildHour != nil && other == ""
	for _, p := range f.IndexProcedures {
		if len(p.Jobs) == 0 && !fingerprint {
			continue
		}
		rank := 4
		if p.Rebuilds && !(p.Online && OnlineIndexEdition(f.EngineEdition)) {
			rank = 3
		}
		what := "reorganises"
		switch {
		case p.Rebuilds && p.Reorganizes:
			what = "rebuilds and reorganises"
		case p.Rebuilds:
			what = "rebuilds"
		}
		name := p.Schema + "." + p.Name
		how := []string{}
		if len(p.Jobs) > 0 {
			how = append(how, pluralWord(int64(len(p.Jobs)), "job ", "jobs ")+quoteList(p.Jobs))
		} else {
			how = append(how, "called from outside SQL Server Agent")
		}
		if f.RebuildHour != nil && f.RebuildCount > 0 {
			how = append(how, fmt.Sprintf("last seen at work %s, %s", f.RebuildHour.Format("2006-01-02 15:04"), plural(int64(f.RebuildCount), "index", "indexes")))
		}
		finding := fmt.Sprintf("%s %s indexes on a schedule (%s).", name, what, strings.Join(how, "; "))
		evidence := fmt.Sprintf("Procedure %s: its code alters the indexes it picks from their fragmentation.", name)
		if len(p.Jobs) > 0 {
			evidence += " Enabled jobs that run it: " + quoteList(p.Jobs) + "."
		}
		if f.RebuildHour != nil && f.RebuildCount > 0 {
			evidence += fmt.Sprintf(" Index statistics that read every row (indexes over %s pages, where a statistics update samples): %s in the hour from %s.",
				n0int(Mnt09FingerprintPages), n0int(int64(f.RebuildCount)), f.RebuildHour.Format("2006-01-02 15:04"))
			if h := f.RebuildHour.Hour(); h >= 5 && h < 22 {
				evidence += " That hour is outside the maintenance plan's night (22:00 to 05:00): an offline rebuild then locks tables while people work."
			}
		}
		if other != "" {
			evidence += " The job \"" + other + "\" maintains indexes too."
		}
		if f.IndexJobStepsError != "" {
			evidence += " The Agent jobs could not be read: " + f.IndexJobStepsError
		}
		b.add("MNT09", rank, "Online", EffortModerate, name, finding, mnt09Why, mnt09Do, evidence, "")
	}
}

func quoteList(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = "\"" + x + "\""
	}
	return strings.Join(q, ", ")
}
