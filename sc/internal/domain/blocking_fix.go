package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The blocking page's two fixes (docs/phase4c-contract.md, part 2): kill-session, which ends one
// session and rolls its work back, and rcsi, which turns READ COMMITTED SNAPSHOT on for the database.

// Fix ids.
const (
	FixKillSession = "kill-session"
	FixRCSI        = "rcsi"
)

// KillPermission is the permission KILL needs.
const KillPermission = "Needs the ALTER ANY CONNECTION permission (sysadmin or processadmin)."

// ErrSessionGone is a kill-session preview for a session that is no longer connected.
var ErrSessionGone = errors.New("the session is no longer connected")

// killLoginTimeLayout writes a session's login time into the preview's options, so that Apply can tell
// the session it previewed from a new one that got the same id.
const killLoginTimeLayout = "2006-01-02T15:04:05.000"

// ParseSessionID reads the sessionId option: a session id SQL Server can hand out (1 to 32767).
func ParseSessionID(v string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 || n > 32767 {
		return 0, fmt.Errorf("sessionId must be a session number, not %q", v)
	}
	return n, nil
}

// KillLoginTime is a login time as the kill preview echoes it in its options.
func KillLoginTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(killLoginTimeLayout)
}

// KillStatement is the statement of the kill-session fix.
func KillStatement(session int) string { return fmt.Sprintf("KILL %d;", session) }

// KillStatusOnlyStatement asks how far a killed session's rollback has come; it changes nothing.
func KillStatusOnlyStatement(session int) string {
	return fmt.Sprintf("KILL %d WITH STATUSONLY;", session)
}

// KillTarget is the session a kill-session preview reads again (collect.ReadKillTarget).
type KillTarget struct {
	Found          bool
	Session        BlockingSession
	Behind         int // requests waiting on it now
	OwnSession     int
	ServerName     string
	ProductVersion string
	ServerNow      time.Time
	JobsError      string
}

// RollingBack says whether the session is already killed and rolling back.
func (t KillTarget) RollingBack() bool {
	return strings.HasPrefix(strings.ToUpper(t.Session.Command), "KILLED")
}

// BuildKillSessionFix builds the kill-session preview from the session read again: who, what it runs,
// its transaction's age and log, the rollback estimate. A system session, kakapo's own and one already
// rolling back are refused: the preview has the reason and no change, so there is nothing to apply.
// ErrSessionGone when the session is not connected any more.
func BuildKillSessionFix(t KillTarget) (FixPreview, Fix, error) {
	if !t.Found {
		return FixPreview{}, Fix{}, ErrSessionGone
	}
	s := t.Session
	s.Sleeping = strings.EqualFold(s.Status, "sleeping")
	s.TranSeconds = blkAge(t.ServerNow, s.TranStarted)
	if !s.Sleeping {
		s.RequestSeconds = blkAge(t.ServerNow, s.RequestStarted)
	}
	n := s.Session
	def, _ := FindFixDef(FixKillSession)
	current := killCurrent(s)
	fix := Fix{ID: def.ID, Title: def.Title, Scope: def.Scope, CheckID: def.CheckID, Setting: fmt.Sprintf("Session %d", n),
		Current: current, Target: "killed", Options: map[string]string{"sessionId": strconv.Itoa(n)}}
	p := FixPreview{Database: s.Database, EngineVersion: t.ProductVersion, Changes: []Drift{}, Warnings: []string{},
		Options: map[string]string{"sessionId": strconv.Itoa(n)}}
	if lt := KillLoginTime(s.LoginTime); lt != "" {
		p.Options["loginTime"] = lt
	}

	who := fmt.Sprintf("Session %d: login %s", n, orQ(s.Login))
	if s.Host != "" {
		who += " on " + s.Host
	}
	if s.Program != "" {
		who += ", program " + s.Program
	}
	if s.Database != "" {
		who += ", database " + s.Database
	}
	who += ", " + orQ(s.Status) + "."
	if t.Behind > 0 {
		who += " " + blkWaitWord(t.Behind) + " behind it."
	}

	refusal := KillRefusal(s, t.OwnSession)
	if refusal == "" && t.RollingBack() {
		refusal = fmt.Sprintf("Session %d is already killed and is rolling its work back: killing it again does nothing. Wait for the rollback to finish.", n)
	}
	if refusal != "" {
		p.Warnings = append(p.Warnings, refusal, who)
		return p, fix, nil
	}

	p.Changes = append(p.Changes, Drift{Setting: fix.Setting, Current: current, Target: "killed"})
	p.Statement = KillStatement(n)
	p.Warnings = append(p.Warnings, who)
	switch {
	case s.JobName != "":
		p.Warnings = append(p.Warnings, fmt.Sprintf("It is the SQL Agent job %s: this run of the job fails, and the job's history says so.", s.JobName))
	case strings.HasPrefix(s.Program, "SQLAgent - TSQL JobStep"):
		p.Warnings = append(p.Warnings, "It is a SQL Agent job (its name could not be read from msdb): this run of the job fails.")
	}
	if s.OpenTran > 0 || s.TranSeconds > 0 {
		tran := "Its transaction"
		if s.TranSeconds > 0 {
			tran += " has been open for " + BlockDuration(s.TranSeconds)
		} else {
			tran += " is open"
		}
		tran += fmt.Sprintf(" and has written %s MB of log.", n1(float64(s.LogBytes)/1048576))
		p.Warnings = append(p.Warnings, tran, "Its work is rolled back: everything that transaction changed is undone.")
		est := "The rollback takes about as long as the work did"
		if s.TranSeconds > 0 {
			est += ": up to about " + BlockDuration(s.TranSeconds) + " here"
		}
		p.Warnings = append(p.Warnings, est+", and the sessions behind it keep waiting until it ends.")
	} else {
		p.Warnings = append(p.Warnings, "It has no open transaction, so nothing is rolled back and it ends at once.")
	}
	p.Warnings = append(p.Warnings, fmt.Sprintf("Tell whoever owns it (%s) before you kill it.", blkOwner(s)), KillPermission)
	return p, fix, nil
}

