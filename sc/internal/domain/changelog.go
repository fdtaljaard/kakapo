package domain

import (
	"fmt"
	"strings"
	"time"
)

// The change audit, step two (docs/phase5w-contract.md, B5-B7): kakapo's change log on the server,
// master.dbo.kakapo_ChangeLog, append-only by its trigger. install-change-log creates it and copies kakapo's
// own log of the server into it; remove-change-log drops it. While it is installed every action's started
// and ended rows are written there as well as locally, and every read puts back the rows it lacks. Pure:
// the statements, the previews, the read-backs, the catch-up's arithmetic and CHG02.

const (
	// FixInstallChangeLog installs kakapo's change log on the server (CHG02).
	FixInstallChangeLog = "install-change-log"
	// FixRemoveChangeLog takes it off the server; kakapo's own copy stays.
	FixRemoveChangeLog = "remove-change-log"
	// ChangeLogObject is what the fixes and the change rows name.
	ChangeLogObject = "master.dbo.kakapo_ChangeLog"
	// ChangeLogSetting is the fixes' setting, as FixDefs names it.
	ChangeLogSetting = "kakapo_ChangeLog"
)

// The baseline's kakapo/change_log values.
const (
	ChangeLogAbsent         = "absent"
	ChangeLogPresent        = "present"
	ChangeLogTriggerMissing = "present, its trigger missing"
	ChangeLogTriggerOff     = "present, its trigger disabled"
)

// ChangeLogInstalled says a kakapo/change_log value means the table is there (whatever its trigger).
func ChangeLogInstalled(v string) bool { return strings.HasPrefix(v, ChangeLogPresent) }

// changeLogTable, changeLogTrigger and changeLogProperty are the contract's DDL (B5), verified on SQL Server
// 2022 with SET NOEXEC ON and SET PARSEONLY ON.
const changeLogTable = `CREATE TABLE dbo.kakapo_ChangeLog (
    ID                bigint IDENTITY(1, 1) NOT NULL CONSTRAINT PK_kakapo_ChangeLog PRIMARY KEY CLUSTERED,
    ActionID          uniqueidentifier NOT NULL,
    Phase             varchar(8)       NOT NULL CONSTRAINT CK_kakapo_ChangeLog_Phase CHECK (Phase IN ('started', 'ended', 'copied')),
    LoggedAtUtc       datetime2(3)     NOT NULL CONSTRAINT DF_kakapo_ChangeLog_LoggedAtUtc DEFAULT SYSUTCDATETIME(),
    StartedAtUtc      datetime2(3)     NOT NULL,
    EndedAtUtc        datetime2(3)     NULL,
    DurationMs        bigint           NULL,
    WindowsUser       nvarchar(256)    NOT NULL,
    SqlLogin          nvarchar(256)    NOT NULL CONSTRAINT DF_kakapo_ChangeLog_SqlLogin DEFAULT ORIGINAL_LOGIN(),
    KakapoHost        nvarchar(256)    NOT NULL,
    KakapoVersion     varchar(64)      NOT NULL,
    DatabaseName      sysname          NULL,
    ObjectName        nvarchar(512)    NOT NULL,
    ChangeType        varchar(32)      NOT NULL,
    FixID             varchar(64)      NOT NULL,
    Description       nvarchar(2000)   NOT NULL,
    Statement         nvarchar(max)    NOT NULL,
    RollbackStatement nvarchar(max)    NULL,
    ValueBefore       nvarchar(4000)   NULL,
    ValueAfter        nvarchar(4000)   NULL,
    Outcome           varchar(16)      NULL CONSTRAINT CK_kakapo_ChangeLog_Outcome
                      CHECK (Outcome IN ('verified', 'differs', 'failed', 'stopped', 'unknown')),
    Result            nvarchar(4000)   NULL,
    CONSTRAINT UQ_kakapo_ChangeLog_Action UNIQUE (ActionID, Phase)
);`

const changeLogTrigger = `CREATE TRIGGER dbo.kakapo_ChangeLog_AppendOnly ON dbo.kakapo_ChangeLog
AFTER UPDATE, DELETE
AS
BEGIN
    SET NOCOUNT ON;
    THROW 50001, N'kakapo_ChangeLog is append-only: kakapo never changes or deletes its rows. Use kakapo''s remove fix to take the log off this server.', 1;
END;`

