package domain

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Did my change help? (docs/phase5h-contract.md): the tuning report follows the changed object itself,
// its own numbers before and after, hour by hour, in one sentence and one small chart. A procedure,
// function or trigger is followed by its object_id (ALTER keeps it, so its history runs across the
// change); a view by the queries whose text names it; an index by the queries whose plan uses it; a
// DEPLOY by each procedure, function and trigger in its statement. Before and Now are the report's own
// windows (tWindows) and the verdict is TuningVerdict, so the object and the per-query table never
// disagree.

// Focus kinds: what the read follows (@focusKind), and the report's kind for a DEPLOY.
const (
	FocusModule = "module"
	FocusView   = "view"
	FocusIndex  = "index"
	FocusDeploy = "deploy"
)

// focusCreatedSlack is how far before the change an object's create_date may be and still count as
// created at the change: the default trace writes the commit row a moment after the object is created,
// and a change recorded by hand carries the minute only.
const focusCreatedSlack = time.Minute

// FocusObject is one object the report follows.
type FocusObject struct {
	Name   string // as the change names it: "dbo.GetOrders", "dbo.T.IX_T_V"
	Type   string // PROCEDURE, FUNCTION, TRIGGER, VIEW or INDEX
	Schema string
	Object string // the module or view; an index's table, "" when the change names none
	Index  string // an index's name
}

// TuningFocusTarget is what the read follows for one change: the kind ("" when the change's type is not
// followed) and the objects, in the order of the read's @names lines.
type TuningFocusTarget struct {
	ChangeID int64
	Kind     string // FocusModule, FocusView, FocusIndex or ""
	Deploy   bool
	Objects  []FocusObject
}

// FocusNames is the read's @names: one object per line, its schema, object and index apart by tabs.
func (t TuningFocusTarget) FocusNames() string {
	lines := make([]string, 0, len(t.Objects))
	for _, o := range t.Objects {
		lines = append(lines, o.Schema+"\t"+o.Object+"\t"+o.Index)
	}
	return strings.Join(lines, "\n")
}

// splitObjectName splits "schema.name" on the dots outside brackets, without the brackets
// ("[dbo].[Get.Orders]" is dbo and Get.Orders; "]]" inside brackets is "]").
func splitObjectName(s string) []string {
	parts := []string{}
	var b strings.Builder
	in := false
	rs := []rune(strings.TrimSpace(s))
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case in && r == ']':
			if i+1 < len(rs) && rs[i+1] == ']' {
				b.WriteRune(']')
				i++
				continue
			}
			in = false
		case !in && r == '[':
			in = true
		case !in && r == '.':
			parts = append(parts, strings.TrimSpace(b.String()))
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	return append(parts, strings.TrimSpace(b.String()))
}

// ParseFocusObject reads a change's object for its type: "schema.name" for a procedure, function,
// trigger or view (a bare name is in dbo; a database in front is dropped); "schema.table.index" for an
// index ("table.index" is in dbo; a bare name is an index without its table). false for a type that is
// not followed or a name that cannot be one.
func ParseFocusObject(name, changeType string) (FocusObject, bool) {
	o := FocusObject{Name: strings.TrimSpace(name), Type: strings.ToUpper(strings.TrimSpace(changeType))}
	if o.Name == "" || strings.ContainsAny(o.Name, "\t\r\n") {
		return o, false
	}
	p := splitObjectName(o.Name)
	for _, x := range p {
		if x == "" {
			return o, false
		}
	}
	switch o.Type {
	case "PROCEDURE", changeTypeFunction, changeTypeTrigger, "VIEW":
		switch len(p) {
		case 1:
			o.Schema, o.Object = "dbo", p[0]
		case 2, 3:
			o.Schema, o.Object = p[len(p)-2], p[len(p)-1]
		default:
			return o, false
		}
	case "INDEX":
		switch len(p) {
		case 1:
			o.Schema, o.Index = "dbo", p[0]
		case 2:
			o.Schema, o.Object, o.Index = "dbo", p[0], p[1]
		case 3:
			o.Schema, o.Object, o.Index = p[0], p[1], p[2]
		default:
			return o, false
		}
	default:
		return o, false
	}
	return o, true
}

// deployTypes are the words of 5f's SchemaChangeFact.line() that a DEPLOY's objects are followed for.
var deployTypes = map[string]string{"Procedure": "PROCEDURE", "Function": changeTypeFunction, "Trigger": changeTypeTrigger}

