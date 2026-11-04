package domain

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Blocking now (docs/phase4c-contract.md, part 2): the chain of sessions that block and are blocked
// across the instance, read on request from the blocking page and polled by its watch. The tree, the
// classification of each head blocker (sp_HumanEventsBlockViewer's rules: sleeping, implicit
// transaction, serializable or repeatable read, compile lock, long-running statement) and the plain
// sentences are built here from what collect.ReadBlocking read.

// BlockingAppName is the program name kakapo's own connections carry (collect.ConnString's "app
// name"); the kill-session fix refuses them.
const BlockingAppName = "SQLMonitor"

// blkLongRunningSec is how long a head blocker's statement has run before it counts as long-running.
const blkLongRunningSec = 30

// Classification of a session in a chain (BlockingNode.Kinds).
const (
	BlockSleeping       = "sleeping"        // status sleeping: holds its locks while doing nothing
	BlockImplicit       = "implicit"        // its transaction was opened by IMPLICIT_TRANSACTIONS
	BlockSerializable   = "serializable"    // SERIALIZABLE isolation: range locks
	BlockRepeatableRead = "repeatable-read" // REPEATABLE READ isolation: shared locks kept to the end
	BlockCompile        = "compile"         // the sessions behind it wait on a compile lock
	BlockLongRunning    = "long-running"    // one statement running 30 s or more
	BlockRollingBack    = "rolling-back"    // killed, rolling its work back
)

// BlockingSession is one session in a blocking chain as read. The times are the server's clock and
// stay out of the JSON; the ages in seconds are what the page shows.
type BlockingSession struct {
	Session         int     `json:"session"`
	BlockedBy       int     `json:"blockedBy"` // 0 when not blocked
	Login           string  `json:"login"`
	Host            string  `json:"host"`
	Program         string  `json:"program"`
	JobName         string  `json:"jobName,omitempty"` // the SQL Agent job, when the program is a job step
	Status          string  `json:"status"`            // running, runnable, suspended, sleeping, background
	IsUser          bool    `json:"isUser"`
	Command         string  `json:"command,omitempty"`
	Database        string  `json:"database"`
	Statement       string  `json:"statement"`
	ObjectName      string  `json:"objectName,omitempty"` // the procedure the statement belongs to
	StatementIsLast bool    `json:"statementIsLast"`      // the last batch it sent, nothing running now
	WaitType        string  `json:"waitType,omitempty"`
	WaitMs          int64   `json:"waitMs"`
	WaitResource    string  `json:"waitResource,omitempty"` // as SQL Server writes it: "KEY: 7:72057594043236352 (8194443284a0)"
	Resource        string  `json:"resource,omitempty"`     // the table (and index) of the lock it waits on: "dbo.MasterItem (PK_MasterItem)"
	ResourceTable   string  `json:"resourceTable,omitempty"`
	LockMode        string  `json:"lockMode,omitempty"` // "wants U, held X"
	Isolation       string  `json:"isolation"`          // "READ COMMITTED"
	OpenTran        int     `json:"openTran"`
	TranName        string  `json:"tranName,omitempty"` // user_transaction, implicit_transaction, ...
	LogBytes        int64   `json:"logBytes"`
	PercentComplete float64 `json:"percentComplete,omitempty"`
	TranSeconds     int64   `json:"tranSeconds"`    // how long its oldest transaction has been open; 0 without one
	RequestSeconds  int64   `json:"requestSeconds"` // how long its current statement has run; 0 when sleeping
	Sleeping        bool    `json:"sleeping"`

	LoginTime      time.Time `json:"-"`
	TranStarted    time.Time `json:"-"`
	RequestStarted time.Time `json:"-"`
	IsolationLevel int       `json:"-"` // sys.dm_exec_sessions.transaction_isolation_level
}

// LockWait is a lock a blocked session waits for, with the mode its blocker holds on the same resource.
type LockWait struct {
	Session      int
	ResourceType string // KEY, PAGE, RID, OBJECT, HOBT, ...
	Database     string
	Object       string // schema.table, "" when it could not be named
	Index        string
	WaitMode     string
	HeldMode     string
}