const changeLogProperty = `EXEC sys.sp_addextendedproperty @name = N'kakapo', @value = N'kakapo''s change log: every action kakapo took on this server, before and after',
     @level0type = N'SCHEMA', @level0name = N'dbo', @level1type = N'TABLE', @level1name = N'kakapo_ChangeLog';`

// changeLogDrop is remove-change-log's statement, and install-change-log's rollback.
const changeLogDrop = `USE master;
GO
IF OBJECT_ID(N'dbo.kakapo_ChangeLog_AppendOnly', N'TR') IS NOT NULL DROP TRIGGER dbo.kakapo_ChangeLog_AppendOnly;
GO
IF OBJECT_ID(N'dbo.kakapo_ChangeLog', N'U') IS NOT NULL DROP TABLE dbo.kakapo_ChangeLog;`

// ChangeLogFacts is what the fixes are built from, and their read-back.
type ChangeLogFacts struct {
	ServerName     string
	ProductVersion string
	Table          bool
	Trigger        bool // the append-only trigger exists
	TriggerOff     bool // it exists and is disabled
	Property       bool // the extended property named kakapo is on the table
	Rows           int64
}

// State is the facts as the baseline's kakapo/change_log says them.
func (f ChangeLogFacts) State() string {
	switch {
	case !f.Table:
		return ChangeLogAbsent
	case !f.Trigger:
		return ChangeLogTriggerMissing
	case f.TriggerOff:
		return ChangeLogTriggerOff
	}
	return ChangeLogPresent
}

const (
	warnChangeLogObject = "A server object in master: it is not in any database, so it stays when a database is dropped or restored elsewhere."
	warnChangeLogSize   = "One table and one trigger in master; a few KB per action; kakapo never deletes from it, and kakapo's maintenance clean-up never touches it."
	warnChangeLogPerm   = "Needs CREATE TABLE in master (sysadmin has it)."
)

// BuildInstallChangeLogFix builds install-change-log's preview: what is missing of the table, the trigger
// and the property, then the copy of kakapo's own log of this server (local rows). ErrFixNotApplicable when
// all three are there and the trigger is on.
func BuildInstallChangeLogFix(f ChangeLogFacts, local int) (FixPreview, Fix, error) {
	if f.Table && f.Trigger && !f.TriggerOff && f.Property {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	fix := suFix(FixInstallChangeLog)
	fix.Current, fix.Target = f.State(), ChangeLogPresent
	if f.Table && f.Trigger && !f.TriggerOff {
		fix.Current = "present, without its description"
	}
	p := FixPreview{EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}}
	var b strings.Builder
	b.WriteString("USE master;\nGO\n")
	if !f.Table {
		p.Changes = append(p.Changes, Drift{Setting: "Table " + ChangeLogObject, Current: "missing", Target: "created"})
		b.WriteString(changeLogTable + "\nGO\n")
	}
	switch {
	case !f.Trigger:
		p.Changes = append(p.Changes, Drift{Setting: "Trigger dbo.kakapo_ChangeLog_AppendOnly", Current: "missing", Target: "created (refuses UPDATE and DELETE)"})
		b.WriteString(changeLogTrigger + "\nGO\n")
	case f.TriggerOff:
		p.Changes = append(p.Changes, Drift{Setting: "Trigger dbo.kakapo_ChangeLog_AppendOnly", Current: "disabled", Target: "enabled"})
		b.WriteString("ENABLE TRIGGER dbo.kakapo_ChangeLog_AppendOnly ON dbo.kakapo_ChangeLog;\nGO\n")
	}
	if !f.Property {
		p.Changes = append(p.Changes, Drift{Setting: "Extended property kakapo", Current: "missing", Target: "added"})
		b.WriteString(changeLogProperty + "\nGO\n")
	}
	p.Changes = append(p.Changes, Drift{Setting: "kakapo's own log of this server", Current: plural(int64(local), "row", "rows") + " in kakapo only",
		Target: "copied to the table"})
	p.Statement = strings.TrimSuffix(b.String(), "\nGO\n")
	p.Rollback = changeLogDrop
	if f.Table {
		p.Rollback = "-- The table was there before: remove it with kakapo's remove fix.\n" + changeLogDrop
	}
	p.Lead = "Keeps kakapo's change log on this server, so anyone on it can see what kakapo did there, before and after each change."
	p.Warnings = append(p.Warnings, warnChangeLogObject, warnChangeLogSize,
		"From now on every kakapo change here writes its started row to the table first: if that write fails, the change is refused.",
		warnChangeLogPerm)
	return p, fix, nil
}

