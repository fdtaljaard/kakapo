package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The settings baseline (docs/phase5w-contract.md, section A): a flat set of items read from the server on
// every read, kept only when something changed, and the difference between two snapshots, which catches a
// change whoever made it. Item keys are flat and stable:
//
//	config/{name}                 value_in_use of sys.configurations
//	config.pending/{name}         value, only when it differs from value_in_use (RECONFIGURE not run)
//	server/{version|cu|edition|start_time|memory_model|trace_flags}
//	db/{name}/present             and db/{name}/{prop}, one per DatabaseProps
//	file/{db}/{logical}/present   and .../growth, .../max_size; db/tempdb/data_files
//	job/{name}/present            and .../enabled, .../date_modified, .../version, .../schedules, .../backup
//	kakapo/change_log             "absent", "present", "present, its trigger missing" or "present, its trigger disabled"
//
// A nil value is "not read" (a closed AUTO_CLOSE database, an older version, a missing right), never a value.

// Baseline areas: each is read in its own TRY; Errors names the ones that could not be read.
const (
	AreaConfig    = "configuration"
	AreaServer    = "server"
	AreaMemory    = "memory"
	AreaTraceFlag = "trace flags"
	AreaDatabases = "databases"
	AreaFiles     = "files"
	AreaJobs      = "jobs"
	AreaKakapo    = "kakapo"
)

// Reasons of a baseline read (baseline_read.reason).
const (
	BaselineFirst        = "first"
	BaselineRead         = "read"
	BaselineBeforeAction = "before-action"
	BaselineAfterAction  = "after-action"
)

// Item keys and props used by the rules.
const (
	ItemMaxServerMemory = "config/max server memory (MB)"
	ItemMinServerMemory = "config/min server memory (MB)"
	ItemMaxDop          = "config/max degree of parallelism"
	ItemCostThreshold   = "config/cost threshold for parallelism"
	ItemVersion         = "server/version"
	ItemCU              = "server/cu"
	ItemEdition         = "server/edition"
	ItemStartTime       = "server/start_time"
	ItemMemoryModel     = "server/memory_model"
	ItemTraceFlags      = "server/trace_flags"
	ItemChangeLog       = "kakapo/change_log"

	propPresent = "present"
	// UnlimitedMemoryMB is max server memory's default: no cap.
	UnlimitedMemoryMB = 2147483647
)

// DatabaseProps are the per-database items, in the order the read returns them, with their words.
var DatabaseProps = []struct{ Key, Words string }{
	{"state", "State"},
	{"user_access", "User access"},
	{"read_only", "Read only"},
	{"recovery_model", "Recovery model"},
	{"compatibility_level", "Compatibility level"},
	{"rcsi", "Read committed snapshot (RCSI)"},
	{"snapshot_isolation", "Snapshot isolation"},
	{"auto_create_stats", "Auto create statistics"},
	{"auto_update_stats", "Auto update statistics"},
	{"auto_update_stats_async", "Auto update statistics async"},
	{"auto_close", "Auto close"},
	{"auto_shrink", "Auto shrink"},
	{"page_verify", "Page verify"},
	{"query_store", "Query Store"},
	{"delayed_durability", "Delayed durability"},
	{"target_recovery_time", "Target recovery time (s)"},
}

var propWords = func() map[string]string {
	m := map[string]string{
		"growth": "growth", "max_size": "maximum size", "data_files": "data files",
		"enabled": "enabled", "date_modified": "last modified", "version": "version", "schedules": "enabled schedules",
		"backup": "takes backups", "query_store_state": "Query Store state",
	}
	for _, p := range DatabaseProps {
		m[p.Key] = p.Words
	}
	return m
}()

// Str is a value for an item.
func Str(s string) *string { return &s }

// BaselineFacts is one read of the baseline: the items, the areas that could not be read, and SQL Server's
// memory in use and target, which are kept on the read and not in the snapshot (they change all the time).
type BaselineFacts struct {
	Items          map[string]*string
	Errors         map[string]string
	SQLMemoryMB    *int64
	TargetMemoryMB *int64
}

// Hash is the hash of the sorted items and the areas not read: two reads with the same hash are the same
// snapshot.
func (f BaselineFacts) Hash() string {
	keys := make([]string, 0, len(f.Items))
	for k := range f.Items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		v := "\x01null"
		if p := f.Items[k]; p != nil {
			v = *p
		}
		fmt.Fprintf(h, "%s\x00%s\n", k, v)
	}
	areas := make([]string, 0, len(f.Errors))
	for a := range f.Errors {
		areas = append(areas, a)
	}
	sort.Strings(areas)
	fmt.Fprintf(h, "errors\x00%s\n", strings.Join(areas, ","))
	return hex.EncodeToString(h.Sum(nil))
}

// Baseline is a stored snapshot.
type Baseline struct {
	ID       int64
	ServerID int64
	TakenAt  time.Time
	Hash     string
	Items    map[string]*string
	Errors   map[string]string
}