// BlockingFacts is what collect.ReadBlocking read. Each area that failed has its error; the others
// still count.
type BlockingFacts struct {
	Database       string
	Found          bool // the database exists on the instance
	RCSI           bool
	OwnSession     int // the session that read this (@@SPID)
	ServerName     string
	ProductVersion string
	ServerNow      time.Time
	Sessions       []BlockingSession
	Waits          []LockWait
	SessionsError  string
	JobsError      string
	LocksError     string
	NamesError     string
}

// BlockingNode is a session in the tree: its victims under it, how many wait behind it at every level
// and the longest of those waits, its classification, and whether it may be killed from here.
type BlockingNode struct {
	BlockingSession
	Kinds          []string       `json:"kinds"`
	Behind         int            `json:"behind"`
	LongestWaitSec int64          `json:"longestWaitSec"`
	CanKill        bool           `json:"canKill"`
	KillRefusal    string         `json:"killRefusal,omitempty"`
	Victims        []BlockingNode `json:"victims"`
}

// BlockingTree is the blocking page (GET .../blocking): head blockers at the top, each with its
// victims, each victim with its own; the Explain sentences first, and the chain as text for the vendor.
type BlockingTree struct {
	CapturedAt    time.Time      `json:"capturedAt"`
	ServerName    string         `json:"serverName"`
	Database      string         `json:"database"`
	EngineVersion string         `json:"engineVersion"`
	RCSI          bool           `json:"rcsi"`
	OfferRCSI     bool           `json:"offerRcsi"` // the database has READ COMMITTED SNAPSHOT off: the rcsi fix applies
	Blocked       int            `json:"blocked"`   // sessions waiting, in every chain
	Heads         []BlockingNode `json:"heads"`
	Explain       []string       `json:"explain"`
	Notes         []string       `json:"notes"` // what could not be read
	Handover      string         `json:"handover"`
}

// IsolationName is the words for sys.dm_exec_sessions.transaction_isolation_level.
func IsolationName(level int) string {
	switch level {
	case 1:
		return "READ UNCOMMITTED"
	case 2:
		return "READ COMMITTED"
	case 3:
		return "REPEATABLE READ"
	case 4:
		return "SERIALIZABLE"
	case 5:
		return "SNAPSHOT"
	}
	return "unspecified"
}

// KillRefusal is why a session may not be killed from kakapo: a SQL Server system session, or
// kakapo's own connection. "" when it may.
func KillRefusal(s BlockingSession, ownSession int) string {
	switch {
	case s.Session <= 50 || !s.IsUser:
		return fmt.Sprintf("Session %d is a SQL Server system session: SQL Server needs it, so kakapo does not kill it.", s.Session)
	case s.Session == ownSession || s.Program == BlockingAppName:
		return fmt.Sprintf("Session %d is kakapo's own connection: closing kakapo ends it.", s.Session)
	}
	return ""
}