// ParseDeployStatement reads a DEPLOY's statement, one object per line as 5f writes it ("Procedure
// dbo.GetOrders altered"): every procedure, function and trigger, once each, in order; dropped ones,
// views, tables and indexes left out. No cap.
func ParseDeployStatement(statement string) []FocusObject {
	out := []FocusObject{}
	seen := map[string]bool{}
	for _, line := range strings.Split(statement, "\n") {
		line = strings.TrimSpace(line)
		first, last := strings.Index(line, " "), strings.LastIndex(line, " ")
		if first < 0 || last <= first {
			continue
		}
		t, ok := deployTypes[line[:first]]
		event := line[last+1:]
		if !ok || event == "dropped" || (event != "created" && event != "altered") {
			continue
		}
		o, ok := ParseFocusObject(line[first+1:last], t)
		if !ok || o.Name == "?" {
			continue
		}
		key := strings.ToLower(o.Schema + "." + o.Object)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, o)
	}
	return out
}

// TuningFocusFor is what the read follows for a change: a procedure, function or trigger by its
// object_id, a view by the queries that name it, an index by the queries whose plan uses it, a DEPLOY
// by each of its procedures, functions and triggers. Any other type (SETTING, STATISTICS, PLAN, TABLE,
// ...) is not followed (Kind "").
func TuningFocusFor(c Change) TuningFocusTarget {
	t := TuningFocusTarget{ChangeID: c.ID}
	switch typ := strings.ToUpper(strings.TrimSpace(c.ChangeType)); typ {
	case "PROCEDURE", changeTypeFunction, changeTypeTrigger, "VIEW", "INDEX":
		o, ok := ParseFocusObject(c.Object, typ)
		if !ok {
			return t
		}
		t.Objects = []FocusObject{o}
		t.Kind = map[string]string{"VIEW": FocusView, "INDEX": FocusIndex}[typ]
		if t.Kind == "" {
			t.Kind = FocusModule
		}
	case ChangeTypeDeploy:
		t.Objects = ParseDeployStatement(c.Statement)
		if len(t.Objects) > 0 {
			t.Kind, t.Deploy = FocusModule, true
		}
	}
	return t
}

// TuningCentreChange is the change the report centres on, as BuildTuningReport chooses it: the given id,
// none for TuningChangeNone, else the latest change in the list that can affect performance (newest by
// time, then by id). nil when there is none.
func TuningCentreChange(changes []Change, changeID int64) *Change {
	if changeID == TuningChangeNone {
		return nil
	}
	sorted := append([]Change(nil), changes...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].ChangedAt.Equal(sorted[j].ChangedAt) {
			return sorted[i].ChangedAt.After(sorted[j].ChangedAt)
		}
		return sorted[i].ID > sorted[j].ID
	})
	for i := range sorted {
		if (changeID > 0 && sorted[i].ID == changeID) || (changeID == 0 && IsPerformanceChange(sorted[i])) {
			return &sorted[i]
		}
	}
	return nil
}

// TuningFocusItemFact is one followed object as the read found it. Times are the server's clock.
type TuningFocusItemFact struct {
	N           int   // the line of @names, from 1
	ObjectID    int64 // 0 when not found; for an index, its table
	Created     *time.Time
	Modified    *time.Time
	QueryHash   string // the costliest matched statement in the window, for Investigate
	Table       string // an index's table as found, "schema.table" (by the index's name alone when the change names none)
	IndexFound  bool
	UsageRow    bool // sys.dm_db_index_usage_stats holds a row for the index
	Seeks       int64
	Scans       int64
	Lookups     int64
	Updates     int64
	LastUse     *time.Time
	ServerStart *time.Time // sqlserver_start_time
}

// TuningFocusHourFact is one followed object's CPU and runs in one hour. A module's row is its statements
// together, with runs the executions of its most-run statement; a view's or an index's rows are per
// matched query (QueryHash).
type TuningFocusHourFact struct {
	N         int
	QueryHash string
	Hour      time.Time
	CPU       int64 // microseconds
	Runs      int64
}

// TuningFocusQueryFact is one query that names a followed view or uses a followed index.
type TuningFocusQueryFact struct {
	N          int
	QueryHash  string
	ObjectName string
	Text       string
}

// TuningFocus is "Did it help {object}?": the object's own numbers before and now, its verdict, its hours
// for the chart, and for a DEPLOY one row per object (for a view or an index, one row per query).
type TuningFocus struct {
	ChangeID  int64              `json:"changeId"`
	Kind      string             `json:"kind"` // module, view, index, deploy
	Type      string             `json:"type"` // PROCEDURE, FUNCTION, TRIGGER, VIEW, INDEX, DEPLOY
	Object    string             `json:"object"`
	Found     bool               `json:"found"`
	Verdict   string             `json:"verdict"` // "" when there is nothing to judge
	Sentence  string             `json:"sentence"`
	Before    TuningFocusNumbers `json:"before"`
	Now       TuningFocusNumbers `json:"now"`
	Hours     []TuningHour       `json:"hours"` // the object's own: CPU per run and runs per hour
	Index     *TuningIndexUse    `json:"index,omitempty"`
	QueryHash string             `json:"queryHash"` // Investigate: the costliest statement
	Rows      []TuningFocusRow   `json:"rows"`
	Notes     []string           `json:"notes"`
	Copy      string             `json:"copy"`
}