// ChangeLogInstallReadBack says what is in master after the install: verified when the table, the trigger
// (on) and the property exist and the table holds every row of kakapo's own log of the server.
func ChangeLogInstallReadBack(f ChangeLogFacts, local, held int, copyErr string) (string, bool) {
	missing := []string{}
	if !f.Table {
		missing = append(missing, "the table")
	}
	if !f.Trigger {
		missing = append(missing, "the trigger")
	}
	if f.Table && !f.Property {
		missing = append(missing, "the extended property")
	}
	if len(missing) > 0 {
		return "missing in master: " + strings.Join(missing, ", "), false
	}
	s := fmt.Sprintf("present, holding %s of kakapo's %d", plural(int64(held), "row", "rows"), local)
	if f.TriggerOff {
		return s + ", but its trigger is disabled", false
	}
	if copyErr != "" {
		return s + "; the copy stopped: " + copyErr, false
	}
	return s, held >= local
}

// BuildRemoveChangeLogFix builds remove-change-log's preview. host is the machine kakapo runs on, whose copy
// stays. ErrFixNotApplicable when the table is not there.
func BuildRemoveChangeLogFix(f ChangeLogFacts, host string) (FixPreview, Fix, error) {
	if !f.Table && !f.Trigger {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	fix := suFix(FixRemoveChangeLog)
	fix.Current, fix.Target = f.State(), ChangeLogAbsent
	p := FixPreview{EngineVersion: f.ProductVersion, Warnings: []string{}, Statement: changeLogDrop,
		Changes:  []Drift{{Setting: "Table " + ChangeLogObject, Current: fmt.Sprintf("present, %s", plural(f.Rows, "row", "rows")), Target: "dropped"}},
		Rollback: "-- Install kakapo's change log again (install-change-log): kakapo copies its own log of this server back into it."}
	if f.Trigger {
		p.Changes = append(p.Changes, Drift{Setting: "Trigger dbo.kakapo_ChangeLog_AppendOnly", Current: "present", Target: "dropped"})
	}
	p.Lead = "Takes kakapo's change log off this server."
	p.Warnings = append(p.Warnings, "Anyone on the server loses the record of what kakapo did here. kakapo's own copy on "+orQ(host)+" stays complete.",
		warnChangeLogPerm)
	return p, fix, nil
}

// ChangeLogRemoveReadBack is what was read after the removal: verified when neither the table nor the
// trigger is there.
func ChangeLogRemoveReadBack(f ChangeLogFacts) (string, bool) {
	if f.Table || f.Trigger {
		return "still in master: " + f.State(), false
	}
	return ChangeLogAbsent, true
}

// ChangeLogInstallDescription and ChangeLogRemoveDescription are the change rows' lines.
func ChangeLogInstallDescription(server string, f ChangeLogFacts) string {
	return "kakapo's change log on " + server + ": " + f.State() + " -> " + ChangeLogPresent
}

func ChangeLogRemoveDescription(server string, f ChangeLogFacts) string {
	return "kakapo's change log on " + server + ": " + f.State() + " -> " + ChangeLogAbsent
}

// ChangeLogMissingWarning is B7's warning on every fix's preview while the table is not on the server. host
// is the machine kakapo runs on.
func ChangeLogMissingWarning(host string) string {
	return "kakapo's log is not on this server yet, so this change is recorded only in kakapo's own file on " + orQ(host) +
		". Install it (one table in master) so anyone on the server can see what kakapo did."
}

// ChangeLogRefusal is why an action is refused when its started row cannot be written on the server (B6).
func ChangeLogRefusal(err error) string {
	return "kakapo could not write its log on the server: " + err.Error() + ". Fix the permission, or remove kakapo's log from this server"
}

// --- B6: the catch-up ---

// ChangeLogKey is an action log row's key on the server: its action id (lower case) and phase.
func ChangeLogKey(actionID, phase string) string {
	return strings.ToLower(strings.TrimSpace(actionID)) + "|" + phase
}

// ChangeLogSync is what kakapo remembers of the table on one server between reads: how many of its own
// rows it knows the table holds, when it last saw that, and the losses found so far.
type ChangeLogSync struct {
	Expected  int             `json:"expected"`
	CheckedAt *time.Time      `json:"checkedAt,omitempty"`
	Losses    []ChangeLogLoss `json:"losses,omitempty"`
}

// ChangeLogLoss is rows of kakapo's that the table lost between two reads (a TRUNCATE, or the table dropped
// and created again): n rows, last seen whole at Since, found missing at Found. kakapo put back the ones it
// holds.
type ChangeLogLoss struct {
	N     int       `json:"n"`
	Since time.Time `json:"since"`
	Found time.Time `json:"found"`
}

// ChangeLogCatchUp compares kakapo's own log of a server (local, oldest first) with the keys the table holds:
// the rows to put there, oldest first, and how many of the rows kakapo knew the table held are gone (never
// negative).
func ChangeLogCatchUp(local []ActionLogRow, held map[string]bool, expected int) (missing []ActionLogRow, lost int) {
	have := 0
	for _, r := range local {
		if held[ChangeLogKey(r.ActionID, r.Phase)] {
			have++
			continue
		}
		missing = append(missing, r)
	}
	if expected > have {
		lost = expected - have
	}
	return missing, lost
}

// ServerOnlyActions are the actions the table holds that kakapo's own log of the server does not: another
// kakapo install's (B6, two installs on one server), grouped.
func ServerOnlyActions(server []ActionLogRow, local []ActionLogRow) []AuditAction {
	mine := map[string]bool{}
	for _, r := range local {
		mine[strings.ToLower(r.ActionID)] = true
	}
	other := []ActionLogRow{}
	for _, r := range server {
		if !mine[strings.ToLower(r.ActionID)] {
			other = append(other, r)
		}
	}
	return GroupActions(other)
}

// --- CHG02 ---

// chg02: kakapo has changed this server and its log is not on it (Low, install-change-log). Only when the
// table's state was read and kakapo's log holds an action for the server.
func (b *upkeepBuild) chg02() {
	f := b.f
	if !f.KakapoActed || f.ChangeLog != ChangeLogAbsent {
		return
	}
	b.add("CHG02", 4, "Online", EffortQuickWin, f.ServerName,
		"kakapo has changed this server, and its log is not on it.",
		"kakapo keeps its own log of every change it makes, but only in its file on the machine it runs on. Anyone else on the server (another consultant, the client's IT, an auditor) cannot see what kakapo did, nor when, nor who ran it.",
		"Install kakapo's change log (the fix): one table and one trigger in master that refuse changes and deletes. kakapo copies its own log of this server into it, then writes every change there before it runs.",
		"master.dbo.kakapo_ChangeLog does not exist; kakapo's own log holds actions on this server.", FixInstallChangeLog)
}

// changeLogHelperLine is the Installed helpers row of kakapo's change log; false when it was not read.
func changeLogHelperLine(f *ChangeLogFacts) (InstalledHelper, bool) {
	h := InstalledHelper{ID: "change-log", Title: "kakapo's change log (" + ChangeLogObject + ")", Details: []string{}}
	if f == nil {
		return h, false
	}
	switch {
	case !f.Table && !f.Trigger:
		h.State, h.FixID, h.FixLabel, h.FixTitle = HelperNotInstalled, FixInstallChangeLog, "Create", "Keep kakapo's change log on this server"
		h.Sentence = "Not on this server: kakapo's log of what it changed here is only in its own file. One table in master would let anyone on the server see it."
	case !f.Trigger || f.TriggerOff || !f.Property:
		h.State, h.FixID, h.FixLabel, h.FixTitle = HelperInstalled, FixInstallChangeLog, "Repair", "Keep kakapo's change log on this server"
		h.Sentence = "kakapo's change log is on this server, but " + strings.TrimPrefix(f.State(), ChangeLogPresent+", ") + ": nothing stops its rows being changed or deleted."
		if f.Trigger && !f.TriggerOff {
			h.Sentence = "kakapo's change log is on this server, without the extended property that names it."
		}
	default:
		h.State, h.FixID, h.FixLabel, h.FixTitle = HelperInstalled, FixRemoveChangeLog, "Remove", "Remove kakapo's change log from this server"
		h.Sentence = fmt.Sprintf("kakapo's change log is on this server, holding %s; its trigger refuses changes and deletes.", plural(f.Rows, "row", "rows"))
	}
	return h, true
}