// BuildBlockingTree builds the tree, the sentences and the hand-over text from what was read.
// capturedAt is when (UTC) the page read it.
func BuildBlockingTree(f BlockingFacts, capturedAt time.Time) BlockingTree {
	t := BlockingTree{CapturedAt: capturedAt, ServerName: f.ServerName, Database: f.Database, EngineVersion: f.ProductVersion,
		RCSI: f.RCSI, OfferRCSI: f.Found && !f.RCSI, Heads: []BlockingNode{}, Explain: []string{}, Notes: []string{}}

	if f.SessionsError != "" {
		t.Explain = append(t.Explain, "kakapo could not read the blocking chain: "+f.SessionsError)
		t.Handover = blkHandover(t)
		return t
	}
	if f.LocksError != "" {
		t.Notes = append(t.Notes, "The locked tables could not be read, so the chain shows SQL Server's resource text instead: "+f.LocksError)
	}
	if f.NamesError != "" {
		t.Notes = append(t.Notes, "Some locked tables could not be named: "+f.NamesError)
	}
	if f.JobsError != "" {
		t.Notes = append(t.Notes, "SQL Agent job names could not be read from msdb: "+f.JobsError)
	}

	sessions := make([]BlockingSession, len(f.Sessions))
	copy(sessions, f.Sessions)
	waits := map[int]LockWait{}
	for _, w := range f.Waits {
		if _, dup := waits[w.Session]; !dup {
			waits[w.Session] = w
		}
	}
	byID := map[int]int{}
	for i := range sessions {
		s := &sessions[i]
		s.Isolation = IsolationName(s.IsolationLevel)
		s.Sleeping = strings.EqualFold(s.Status, "sleeping")
		s.TranSeconds = blkAge(f.ServerNow, s.TranStarted)
		if !s.Sleeping {
			s.RequestSeconds = blkAge(f.ServerNow, s.RequestStarted)
		}
		if w, ok := waits[s.Session]; ok {
			s.ResourceTable = w.Object
			s.Resource = w.Object
			if w.Object != "" && w.Index != "" {
				s.Resource += " (" + w.Index + ")"
			}
			if w.WaitMode != "" {
				s.LockMode = "wants " + w.WaitMode
				if w.HeldMode != "" {
					s.LockMode += ", held " + w.HeldMode
				}
			}
		}
		if s.Resource == "" {
			s.Resource = s.WaitResource
		}
		byID[s.Session] = i
	}

	children := map[int][]int{}
	for i, s := range sessions {
		if s.BlockedBy > 0 && s.BlockedBy != s.Session {
			if _, ok := byID[s.BlockedBy]; ok {
				children[s.BlockedBy] = append(children[s.BlockedBy], i)
			}
		}
	}
	for k := range children {
		c := children[k]
		sort.Slice(c, func(a, b int) bool {
			if sessions[c[a]].WaitMs != sessions[c[b]].WaitMs {
				return sessions[c[a]].WaitMs > sessions[c[b]].WaitMs
			}
			return sessions[c[a]].Session < sessions[c[b]].Session
		})
	}

	visited := map[int]bool{}
	var build func(i int) BlockingNode
	build = func(i int) BlockingNode {
		s := sessions[i]
		visited[s.Session] = true
		n := BlockingNode{BlockingSession: s, Kinds: []string{}, Victims: []BlockingNode{}}
		for _, c := range children[s.Session] {
			if visited[sessions[c].Session] {
				continue
			}
			v := build(c)
			n.Victims = append(n.Victims, v)
			n.Behind += 1 + v.Behind
			n.LongestWaitSec = max(n.LongestWaitSec, sessions[c].WaitMs/1000, v.LongestWaitSec)
		}
		n.Kinds = blkKinds(n)
		if r := KillRefusal(s, f.OwnSession); r != "" {
			n.KillRefusal = r
		} else {
			n.CanKill = true
		}
		return n
	}

	// Heads: blocked by nobody in the chain. A head whose victims have gone since the read is left out.
	for i, s := range sessions {
		_, blockerInChain := byID[s.BlockedBy]
		if (s.BlockedBy == 0 || s.BlockedBy == s.Session || !blockerInChain) && len(children[s.Session]) > 0 {
			t.Heads = append(t.Heads, build(i))
		}
	}
	// Sessions blocking each other in a circle (a deadlock about to be broken) have no head: cut the
	// circle at its lowest session id.
	for i, s := range sessions {
		if !visited[s.Session] && len(children[s.Session]) > 0 && s.BlockedBy > 0 {
			t.Heads = append(t.Heads, build(i))
		}
	}
	sort.SliceStable(t.Heads, func(a, b int) bool {
		if t.Heads[a].Behind != t.Heads[b].Behind {
			return t.Heads[a].Behind > t.Heads[b].Behind
		}
		return t.Heads[a].LongestWaitSec > t.Heads[b].LongestWaitSec
	})
	for _, h := range t.Heads {
		t.Blocked += h.Behind
	}
	t.Explain = append(t.Explain, BlockingExplain(t)...)
	t.Handover = blkHandover(t)
	return t
}

func blkAge(now, then time.Time) int64 {
	if then.IsZero() || now.IsZero() || now.Before(then) {
		return 0
	}
	return int64(now.Sub(then) / time.Second)
}