// TuningFocusNumbers are one window's numbers of the followed object, from sums.
type TuningFocusNumbers struct {
	Runs         int64    `json:"runs"`
	Hours        int      `json:"hours"`
	CPUPerRunMs  *float64 `json:"cpuPerRunMs"`
	RunsPerHour  *float64 `json:"runsPerHour"`
	CPUPerHourMs *float64 `json:"cpuPerHourMs"`
}

// TuningIndexUse is what SQL Server counted for a followed index since it last started.
type TuningIndexUse struct {
	Table         string `json:"table"`
	Index         string `json:"index"`
	Used          bool   `json:"used"`
	Seeks         int64  `json:"seeks"`
	Scans         int64  `json:"scans"`
	Lookups       int64  `json:"lookups"`
	Updates       int64  `json:"updates"`
	LastUse       string `json:"lastUse"`
	Since         string `json:"since"` // the later of the server's start and the change
	ServerStarted string `json:"serverStarted"`
}

// TuningFocusRow is one object of a DEPLOY, or one query of a followed view or index.
type TuningFocusRow struct {
	Name              string   `json:"name"`
	QueryHash         string   `json:"queryHash"`
	Text              string   `json:"text"`
	Found             bool     `json:"found"`
	Note              string   `json:"note"` // "not found", "dropped and created again on Tue 3 Nov 14:20"
	RunsBefore        int64    `json:"runsBefore"`
	RunsNow           int64    `json:"runsNow"`
	CPUBeforeMs       *float64 `json:"cpuBeforeMs"`
	CPUNowMs          *float64 `json:"cpuNowMs"`
	RunsPerHourBefore float64  `json:"runsPerHourBefore"`
	RunsPerHourNow    float64  `json:"runsPerHourNow"`
	Verdict           string   `json:"verdict"`
	ImpactMsPerHour   float64  `json:"impactMsPerHour"` // CPU added (+) or saved (-) per business hour
}

// fHour is one hour of a followed object.
type fHour struct{ cpu, runs int64 }

// fSum is a followed object's sums over one window.
type fSum struct {
	hours      int
	runs, cpu  int64
	cpuPerRun  float64
	hasPerRun  bool
	runsPerHr  float64
	cpuPerHrMs float64
}

func fSumOf(byHour map[time.Time]fHour, hours []tHour, idx []int) fSum {
	s := fSum{hours: len(idx)}
	for _, i := range idx {
		h := byHour[hours[i].at]
		s.runs += h.runs
		s.cpu += h.cpu
	}
	if s.runs > 0 {
		s.cpuPerRun, s.hasPerRun = float64(s.cpu)/float64(s.runs)/1000, true
	}
	if s.hours > 0 {
		s.runsPerHr = float64(s.runs) / float64(s.hours)
		s.cpuPerHrMs = float64(s.cpu) / 1000 / float64(s.hours)
	}
	return s
}

func (s fSum) numbers() TuningFocusNumbers {
	n := TuningFocusNumbers{Runs: s.runs, Hours: s.hours}
	if s.hasPerRun {
		v := roundHalfAway(s.cpuPerRun, 2)
		n.CPUPerRunMs = &v
	}
	if s.hours > 0 {
		r, c := roundHalfAway(s.runsPerHr, 1), roundHalfAway(s.cpuPerHrMs, 1)
		n.RunsPerHour, n.CPUPerHourMs = &r, &c
	}
	return n
}

// fVerdict is TuningVerdict on the object's CPU per run, and the CPU it added (+) or saved (-) per
// business hour, as tQueries reckons it.
func fVerdict(b, n fSum) (string, float64) {
	v := TuningVerdict(b.runs, n.runs, b.cpuPerRun, n.cpuPerRun)
	impact := 0.0
	switch v {
	case VerdictFaster, VerdictSlower, VerdictSame:
		impact = (n.cpuPerRun - b.cpuPerRun) * float64(n.runs) / float64(n.hours)
	case VerdictNew:
		impact = float64(n.cpu) / 1000 / float64(n.hours)
	case VerdictGone:
		impact = -float64(b.cpu) / 1000 / float64(b.hours)
	}
	return v, roundHalfAway(impact, 1)
}

// focusInput is what tBuildFocus works from.
type focusInput struct {
	f        TuningFacts
	r        *TuningReport
	centre   *tChange
	hours    []tHour
	before   []int
	now      []int
	tooEarly bool // 5c prints "too early to say": the comparison sentences are skipped
}

