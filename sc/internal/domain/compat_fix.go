package domain

import (
	"fmt"
	"strconv"
	"strings"
)

// The compat-level fix on DBC06 (2026-11-10): raise a database's compatibility level, with what the
// change does to its queries and whether Query Store holds the days before it, so the Tuning report and
// "did my change help" (phases 5c and 5h) can say afterwards whether it helped. Going back is the
// rollback, and takes effect at once.
//
// Sources: Microsoft Learn, "ALTER DATABASE compatibility level" (the levels per version, plans recompile
// after the change, the recommended upgrade path: keep Query Store on at the old level, raise it, compare,
// force the plans that regressed); "Intelligent query processing in SQL Server databases" (which features
// each level brings); "Change the Database Compatibility Level and use the Query Store".

const (
	FixCompatLevel     = "compat-level"
	OptionCompatTarget = "level" // compat-level: the level to raise to
)

// compatMinQueryStoreDays is the history the preview wants before the change: a working week to
// compare the days after against.
const compatMinQueryStoreDays = 7

// CompatFacts is what the compat-level fix reads about one database.
type CompatFacts struct {
	ServerName      string
	ProductVersion  string
	Database        string
	CompatLevel     int
	QueryStoreState string  // sys.database_query_store_options actual_state_desc; "" when not read
	QueryStoreDays  float64 // days since the oldest Query Store interval; 0 when none
	QueryStoreError string
}

// compatFeatures are what each level brings to a database's queries (Microsoft Learn, "Intelligent
// query processing": the feature table's compatibility level column).
var compatFeatures = map[int]string{
	120: "the newer cardinality estimator replaces the legacy one, the largest single change to plans",
	140: "interleaved execution for multi-statement table-valued functions, and adaptive joins and memory grant feedback for batch mode",
	150: "scalar UDF inlining, table variable deferred compilation, memory grant feedback in row mode, and batch mode on rowstore (Enterprise edition only)",
	160: "parameter sensitive plan optimization, DOP feedback, and cardinality estimation feedback (these learn from Query Store)",
}

// compatLevels are the levels above current up to the server's, lowest first.
func compatLevels(current, server int) []int {
	out := []int{}
	for l := (current/10 + 1) * 10; l <= server; l += 10 {
		out = append(out, l)
	}
	return out
}

func compatStatement(db string, level int) string {
	return fmt.Sprintf("ALTER DATABASE %s SET COMPATIBILITY_LEVEL = %d;", QuoteName(db), level)
}

// BuildCompatLevelFix builds the compat-level preview: the level option (the server's by default, any
// level between), the statement, the rollback to the current level and the warnings. ErrFixNotApplicable
// when the database is already at the server's level.
func BuildCompatLevelFix(f CompatFacts, options map[string]string) (FixPreview, Fix, error) {
	server := EngineMajor(f.ProductVersion) * 10
	levels := compatLevels(f.CompatLevel, server)
	if f.CompatLevel <= 0 || len(levels) == 0 {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	target := server
	choices := make([]string, len(levels))
	for i, l := range levels {
		choices[i] = strconv.Itoa(l)
	}
	in := FixInput{Name: OptionCompatTarget, Label: "Raise to", Kind: "select", Options: choices, Value: strconv.Itoa(target),
		Help: fmt.Sprintf("%d is the highest this server supports. A step at a time is also fine: each level can be tested on its own.", server)}
	fix := suFix(FixCompatLevel)
	p := FixPreview{Database: f.Database, EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}, Options: map[string]string{}}
	if typed, given := options[OptionCompatTarget]; given && strings.TrimSpace(typed) != "" {
		v, err := strconv.Atoi(strings.TrimSpace(typed))
		ok := err == nil
		if ok {
			ok = false
			for _, l := range levels {
				ok = ok || l == v
			}
		}
		if !ok {
			in.Value = strings.TrimSpace(typed)
			in.Warning = "Choose one of " + strings.Join(choices, ", ") + "."
			p.Inputs, p.Options[OptionCompatTarget] = []FixInput{in}, in.Value
			p.Warnings = append(p.Warnings, in.Warning)
			return p, fix, nil
		}
		target, in.Value = v, strconv.Itoa(v)
	}
	cur, tgt := strconv.Itoa(f.CompatLevel), strconv.Itoa(target)
	fix.Current, fix.Target = cur, tgt
	fix.Options = map[string]string{OptionCompatTarget: tgt}
	p.Inputs, p.Options[OptionCompatTarget] = []FixInput{in}, tgt
	p.Changes = append(p.Changes, Drift{Setting: "COMPATIBILITY_LEVEL", Current: cur, Target: tgt})
	p.Statement = compatStatement(f.Database, target)
	p.Rollback = compatStatement(f.Database, f.CompatLevel)

	p.Warnings = append(p.Warnings, compatQueryStoreWarning(f))
	if brings := compatBrings(f.CompatLevel, target); brings != "" {
		p.Warnings = append(p.Warnings, "What changes for the queries: "+brings+".")
	}
	p.Warnings = append(p.Warnings,
		"Most queries get the same or better plans; a few can get slower. When one does, force its old plan from its query page rather than going back a level.",
		"Every query in "+f.Database+" compiles again under the new level, so CPU rises for a while as plans are rebuilt: apply it in a quiet hour.",
		fmt.Sprintf("Going back is the rollback (COMPATIBILITY_LEVEL = %d) and takes effect at once.", f.CompatLevel),
		"If the application's supplier certifies a compatibility level, stay within it.",
		"Needs the ALTER permission on "+f.Database+" (db_owner).")
	return p, fix, nil
}

// compatBrings lists what the levels above current up to target bring, lowest first.
func compatBrings(current, target int) string {
	parts := []string{}
	for _, l := range compatLevels(current, target) {
		if s, ok := compatFeatures[l]; ok {
			parts = append(parts, fmt.Sprintf("at %d, %s", l, s))
		}
	}
	return strings.Join(parts, "; ")
}

// compatQueryStoreWarning says whether the days before the change are recorded.
func compatQueryStoreWarning(f CompatFacts) string {
	const after = "the Tuning report and \"did my change help\" compare the days after the change against them"
	switch {
	case f.QueryStoreError != "":
		return "kakapo could not read Query Store (" + f.QueryStoreError + "): make sure it is on and has a week of history first, or nothing can show whether the change helped or hurt."
	case f.QueryStoreState != "READ_WRITE" && f.QueryStoreState != "READ_ONLY":
		state := strings.ToLower(f.QueryStoreState)
		if state == "" {
			state = "off"
		}
		return fmt.Sprintf("Query Store is %s, so nothing records how queries ran before the change and no report can show whether it helped or hurt. Turn Query Store on (Setup card) and wait %d days first.", state, compatMinQueryStoreDays)
	case f.QueryStoreDays < compatMinQueryStoreDays:
		held := "only " + dayWords(f.QueryStoreDays)
		if f.QueryStoreDays < 1 {
			held = "under a day"
		}
		return fmt.Sprintf("Query Store holds %s of history; %d days gives a working week to compare against. Wait until it does, unless the change cannot wait.", held, compatMinQueryStoreDays)
	}
	return fmt.Sprintf("Query Store holds %s of history: %s.", dayWords(f.QueryStoreDays), after)
}

func dayWords(d float64) string {
	if d < 1 {
		return "less than a day"
	}
	return plural(int64(d), "day", "days")
}

// CompatReadBack is the level after the statement and whether it is the target.
func CompatReadBack(after CompatFacts, target string) (string, bool) {
	v := strconv.Itoa(after.CompatLevel)
	return v, v == target
}