// blkKinds classifies a session as sp_HumanEventsBlockViewer does (its findings "Sleeping Query
// Blocking", "Implicit Transaction Blocking", "Serializable Blocking", "Repeatable Read Blocking",
// "Compile Lock Blocking"), plus a statement that has run long and a session rolling back a KILL.
func blkKinds(n BlockingNode) []string {
	k := []string{}
	if strings.HasPrefix(strings.ToUpper(n.Command), "KILLED") {
		k = append(k, BlockRollingBack)
	}
	if n.Sleeping {
		k = append(k, BlockSleeping)
	}
	if n.TranName == "implicit_transaction" {
		k = append(k, BlockImplicit)
	}
	switch n.IsolationLevel {
	case 4:
		k = append(k, BlockSerializable)
	case 3:
		k = append(k, BlockRepeatableRead)
	}
	for _, v := range n.Victims {
		if strings.Contains(strings.ToUpper(v.WaitResource), "COMPILE") {
			k = append(k, BlockCompile)
			break
		}
	}
	if !n.Sleeping && n.RequestSeconds >= blkLongRunningSec && !strings.HasPrefix(strings.ToUpper(n.Command), "KILLED") {
		k = append(k, BlockLongRunning)
	}
	return k
}

func blkHas(kinds []string, k string) bool {
	for _, x := range kinds {
		if x == k {
			return true
		}
	}
	return false
}

// BlockDuration writes seconds as the page says them: "45 seconds", "6 minutes", "1.5 hours".
func BlockDuration(sec int64) string {
	switch {
	case sec < 60:
		return plural(sec, "second", "seconds")
	case sec < 5400:
		return plural(int64(math.Round(float64(sec)/60)), "minute", "minutes")
	}
	h := strings.TrimSuffix(fmt.Sprintf("%.1f", float64(sec)/3600), ".0")
	return h + " hours"
}

// blkWho is "(GraniteScheduler on WMS-APP01)": the program or SQL Agent job, and the host.
func blkWho(s BlockingSession) string {
	what := s.Program
	switch {
	case s.JobName != "":
		what = "SQL Agent job " + s.JobName
	case strings.HasPrefix(s.Program, "SQLAgent - TSQL JobStep"):
		what = "a SQL Agent job"
	case what == "":
		what = orQ(s.Login)
	}
	if s.Host == "" {
		return "(" + what + ")"
	}
	return "(" + what + " on " + s.Host + ")"
}

// blkOwner is who to ask: "granite on WMS-APP01".
func blkOwner(s BlockingSession) string {
	if s.Host == "" {
		return orQ(s.Login)
	}
	return orQ(s.Login) + " on " + s.Host
}

// BlockSnippet is a statement on one line, at most n characters.
func BlockSnippet(stmt string, n int) string {
	s := strings.Join(strings.Fields(stmt), " ")
	if r := []rune(s); len(r) > n {
		return strings.TrimRight(string(r[:n-1]), " ") + "…"
	}
	return s
}

// blkWhat is what a session runs: its procedure, or the statement in quotes.
func blkWhat(s BlockingSession) string {
	if s.ObjectName != "" {
		return s.ObjectName
	}
	if s.Statement == "" {
		return "a statement kakapo could not read"
	}
	return `"` + BlockSnippet(s.Statement, 80) + `"`
}

// blkResource is the table a head blocker's victims wait on: the one most of them name.
func blkResource(n BlockingNode) string {
	count := map[string]int{}
	best := ""
	for _, v := range n.Victims {
		r := v.ResourceTable
		if r == "" {
			continue
		}
		count[r]++
		if count[r] > count[best] || best == "" {
			best = r
		}
	}
	return best
}

func blkWaitWord(n int) string {
	if n == 1 {
		return "1 session waits"
	}
	return fmt.Sprintf("%s sessions wait", n0int(int64(n)))
}