// tBuildFocus builds "Did it help {object}?" when the read followed the centred change; nil otherwise
// (a type that is not followed, another change, or a read that failed: the report stays as 5c's).
func tBuildFocus(in focusInput) *TuningFocus {
	f, t, c := in.f, in.f.Focus, in.centre
	if t.Kind == "" || c == nil || c.c.ID != t.ChangeID || len(t.Objects) == 0 ||
		f.FocusError != "" || f.AboutError != "" || f.HoursError != "" || f.ShapesError != "" {
		return nil
	}
	items := map[int]TuningFocusItemFact{}
	for _, it := range f.FocusItems {
		items[it.N] = it
	}
	byHour := map[int]map[time.Time]fHour{}
	byQuery := map[int]map[string]map[time.Time]fHour{}
	for _, h := range f.FocusHours {
		at := naiveUTC(h.Hour).Truncate(time.Hour)
		if byHour[h.N] == nil {
			byHour[h.N] = map[time.Time]fHour{}
		}
		x := byHour[h.N][at]
		x.cpu += h.CPU
		x.runs += h.Runs
		byHour[h.N][at] = x
		if h.QueryHash != "" {
			key := strings.ToUpper(h.QueryHash)
			if byQuery[h.N] == nil {
				byQuery[h.N] = map[string]map[time.Time]fHour{}
			}
			if byQuery[h.N][key] == nil {
				byQuery[h.N][key] = map[time.Time]fHour{}
			}
			q := byQuery[h.N][key][at]
			q.cpu += h.CPU
			q.runs += h.Runs
			byQuery[h.N][key][at] = q
		}
	}

	fo := &TuningFocus{ChangeID: c.c.ID, Kind: t.Kind, Type: strings.ToUpper(c.c.ChangeType), Object: strings.TrimSpace(c.c.Object),
		Hours: []TuningHour{}, Rows: []TuningFocusRow{}, Notes: []string{}}
	if t.Deploy {
		fo.Kind = FocusDeploy
		in.deploy(fo, items, byHour)
		return fo
	}

	o := t.Objects[0]
	it := items[1]
	b, n := fSumOf(byHour[1], in.hours, in.before), fSumOf(byHour[1], in.hours, in.now)
	fo.Before, fo.Now = b.numbers(), n.numbers()
	fo.QueryHash = it.QueryHash
	fo.Hours = in.focusHours(byHour[1])
	switch t.Kind {
	case FocusModule, FocusView:
		fo.Found = it.ObjectID != 0
	case FocusIndex:
		fo.Found = it.IndexFound
	}
	if t.Kind != FocusModule {
		fo.Rows = in.queryRows(byQuery[1], f.FocusQueries)
	}
	if t.Kind == FocusIndex {
		if o.Object == "" {
			fo.Notes = append(fo.Notes, fmt.Sprintf("The change names no table, so kakapo found index %s by its name alone (on %s) and matched the plans on the index's name.",
				o.Index, orQ(it.Table)))
		}
		fo.Notes = append(fo.Notes, "A query that reaches the table only through a view or a nested call is not followed.")
		if fo.Found {
			fo.Index = in.indexUse(o, it)
		}
	}
	if t.Kind == FocusView {
		fo.Notes = append(fo.Notes, "The queries that name the view in their text are followed; a query that reaches it through another view is not.")
	}
	if fo.Found && !(t.Kind == FocusModule && in.recreated(it)) {
		fo.Verdict, _ = fVerdict(b, n)
	}
	fo.Sentence = in.sentence(fo, o, it, b, n, in.ranBefore(byHour[1]))
	fo.Copy = in.focusCopy(fo)
	return fo
}

// ranBefore says whether the object ran in any hour of the read before the change's hour: "Query Store
// holds no runs of it before the change" is then a fact that time does not change.
func (in focusInput) ranBefore(byHour map[time.Time]fHour) bool {
	cH := in.centre.at.Truncate(time.Hour)
	for at, h := range byHour {
		if at.Before(cH) && h.runs > 0 {
			return true
		}
	}
	return false
}

// recreated says whether a module was created at or after the change: dropped and created, so its
// object_id is new and Query Store holds nothing of it before.
func (in focusInput) recreated(it TuningFocusItemFact) bool {
	return it.Created != nil && !it.Created.Before(in.centre.at.Add(-focusCreatedSlack))
}