// BaselineReadRow is one read of a server's baseline: which snapshot was in force then.
type BaselineReadRow struct {
	ServerID       int64
	ReadAt         time.Time
	BaselineID     int64
	Reason         string
	ActionID       string
	SQLMemoryMB    *int64
	TargetMemoryMB *int64
}

// Setting change kinds.
const (
	SettingChanged = "changed"
	SettingAdded   = "added"
	SettingRemoved = "removed"
	SettingRestart = "restart"
	SettingVersion = "version"
	SettingPending = "pending"
)

// SettingChange is one difference between two snapshots: the item, from and to, and the bracket: the last
// read that saw the old value (Before) and the first that saw the new one (After). For a database, job or
// file added or removed, Props are that group's items (from the snapshot that has it).
type SettingChange struct {
	Item   string            `json:"item"`
	Kind   string            `json:"kind"`
	From   string            `json:"from,omitempty"`
	To     string            `json:"to,omitempty"`
	Before time.Time         `json:"before"`
	After  time.Time         `json:"after"`
	Props  map[string]string `json:"props,omitempty"`
	// BeforeAction and AfterAction are the actions whose before and after snapshots the two reads were.
	BeforeAction string `json:"-"`
	AfterAction  string `json:"-"`
}

// itemParts splits a key: its kind (config, server, db, file, job, ...), its group ("db/Sales" for
// db/Sales/recovery_model; the key itself for config and server items) and its prop ("recovery_model").
// Names may contain "/": a prop never does, so the prop is the last part.
func itemParts(key string) (kind, group, prop string) {
	i := strings.Index(key, "/")
	if i < 0 {
		return key, key, ""
	}
	kind = key[:i]
	switch kind {
	case "db", "file", "job":
		j := strings.LastIndex(key, "/")
		if j <= i {
			return kind, key, ""
		}
		return kind, key[:j], key[j+1:]
	}
	return kind, key, ""
}

// groupName is the name inside a group: "Sales" for db/Sales, "Sales/Sales_log" for file/Sales/Sales_log.
func groupName(group string) string {
	if i := strings.Index(group, "/"); i >= 0 {
		return group[i+1:]
	}
	return group
}

var groupArea = map[string]string{"db": AreaDatabases, "file": AreaFiles, "job": AreaJobs}

// groups are the db, file and job groups of a snapshot (their present items).
func groups(items map[string]*string) map[string]bool {
	out := map[string]bool{}
	for k, v := range items {
		if kind, g, p := itemParts(k); p == propPresent && v != nil && groupArea[kind] != "" {
			out[g] = true
		}
	}
	return out
}

func groupProps(items map[string]*string, group string) map[string]string {
	out := map[string]string{}
	for k, v := range items {
		if _, g, p := itemParts(k); g == group && p != "" && p != propPresent && v != nil {
			out[p] = *v
		}
	}
	return out
}

// DiffBaselines is the difference between two snapshots of one server (decision A4), before and after the
// times of the reads that bracket it. NULL on either side is not a change; a database, job or file that
// appears is added and one that goes is removed (only when its area was read both times), and its items are
// not listed one by one; a file of a database added or removed is not listed; a new start time is a restart;
// a new version or CU is one version change; a pending value is said as such. The changes come sorted by item.
func DiffBaselines(prev, next Baseline, before, after time.Time) []SettingChange {
	out := []SettingChange{}
	read := func(b Baseline, area string) bool { _, bad := b.Errors[area]; return !bad }
	pg, ng := groups(prev.Items), groups(next.Items)
	skip := map[string]bool{} // groups added or removed: their items are not listed
	parentGone := func(group string) bool {
		// file/{db}/{logical}: a file of a database added or removed.
		if !strings.HasPrefix(group, "file/") {
			return false
		}
		for g := range skip {
			if strings.HasPrefix(g, "db/") && strings.HasPrefix(group, "file/"+groupName(g)+"/") {
				return true
			}
		}
		return false
	}
	gs := []string{}
	for g := range pg {
		gs = append(gs, g)
	}
	for g := range ng {
		if !pg[g] {
			gs = append(gs, g)
		}
	}
	sort.Slice(gs, func(i, j int) bool {
		// databases first, so their files are known to be skipped
		ki, _, _ := itemParts(gs[i] + "/x")
		kj, _, _ := itemParts(gs[j] + "/x")
		if (ki == "db") != (kj == "db") {
			return ki == "db"
		}
		return gs[i] < gs[j]
	})
	for _, g := range gs {
		kind, _, _ := itemParts(g + "/x")
		area := groupArea[kind]
		if pg[g] == ng[g] {
			continue
		}
		skip[g] = true
		if !read(prev, area) || !read(next, area) || parentGone(g) {
			continue
		}
		c := SettingChange{Item: g, Before: before, After: after}
		if ng[g] {
			c.Kind, c.Props = SettingAdded, groupProps(next.Items, g)
		} else {
			c.Kind, c.Props = SettingRemoved, groupProps(prev.Items, g)
		}
		out = append(out, c)
	}

	keys := map[string]bool{}
	for k := range prev.Items {
		keys[k] = true
	}
	for k := range next.Items {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	versionDone := false
	for _, k := range sorted {
		kind, g, p := itemParts(k)
		if p == propPresent || skip[g] || parentGone(g) {
			continue
		}
		pv, pok := prev.Items[k]
		nv, nok := next.Items[k]
		if kind == "config.pending" {
			// A pending value that appears or moves; one that goes is the change itself, dated when
			// value_in_use moves.
			if nok && nv != nil && (!pok || pv == nil || *pv != *nv) && read(prev, AreaConfig) {
				from := ""
				if v := next.Items["config/"+strings.TrimPrefix(k, "config.pending/")]; v != nil {
					from = *v
				}
				out = append(out, SettingChange{Item: k, Kind: SettingPending, From: from, To: *nv, Before: before, After: after})
			}
			continue
		}
		if !pok || !nok || pv == nil || nv == nil || *pv == *nv {
			continue
		}
		c := SettingChange{Item: k, Kind: SettingChanged, From: *pv, To: *nv, Before: before, After: after}
		switch k {
		case ItemStartTime:
			c.Kind = SettingRestart
		case ItemVersion, ItemCU:
			if versionDone {
				continue
			}
			versionDone = true
			c.Item, c.Kind = ItemVersion, SettingVersion
			c.From, c.To = versionText(prev.Items), versionText(next.Items)
		}
		if kind == "db" || kind == "file" || kind == "job" {
			c.Props = groupProps(next.Items, g)
		}
		out = append(out, c)
	}
	return out
}

func versionText(items map[string]*string) string {
	v, cu := "", ""
	if p := items[ItemVersion]; p != nil {
		v = *p
	}
	if p := items[ItemCU]; p != nil {
		cu = *p
	}
	return strings.TrimSpace(v + " " + cu)
}

// valueWords says a value as people say it: max server memory's 2147483647 is "no cap".
func valueWords(item, v string) string {
	if item == ItemMaxServerMemory || item == "config.pending/max server memory (MB)" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= UnlimitedMemoryMB {
			return "no cap"
		}
	}
	return v
}