// BlockingExplain is the page's sentences, in the order they show: how many wait, then for each head
// blocker what it holds and why, with the action that fits; READ COMMITTED SNAPSHOT last.
func BlockingExplain(t BlockingTree) []string {
	out := []string{}
	if len(t.Heads) == 0 {
		return append(out, "No session is blocking another right now. Blocking often lasts only seconds: Watch for 30 seconds to catch it.")
	}
	chains := "1 blocking chain"
	if len(t.Heads) > 1 {
		chains = fmt.Sprintf("%d blocking chains", len(t.Heads))
	}
	out = append(out, fmt.Sprintf("Right now %s in %s.", blkWaitWord(t.Blocked), chains))

	readers := 0
	for _, h := range t.Heads {
		out = append(out, blkHeadSentence(h))
		out = append(out, blkKindSentences(h)...)
		readers += blkReaders(h, t.Database)
	}
	if t.OfferRCSI && readers > 0 {
		who := "1 session is reading and waits"
		if readers > 1 {
			who = fmt.Sprintf("%d sessions are reading and wait", readers)
		}
		out = append(out, fmt.Sprintf("%s for a writer in %s. READ COMMITTED SNAPSHOT lets readers see the last committed rows instead of waiting: Turn on READ COMMITTED SNAPSHOT below.", who, t.Database))
	}
	return out
}

// blkReaders counts the sessions behind a head, in this database, that wait to read (a shared lock).
func blkReaders(n BlockingNode, database string) int {
	c := 0
	for _, v := range n.Victims {
		if (v.WaitType == "LCK_M_S" || v.WaitType == "LCK_M_IS") && strings.EqualFold(v.Database, database) {
			c++
		}
		c += blkReaders(v, database)
	}
	return c
}

// blkHeadSentence: "Session 83 (GraniteScheduler on WMS-APP01) has held a lock on dbo.MasterItem for 6
// minutes in a transaction that is still open and is doing nothing (sleeping); 14 sessions wait behind it."
func blkHeadSentence(h BlockingNode) string {
	who := fmt.Sprintf("Session %d %s", h.Session, blkWho(h.BlockingSession))
	behind := blkWaitWord(h.Behind) + " behind it"
	if h.LongestWaitSec >= 1 && h.Behind > 1 {
		behind += ", the longest for " + BlockDuration(h.LongestWaitSec)
	}
	if blkHas(h.Kinds, BlockRollingBack) {
		done := ""
		if h.PercentComplete > 0 {
			done = fmt.Sprintf(" (%s%% done)", n0(h.PercentComplete))
		}
		return fmt.Sprintf("%s is being killed and is rolling its work back%s; %s until it finishes.", who, done, blkWaitWord(h.Behind))
	}
	held := "has held locks"
	if r := blkResource(h); r != "" {
		held = "has held a lock on " + r
	}
	age := h.TranSeconds
	if age == 0 {
		age = max(h.RequestSeconds, h.LongestWaitSec)
	}
	if age > 0 {
		held += " for " + BlockDuration(age)
	}
	var state string
	switch {
	case h.Sleeping && blkHas(h.Kinds, BlockImplicit):
		state = "in an implicit transaction (opened by the driver, not by the code) that is still open and is doing nothing (sleeping)"
	case h.Sleeping && h.OpenTran > 0:
		state = "in a transaction that is still open and is doing nothing (sleeping)"
	case h.Sleeping:
		state = "and is doing nothing (sleeping)"
	case blkHas(h.Kinds, BlockLongRunning):
		state = fmt.Sprintf("while one statement has run for %s: %s", BlockDuration(h.RequestSeconds), blkWhat(h.BlockingSession))
	default:
		state = "while it runs " + blkWhat(h.BlockingSession)
	}
	return fmt.Sprintf("%s %s %s; %s.", who, held, state, behind)
}