// focusHours are the object's own hours for the chart: the report's hours (classes, windows, change
// hours), with the object's CPU per run and runs.
func (in focusInput) focusHours(byHour map[time.Time]fHour) []TuningHour {
	out := make([]TuningHour, 0, len(in.r.Hours))
	for i, rh := range in.r.Hours {
		x := byHour[in.hours[i].at]
		h := TuningHour{Hour: rh.Hour, Class: rh.Class, Complete: rh.Complete, InProgress: rh.InProgress, Change: rh.Change,
			NoData: x.runs == 0 && x.cpu == 0, Window: rh.Window, Executions: x.runs, CoresBusy: roundHalfAway(CoresBusy(x.cpu, time.Hour), 3)}
		if x.runs > 0 {
			h.CPUPerExecMs = roundHalfAway(float64(x.cpu)/float64(x.runs)/1000, 3)
		}
		out = append(out, h)
	}
	return out
}

// focusRow is one row of a DEPLOY's or a view's or index's table, from its hours.
func (in focusInput) focusRow(row TuningFocusRow, byHour map[time.Time]fHour) TuningFocusRow {
	b, n := fSumOf(byHour, in.hours, in.before), fSumOf(byHour, in.hours, in.now)
	row.RunsBefore, row.RunsNow = b.runs, n.runs
	bn, nn := b.numbers(), n.numbers()
	row.CPUBeforeMs, row.CPUNowMs = bn.CPUPerRunMs, nn.CPUPerRunMs
	row.RunsPerHourBefore, row.RunsPerHourNow = roundHalfAway(b.runsPerHr, 1), roundHalfAway(n.runsPerHr, 1)
	row.Verdict, row.ImpactMsPerHour = fVerdict(b, n)
	return row
}

func sortFocusRows(rows []TuningFocusRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		ra, rb := a.Verdict == VerdictTooFew || a.Verdict == "", b.Verdict == VerdictTooFew || b.Verdict == ""
		if ra != rb {
			return rb
		}
		if math.Abs(a.ImpactMsPerHour) != math.Abs(b.ImpactMsPerHour) {
			return math.Abs(a.ImpactMsPerHour) > math.Abs(b.ImpactMsPerHour)
		}
		return a.Name < b.Name
	})
}

// queryRows are the queries that name a view or use an index, each before and now with its verdict.
func (in focusInput) queryRows(byQuery map[string]map[time.Time]fHour, queries []TuningFocusQueryFact) []TuningFocusRow {
	out := []TuningFocusRow{}
	for _, q := range queries {
		if q.N != 1 {
			continue
		}
		row := TuningFocusRow{Name: tQueryName(q.ObjectName, q.Text), QueryHash: q.QueryHash, Text: q.Text, Found: true}
		out = append(out, in.focusRow(row, byQuery[strings.ToUpper(q.QueryHash)]))
	}
	sortFocusRows(out)
	return out
}

// deploy fills a DEPLOY's focus: one row per procedure, function and trigger, sorted by the CPU added or
// saved per business hour, and the sentence that names the faster and slower ones.
func (in focusInput) deploy(fo *TuningFocus, items map[int]TuningFocusItemFact, byHour map[int]map[time.Time]fHour) {
	for i, o := range in.f.Focus.Objects {
		it := items[i+1]
		row := TuningFocusRow{Name: o.Schema + "." + o.Object, QueryHash: it.QueryHash, Found: it.ObjectID != 0}
		switch {
		case !row.Found:
			row.Note = "not found"
		case in.recreated(it):
			row.Note = "dropped and created again on " + tWhen(*it.Created)
			b, n := fSumOf(byHour[i+1], in.hours, in.before), fSumOf(byHour[i+1], in.hours, in.now)
			row.RunsBefore, row.RunsNow = b.runs, n.runs
			row.CPUNowMs = n.numbers().CPUPerRunMs
			row.RunsPerHourNow = roundHalfAway(n.runsPerHr, 1)
		default:
			row = in.focusRow(row, byHour[i+1])
		}
		if row.Found {
			fo.Found = true
		}
		fo.Rows = append(fo.Rows, row)
	}
	sortFocusRows(fo.Rows)
	if !in.tooEarly {
		fo.Sentence = in.deploySentence(fo)
	}
	fo.Copy = in.focusCopy(fo)
}