// killCurrent is the preview's "before": "running UPDATE dbo.MasterItem …", or "sleeping; last ran …".
func killCurrent(s BlockingSession) string {
	what := "a statement kakapo could not read"
	if s.Statement != "" {
		what = BlockSnippet(s.Statement, 120)
	}
	if s.ObjectName != "" {
		what = s.ObjectName + ": " + what
	}
	if s.Sleeping || s.StatementIsLast {
		return "sleeping; last ran " + what
	}
	return "running " + what
}

// KillChangeDescription is the change log line of a kill.
func KillChangeDescription(t KillTarget) string {
	s := t.Session
	d := fmt.Sprintf("Killed session %d (%s, %s) on %s", s.Session, orQ(s.Login), strings.Trim(blkWho(s), "()"), orQ(t.ServerName))
	if tran := blkAge(t.ServerNow, s.TranStarted); tran > 0 {
		d += fmt.Sprintf(": transaction open %s, %s MB of log rolled back", BlockDuration(tran), n1(float64(s.LogBytes)/1048576))
	}
	if t.Behind > 0 {
		d += fmt.Sprintf("; %s behind it", blkWaitWord(t.Behind))
	}
	return d
}

// KillLoginChanged says whether the session read at apply is a new connection that got the previewed
// session's id: the preview echoed loginTime and the session now logged in at another time.
func KillLoginChanged(options map[string]string, now time.Time) bool {
	want := options["loginTime"]
	return want != "" && !now.IsZero() && KillLoginTime(now) != want
}

// KillReadBack is the session read after KILL (collect.ReadKillStatus): gone, or still there and
// rolling back, with what KILL ... WITH STATUSONLY said.
type KillReadBack struct {
	Present         bool
	LoginTime       time.Time
	Status          string
	Command         string
	PercentComplete float64
	RemainingMs     int64
	Message         string // KILL WITH STATUSONLY's message, "" when not read
}

// KillResult turns the read back into the result's "after" and whether the kill took: the session
// gone (or replaced by a new connection with the same id), or rolling back.
func KillResult(session int, loginTime string, r KillReadBack) (string, bool) {
	if !r.Present || (loginTime != "" && !r.LoginTime.IsZero() && KillLoginTime(r.LoginTime) != loginTime) {
		return fmt.Sprintf("Session %d is gone.", session), true
	}
	if strings.HasPrefix(strings.ToUpper(r.Command), "KILLED") {
		s := fmt.Sprintf("Session %d is rolling its work back", session)
		if r.PercentComplete > 0 {
			s += fmt.Sprintf(": %s%% done", n0(r.PercentComplete))
			if r.RemainingMs > 0 {
				s += ", about " + BlockDuration(max(1, r.RemainingMs/1000)) + " left"
			}
		}
		s += "."
		if r.Message != "" {
			s += " SQL Server: " + strings.TrimSpace(r.Message)
		}
		return s, true
	}
	return fmt.Sprintf("Session %d is still connected (%s).", session, orQ(r.Status)), false
}

// RCSIFacts is what the rcsi fix reads: the setting, and how many sessions use the database now.
type RCSIFacts struct {
	Database       string
	RCSI           bool
	Users          int // sessions in the database other than kakapo's
	ProductVersion string
}

// BuildRCSIFix builds the READ COMMITTED SNAPSHOT preview. ErrFixNotApplicable when it is already on.
func BuildRCSIFix(f RCSIFacts) (FixPreview, Fix, error) {
	if f.RCSI {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	def, _ := FindFixDef(FixRCSI)
	db := QuoteName(f.Database)
	fix := Fix{ID: def.ID, Title: def.Title, Scope: def.Scope, CheckID: def.CheckID, Setting: def.Setting, Current: "OFF", Target: "ON"}
	users := "nobody else is connected now"
	switch {
	case f.Users == 1:
		users = "1 session is connected now"
	case f.Users > 1:
		users = fmt.Sprintf("%d sessions are connected now", f.Users)
	}
	p := FixPreview{
		Database:      f.Database,
		EngineVersion: f.ProductVersion,
		Changes:       []Drift{{Setting: def.Setting, Current: "OFF", Target: "ON"}},
		Statement:     "ALTER DATABASE " + db + " SET READ_COMMITTED_SNAPSHOT ON WITH ROLLBACK IMMEDIATE;",
		Rollback:      "ALTER DATABASE " + db + " SET READ_COMMITTED_SNAPSHOT OFF WITH ROLLBACK IMMEDIATE;",
		Warnings: []string{
			fmt.Sprintf("WITH ROLLBACK IMMEDIATE disconnects every user of %s for a moment and undoes their open transactions (%s), so apply it in a quiet minute.", f.Database, users),
			"Row versions are kept in tempdb: watch its size for a few days (the Storage card's tempdb checks).",
			"Readers stop waiting for writers; writers still wait for writers, so blocking between two updates stays.",
			"Check with the vendor that the application does not rely on readers being blocked to keep two users off the same row (rare).",
			permission(ScopeDatabase, f.Database),
		},
	}
	return p, fix, nil
}

// RCSIValue is the setting as the fix shows it.
func RCSIValue(on bool) string { return onOff(on) }