// blkKindSentences: what each classification means and what to do, naming the action.
func blkKindSentences(h BlockingNode) []string {
	out := []string{}
	n := h.Session
	kill := fmt.Sprintf("Kill session %d", n)
	if !h.CanKill {
		kill = ""
	}
	rollback := ""
	if h.TranSeconds > 0 {
		rollback = fmt.Sprintf(" (the rollback can take up to %s)", BlockDuration(h.TranSeconds))
	}
	if blkHas(h.Kinds, BlockRollingBack) {
		return append(out, fmt.Sprintf("Session %d was killed and SQL Server is undoing its work; nothing makes that faster, and killing it again does nothing. Wait for it to finish.", n))
	}
	if blkHas(h.Kinds, BlockSleeping) {
		s := fmt.Sprintf("A sleeping session with an open transaction is usually an application that did not commit, or an SSMS window left open. Ask %s to commit or close it", blkOwner(h.BlockingSession))
		if kill != "" {
			s += "; " + kill + " ends it and undoes its work" + rollback
		}
		out = append(out, s+".")
	}
	if blkHas(h.Kinds, BlockImplicit) {
		out = append(out, fmt.Sprintf("Session %d runs with implicit transactions: every statement opens a transaction that stays open until the code commits. Copy the chain for the vendor: the code should commit sooner, or turn implicit transactions off.", n))
	}
	if blkHas(h.Kinds, BlockSerializable) {
		out = append(out, fmt.Sprintf("Session %d uses the SERIALIZABLE isolation level, which locks ranges of rows and blocks far more than the default (.NET's TransactionScope picks it unless told otherwise). Copy the chain for the vendor.", n))
	}
	if blkHas(h.Kinds, BlockRepeatableRead) {
		out = append(out, fmt.Sprintf("Session %d uses the REPEATABLE READ isolation level, which keeps every row it read locked until it commits. Copy the chain for the vendor.", n))
	}
	if blkHas(h.Kinds, BlockCompile) {
		out = append(out, fmt.Sprintf("The sessions behind session %d wait on a compile lock: usually a procedure called without its schema (EXEC MyProc instead of EXEC dbo.MyProc) or one that recompiles on every call. Copy the chain for the vendor.", n))
	}
	if blkHas(h.Kinds, BlockLongRunning) {
		s := fmt.Sprintf("Its statement has run for %s. If it should be quicker, Copy it for the vendor", BlockDuration(h.RequestSeconds))
		if kill != "" {
			s += "; " + kill + " undoes everything it did so far, which can take as long again"
		}
		out = append(out, s+".")
	}
	if len(out) == 0 && kill != "" {
		out = append(out, fmt.Sprintf("Find out from %s what session %d is doing before acting; %s undoes its work%s.", blkOwner(h.BlockingSession), n, kill, rollback))
	}
	if h.KillRefusal != "" {
		out = append(out, h.KillRefusal)
	}
	return out
}

// blkHandover is the chain as text for the vendor: the sentences, then the tree, two spaces a level.
func blkHandover(t BlockingTree) string {
	var b strings.Builder
	b.WriteString("Blocking chain")
	if t.ServerName != "" {
		b.WriteString(" on " + t.ServerName)
	}
	if t.Database != "" {
		b.WriteString(" (database " + t.Database + ")")
	}
	b.WriteString("\n\n")
	for _, s := range t.Explain {
		b.WriteString(s + "\n")
	}
	for _, n := range t.Notes {
		b.WriteString(n + "\n")
	}
	if len(t.Heads) > 0 {
		b.WriteString("\nThe chain (each session under the one it waits for)\n")
		var walk func(n BlockingNode, depth int)
		walk = func(n BlockingNode, depth int) {
			b.WriteString(strings.Repeat("  ", depth) + blkLine(n) + "\n")
			for _, v := range n.Victims {
				walk(v, depth+1)
			}
		}
		for _, h := range t.Heads {
			walk(h, 0)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// blkLine is one session in the hand-over: who, state, transaction, wait, statement.
func blkLine(n BlockingNode) string {
	parts := []string{fmt.Sprintf("Session %d: %s, %s", n.Session, orQ(n.Login), strings.Trim(blkWho(n.BlockingSession), "()"))}
	state := n.Status
	if n.Database != "" {
		state += ", database " + n.Database
	}
	parts = append(parts, state)
	if n.TranSeconds > 0 {
		parts = append(parts, fmt.Sprintf("transaction open %s, %s MB of log", BlockDuration(n.TranSeconds), n1(float64(n.LogBytes)/1048576)))
	}
	parts = append(parts, n.Isolation)
	if n.WaitType != "" {
		w := fmt.Sprintf("waits %s on %s", BlockDuration(n.WaitMs/1000), n.WaitType)
		if n.Resource != "" {
			w += " for " + n.Resource
		}
		parts = append(parts, w)
	}
	label := "runs"
	if n.StatementIsLast {
		label = "last ran"
	}
	if n.Statement != "" {
		what := BlockSnippet(n.Statement, 200)
		if n.ObjectName != "" {
			what = n.ObjectName + ": " + what
		}
		parts = append(parts, label+" "+what)
	}
	return strings.Join(parts, "; ")
}