// deploySentence: "Of the 40 objects in the deploy by DOMAIN\build on Tue 3 Nov 14:20, 3 got slower and 2
// got faster; the rest ran too little to say. Slower: dbo.GetOrders, 18 ms per run instead of 12.4 ms."
func (in focusInput) deploySentence(fo *TuningFocus) string {
	var slower, faster []TuningFocusRow
	same := 0
	for _, r := range fo.Rows {
		switch r.Verdict {
		case VerdictSlower:
			slower = append(slower, r)
		case VerdictFaster:
			faster = append(faster, r)
		case VerdictSame:
			same++
		}
	}
	objects := fo.Object
	if !strings.HasSuffix(objects, "objects") && !strings.HasSuffix(objects, "object") {
		objects = plural(int64(len(fo.Rows)), "object", "objects")
	}
	s := fmt.Sprintf("Of the %s in the deploy%s on %s, %d got slower and %d got faster", objects, deployBy(in.centre.c.Description),
		tWhen(in.centre.at), len(slower), len(faster))
	rest := len(fo.Rows) - len(slower) - len(faster) - same
	switch {
	case same > 0 && rest > 0:
		s += fmt.Sprintf(", %d stayed about the same and the rest ran too little to say.", same)
	case same > 0:
		s += fmt.Sprintf(" and %d stayed about the same.", same)
	default:
		s += "; the rest ran too little to say."
	}
	name := func(label string, rows []TuningFocusRow) string {
		parts := []string{}
		for i, r := range rows {
			if i >= 3 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s, %s per run instead of %s", r.Name, tMsPtr(r.CPUNowMs), tMsPtr(r.CPUBeforeMs)))
		}
		if len(parts) == 0 {
			return ""
		}
		return " " + label + ": " + strings.Join(parts, "; ") + "."
	}
	return s + name("Slower", slower) + name("Faster", faster)
}

// deployBy is " by DOMAIN\build" from 5f's DEPLOY description ("Deploy by DOMAIN\build on BUILD01 (DacFx):
// ..."); "" when it is not one.
func deployBy(description string) string {
	rest, ok := strings.CutPrefix(strings.TrimSpace(description), "Deploy by ")
	if !ok {
		return ""
	}
	for _, sep := range []string{" on ", " (", ":"} {
		if i := strings.Index(rest, sep); i >= 0 {
			rest = rest[:i]
		}
	}
	if rest = strings.TrimSpace(rest); rest == "" || rest == "?" {
		return ""
	}
	return " by " + rest
}

// indexUse is the index's use since SQL Server last started: seeks, scans and lookups are uses, updates
// the cost of keeping it.
func (in focusInput) indexUse(o FocusObject, it TuningFocusItemFact) *TuningIndexUse {
	u := &TuningIndexUse{Table: it.Table, Index: o.Index, Seeks: it.Seeks, Scans: it.Scans, Lookups: it.Lookups, Updates: it.Updates,
		Used: it.UsageRow && it.Seeks+it.Scans+it.Lookups > 0}
	if it.LastUse != nil {
		u.LastUse = tStamp(*it.LastUse)
	}
	since := in.centre.at
	if it.ServerStart != nil {
		u.ServerStarted = tStamp(*it.ServerStart)
		if it.ServerStart.After(since) {
			since = *it.ServerStart
		}
	}
	u.Since = tStamp(since)
	return u
}

// fKindWord is the object's kind in a sentence: "procedure", "function", "trigger", "view", "index".
func fKindWord(typ string) string { return strings.ToLower(typ) }

// fVerb is what the change did to the object: "altered" or "created" when the change log says so (the
// default trace's rows, kakapo's own index fixes), else "changed".
func fVerb(c Change) string {
	if c.Result == ChangeRecordedByHand {
		return "changed"
	}
	d := strings.ToLower(c.Description)
	switch {
	case strings.Contains(d, " altered"):
		return "altered"
	case strings.HasPrefix(d, "created ") || strings.Contains(d, " created"):
		return "created"
	}
	return "changed"
}

// fRunsClause: "it runs about as often (3,150 runs an hour against 3,200)", with "more often" or "less
// often" from a 10% change (5c's tExecsClause rule); changed says which.
func fRunsClause(subject string, many bool, b, n fSum) (string, bool) {
	verb := "runs"
	if many {
		verb = "run"
	}
	nums := fmt.Sprintf("(%s runs an hour against %s)", n0(n.runsPerHr), n0(b.runsPerHr))
	tone := ToneSame
	if b.runsPerHr > 0 {
		tone = tTone((n.runsPerHr-b.runsPerHr)/b.runsPerHr*100, false)
	}
	switch tone {
	case ToneMore:
		return subject + " " + verb + " more often " + nums, true
	case ToneFewer:
		return subject + " " + verb + " less often " + nums, true
	}
	return subject + " " + verb + " about as often " + nums, false
}

// fPctWords: "31% less", "45% more".
func fPctWords(before, now float64) string {
	p := (now - before) / before * 100
	if p < 0 {
		return tPct(p) + " less"
	}
	return tPct(p) + " more"
}