// Headline is the change in one plain sentence: "max server memory (MB) changed from 10240 to no cap",
// "Database Sales added", "Recovery model of Sales changed from SIMPLE to FULL".
func (c SettingChange) Headline() string {
	kind, g, p := itemParts(c.Item)
	name := groupName(g)
	from, to := valueWords(c.Item, c.From), valueWords(c.Item, c.To)
	switch c.Kind {
	case SettingAdded, SettingRemoved:
		switch kind {
		case "db":
			return "Database " + name + " " + c.Kind
		case "job":
			s := "Agent job " + name + " " + c.Kind
			if c.Props["backup"] == "yes" {
				s += " (it takes backups)"
			}
			return s
		case "file":
			return "File " + fileWords(name) + " " + c.Kind
		}
	case SettingRestart:
		return "SQL Server restarted at " + strings.Replace(c.To, "T", " ", 1) + " (server time)"
	case SettingVersion:
		return "SQL Server version changed from " + from + " to " + to
	case SettingPending:
		return strings.TrimPrefix(c.Item, "config.pending/") + " set to " + to + " but not yet in use (RECONFIGURE not run)"
	}
	switch kind {
	case "config":
		return strings.TrimPrefix(c.Item, "config/") + " changed from " + from + " to " + to
	case "server":
		switch c.Item {
		case ItemMemoryModel:
			return "Memory model changed from " + from + " to " + to + lockPagesWords(to)
		case ItemTraceFlags:
			return "Global trace flags changed from " + from + " to " + to
		case ItemEdition:
			return "SQL Server edition changed from " + from + " to " + to
		}
	case "db":
		if g == "db/tempdb" && p == "data_files" {
			return "TempDB data files changed from " + from + " to " + to
		}
		return upperFirst(propWords[p]) + " of " + name + " changed from " + from + " to " + to
	case "file":
		return upperFirst(propWords[p]) + " of file " + fileWords(name) + " changed from " + from + " to " + to
	case "job":
		return upperFirst(propWords[p]) + " of Agent job " + name + " changed from " + from + " to " + to
	case "kakapo":
		return "kakapo's log on this server changed from " + from + " to " + to
	}
	return c.Item + " changed from " + from + " to " + to
}

func lockPagesWords(model string) string {
	switch model {
	case "LOCK_PAGES":
		return " (Lock Pages in Memory on)"
	case "CONVENTIONAL":
		return " (Lock Pages in Memory off)"
	}
	return ""
}

// fileWords is "Sales_log of Sales" for "Sales/Sales_log".
func fileWords(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:] + " of " + name[:i]
	}
	return name
}

// Database is the database a change is about, "" for a server-level one.
func (c SettingChange) Database() string {
	kind, g, _ := itemParts(c.Item)
	switch kind {
	case "db":
		return groupName(g)
	case "file":
		n := groupName(g)
		if i := strings.LastIndex(n, "/"); i >= 0 {
			return n[:i]
		}
	}
	return ""
}