// sentence is the object's sentence (docs/phase5h-contract.md, section 3), only one of them; "" when 5c's
// "too early to say" stands for it.
func (in focusInput) sentence(fo *TuningFocus, o FocusObject, it TuningFocusItemFact, b, n fSum, ranBefore bool) string {
	c := in.centre
	days := plural(int64(in.f.Days), "day", "days")
	word := fKindWord(o.Type)
	if !fo.Found {
		name := fo.Object
		if o.Type == "INDEX" && o.Object != "" {
			name = "index " + o.Index + " on " + o.Schema + "." + o.Object
		}
		return fmt.Sprintf("kakapo cannot find %s in %s, so the report compares the whole database.", name, in.f.Database)
	}
	if o.Type != "INDEX" && o.Type != "VIEW" && in.recreated(it) {
		return fmt.Sprintf("%s was dropped and created again on %s, so Query Store sees a new object and holds nothing before it. ALTER (or CREATE OR ALTER) keeps an object's history.",
			fo.Object, tWhen(*it.Created))
	}

	// The subject: the module itself, or the queries that name the view or use the index.
	subject, many, intro := fo.Object, false, ""
	queries := len(fo.Rows)
	usage := ""
	switch o.Type {
	case "INDEX":
		u := fo.Index
		table := u.Table
		if !u.Used {
			return fmt.Sprintf("Index %s on %s has not been used since %s; SQL Server still updates it on every write to %s (%s). The counters start again when SQL Server restarts or the database is restored or taken offline (SQL Server last started %s).",
				o.Index, table, tWhenStamp(u.Since), table, plural(u.Updates, "update", "updates"), tWhenStamp(u.ServerStarted))
		}
		usage = fmt.Sprintf("It was used %s (%s); SQL Server also updates it on every write to %s (%s so far).",
			plural(u.Seeks+u.Scans+u.Lookups, "time", "times"), indexUses(u), table, plural(u.Updates, "update", "updates"))
		if in.tooEarly {
			return fmt.Sprintf("Index %s on %s was used %s (%s) since %s; SQL Server also updates it on every write to %s (%s so far).",
				o.Index, table, plural(u.Seeks+u.Scans+u.Lookups, "time", "times"), indexUses(u), tWhenStamp(u.Since), table, plural(u.Updates, "update", "updates"))
		}
		if queries == 0 {
			return fmt.Sprintf("Query Store holds no query in the last %s whose plan uses index %s. %s", days, o.Index, usage)
		}
		subject, many = "the "+plural(int64(queries), "query that uses it", "queries that use it"), queries != 1
		intro = fmt.Sprintf("Since index %s was %s on %s on %s, %s", o.Index, fVerb(c.c), table, tWhen(c.at), subject)
		usage = " " + usage
	case "VIEW":
		if in.tooEarly {
			return ""
		}
		if queries == 0 {
			return fmt.Sprintf("Query Store holds no query in the last %s that names view %s.", days, fo.Object)
		}
		subject, many = "the "+plural(int64(queries), "query that names it", "queries that name it"), queries != 1
		intro = fmt.Sprintf("Since view %s was %s on %s, %s", fo.Object, fVerb(c.c), tWhen(c.at), subject)
	default:
		if !ranBefore {
			return fmt.Sprintf("Query Store holds no runs of %s before the change in the last %s.", fo.Object, days)
		}
		if in.tooEarly {
			return ""
		}
		intro = fmt.Sprintf("Since %s %s was %s on %s, it", word, fo.Object, fVerb(c.c), tWhen(c.at))
	}
	pron, have, use := "it", "has", "uses"
	if many {
		pron, have, use = "they", "have", "use"
	}
	if o.Type == "INDEX" {
		use = "take"
	}

	switch {
	case !ranBefore:
		return fmt.Sprintf("Query Store holds no runs of %s before the change in the last %s.", subject, days) + usage
	case b.runs == 0:
		return fmt.Sprintf("%s ran before the change, but not in the business hours compared before it (%s); kakapo needs %d runs there to compare.",
			tCap(subject), in.r.Before.Label, TuningMinRuns) + usage
	case n.runs == 0 && b.runs >= TuningMinRuns:
		return tCap(subject) + " " + have + " not run since the change." + usage
	case n.runs == 0:
		return fmt.Sprintf("%s %s not run since the change, and ran only %s before it; kakapo needs %d to compare.", tCap(subject), have,
			plural(b.runs, "time", "times"), TuningMinRuns) + usage
	}
	switch fo.Verdict {
	case VerdictTooFew:
		if n.runs < TuningMinRuns {
			return fmt.Sprintf("%s ran only %s since the change; kakapo needs %d to compare.", tCap(subject), plural(n.runs, "time", "times"), TuningMinRuns) + usage
		}
		return fmt.Sprintf("%s ran only %s in the business hours before the change; kakapo needs %d to compare.", tCap(subject),
			plural(b.runs, "time", "times"), TuningMinRuns) + usage
	case VerdictSame:
		runs, changed := fRunsClause(pron, many, b, n)
		s := fmt.Sprintf("%s %s about the same CPU per run as before (%s against %s)", intro, use, tMs(n.cpuPerRun), tMs(b.cpuPerRun))
		if changed {
			return s + ", but " + runs + ", so " + map[bool]string{false: "its", true: "their"}[many] + " load changed even if each run did not." + usage
		}
		return s + ": the change made no difference that Query Store can see." + usage
	case VerdictFaster, VerdictSlower:
		runs, _ := fRunsClause(pron, many, b, n)
		obj := map[bool]string{false: "it", true: "them"}[many]
		end := ": the change made " + obj + " cheaper."
		if fo.Verdict == VerdictSlower {
			end = ": Investigate " + obj + "."
		}
		return fmt.Sprintf("%s %s %s of CPU per run instead of %s (%s) over %s, and %s%s", intro, use, tMs(n.cpuPerRun), tMs(b.cpuPerRun),
			fPctWords(b.cpuPerRun, n.cpuPerRun), plural(int64(n.hours), "business hour", "business hours"), runs, end) + usage
	}
	return ""
}

// indexUses: "60 seeks", "50 seeks, 10 scans".
func indexUses(u *TuningIndexUse) string {
	parts := []string{}
	for _, x := range []struct {
		n         int64
		one, many string
	}{{u.Seeks, "seek", "seeks"}, {u.Scans, "scan", "scans"}, {u.Lookups, "lookup", "lookups"}} {
		if x.n > 0 {
			parts = append(parts, plural(x.n, x.one, x.many))
		}
	}
	return strings.Join(parts, ", ")
}

// focusCopy is Copy for the vendor of the followed object: the object, the sentence and its numbers.
func (in focusInput) focusCopy(fo *TuningFocus) string {
	var b strings.Builder
	b.WriteString(fo.Object + " in " + in.f.Database + "\n")
	if fo.QueryHash != "" && fo.Kind != FocusDeploy {
		b.WriteString("Query hash of its costliest statement: " + fo.QueryHash + "\n")
	}
	b.WriteString("\n" + fFocusBlock(*fo))
	return strings.TrimRight(b.String(), "\n")
}

// fFocusBlock is the object's block of Copy for IT: the question, the sentence, the three numbers before
// and now, and the rows.
func fFocusBlock(fo TuningFocus) string {
	var b strings.Builder
	b.WriteString(fFocusTitle(fo) + "\n")
	if fo.Sentence != "" {
		b.WriteString("- " + fo.Sentence + "\n")
	}
	if fo.Kind != FocusDeploy && fo.Found {
		num := func(v *float64, ms bool) string {
			switch {
			case v == nil:
				return "-"
			case ms:
				return tMs(*v)
			}
			return n0(*v)
		}
		fmt.Fprintf(&b, "- CPU per run: %s -> %s (%s runs -> %s runs)", num(fo.Before.CPUPerRunMs, true), num(fo.Now.CPUPerRunMs, true),
			n0int(fo.Before.Runs), n0int(fo.Now.Runs))
		if fo.Verdict != "" {
			b.WriteString("; " + fo.Verdict)
		}
		b.WriteString("\n")
		fmt.Fprintf(&b, "- Runs per business hour: %s -> %s\n", num(fo.Before.RunsPerHour, false), num(fo.Now.RunsPerHour, false))
		fmt.Fprintf(&b, "- CPU per business hour: %s -> %s\n", num(fo.Before.CPUPerHourMs, true), num(fo.Now.CPUPerHourMs, true))
	}
	if fo.Index != nil {
		fmt.Fprintf(&b, "- Index use since %s: %s seeks, %s scans, %s lookups, %s updates\n", tWhenStamp(fo.Index.Since), n0int(fo.Index.Seeks),
			n0int(fo.Index.Scans), n0int(fo.Index.Lookups), n0int(fo.Index.Updates))
	}
	for _, r := range fo.Rows {
		if r.Note != "" && r.Verdict == "" {
			fmt.Fprintf(&b, "- %s: %s\n", r.Name, r.Note)
			continue
		}
		fmt.Fprintf(&b, "- %s%s: %s -> %s per run; %s -> %s runs; %s\n", r.Name, fHashSuffix(r.QueryHash), tMsPtr(r.CPUBeforeMs), tMsPtr(r.CPUNowMs),
			n0int(r.RunsBefore), n0int(r.RunsNow), r.Verdict)
	}
	for _, n := range fo.Notes {
		b.WriteString("- " + n + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func fHashSuffix(h string) string {
	if h == "" {
		return ""
	}
	return " (" + h + ")"
}

// fFocusTitle is the section's title: "Did it help dbo.GetOrders?".
func fFocusTitle(fo TuningFocus) string {
	if fo.Kind == FocusDeploy {
		return "Did it help? The deploy of " + fo.Object
	}
	return "Did it help " + fo.Object + "?"
}
