package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestMultiUserFix(t *testing.T) {
	f := UserAccessFacts{ServerName: `SQL01\WMS`, ProductVersion: "16.0.4255.3", Database: "GraniteLive", UserAccess: "SINGLE_USER",
		Sessions: []UserAccessSession{{Session: 57, Login: `CORP\dba`, Host: "WS01", Program: "SSMS"}}}
	p, fix, err := BuildMultiUserFix(f)
	if err != nil {
		t.Fatal(err)
	}
	if p.Statement != "ALTER DATABASE [GraniteLive] SET MULTI_USER WITH ROLLBACK IMMEDIATE;" ||
		p.Rollback != "ALTER DATABASE [GraniteLive] SET SINGLE_USER WITH ROLLBACK IMMEDIATE;" ||
		!reflect.DeepEqual(p.Changes, []Drift{{"user_access", "SINGLE_USER", "MULTI_USER"}}) || p.Database != "GraniteLive" {
		t.Errorf("preview %+v", p)
	}
	want := []string{
		"Whoever set SINGLE_USER mode may be in the middle of a restore or a repair: ask them first.",
		`Connected to GraniteLive now: session 57 (CORP\dba on WS01, SSMS). WITH ROLLBACK IMMEDIATE disconnects them and undoes their unfinished work.`,
		wantDbPerm,
	}
	if !reflect.DeepEqual(p.Warnings, want) {
		t.Errorf("warnings %q", p.Warnings)
	}
	if fix.ID != FixMultiUser || fix.CheckID != "DBC13" || fix.Current != "SINGLE_USER" || fix.Target != "MULTI_USER" ||
		FixChangeDescription(fix, f.ServerName, f.Database) != "user_access on GraniteLive: SINGLE_USER -> MULTI_USER" {
		t.Errorf("fix %+v", fix)
	}

	f.UserAccess, f.Sessions = "restricted_user", nil
	if p, _, _ := BuildMultiUserFix(f); p.Rollback != "ALTER DATABASE [GraniteLive] SET RESTRICTED_USER WITH ROLLBACK IMMEDIATE;" ||
		p.Warnings[1] != "Nobody else is connected to GraniteLive now." {
		t.Errorf("restricted %+v", p)
	}
	f.SessionsError = "the login does not have VIEW SERVER STATE"
	if p, _, _ := BuildMultiUserFix(f); !strings.HasPrefix(p.Warnings[1], "kakapo could not see who is connected to GraniteLive") {
		t.Errorf("unread sessions %q", p.Warnings)
	}

	// Someone running DBCC or a restore in it: nothing to apply, the first warning says why.
	f = UserAccessFacts{Database: "GraniteLive", UserAccess: "SINGLE_USER",
		Sessions: []UserAccessSession{{Session: 61, Login: "sa", Host: "SQL01", Command: "DBCC"}}}
	p, _, err = BuildMultiUserFix(f)
	if err != nil || len(p.Changes) != 0 || p.Statement != "" ||
		p.Warnings[0] != "Session 61 (sa on SQL01) is running DBCC in GraniteLive now: kakapo does not interrupt it. Wait until it finishes, then preview again." {
		t.Errorf("busy %+v %v", p, err)
	}

	if _, _, err := BuildMultiUserFix(UserAccessFacts{Database: "GraniteLive", UserAccess: "MULTI_USER"}); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("multi user: %v", err)
	}
	if a, ok := MultiUserReadBack("MULTI_USER"); a != "MULTI_USER" || !ok {
		t.Error("read back multi user")
	}
	if _, ok := MultiUserReadBack("SINGLE_USER"); ok {
		t.Error("read back single user")
	}
}

func suPendingFacts() ServerConfigFacts {
	return ServerConfigFacts{ServerName: `SQL01\WMS`, ProductVersion: "16.0.4255.3", ShowAdvanced: 1, MinServerMemoryMB: 16, MaxServerMemoryMB: 28672,
		Pending: []PendingSetting{
			{Name: "show advanced options", Value: 0, ValueInUse: 1, Dynamic: true},
			{Name: "max degree of parallelism", Value: 4, ValueInUse: 8, Dynamic: true, Advanced: true},
			{Name: "min server memory (MB)", Value: 0, ValueInUse: 16, Dynamic: true, Advanced: true}, // SQL Server's own: not pending
			{Name: "fill factor (%)", Value: 90, ValueInUse: 0, Advanced: true},
		}}
}

func TestReconfigureFix(t *testing.T) {
	f := suPendingFacts()
	p, fix, err := BuildReconfigureFix(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantChanges := []Drift{{"fill factor (%)", "0", "90"}, {"max degree of parallelism", "8", "4"}, {"show advanced options", "1", "0"}}
	if p.Statement != "RECONFIGURE;" || strings.Contains(p.Statement, "OVERRIDE") || !reflect.DeepEqual(p.Changes, wantChanges) {
		t.Errorf("activate %+v", p)
	}
	if p.Rollback != "EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'fill factor (%)', 0; "+
		"EXEC sp_configure 'max degree of parallelism', 8; EXEC sp_configure 'show advanced options', 1; RECONFIGURE;" {
		t.Errorf("rollback %q", p.Rollback)
	}
	want := []string{
		"RECONFIGURE activates all of them at once: fill factor (%) 0 -> 90, max degree of parallelism 8 -> 4, show advanced options 1 -> 0.",
		"Activating max degree of parallelism clears the plan cache: queries compile again for a few minutes.",
		"fill factor (%) take effect only when SQL Server restarts: they stay pending until then.",
		wantServer, wantServerPerm,
	}
	if !reflect.DeepEqual(p.Warnings, want) {
		t.Errorf("warnings %q", p.Warnings)
	}
	if len(p.Inputs) != 1 || p.Inputs[0].Kind != "select" || !reflect.DeepEqual(p.Inputs[0].Options, ReconfigureChoices) || p.Inputs[0].Value != "activate" ||
		p.Options[OptionReconfigureAction] != "activate" || p.Options[OptionPendingSeen] != PendingFingerprint(f.pending()) {
		t.Errorf("inputs %+v options %+v", p.Inputs, p.Options)
	}
	if fix.ID != FixReconfigure || fix.CheckID != "INS14" || fix.Current != "3 pending" || p.Database != "" {
		t.Errorf("fix %+v", fix)
	}
	if d := ReconfigureDescription(ReconfigureActivate, f.ServerName, p.Changes); d != `RECONFIGURE on SQL01\WMS activated: fill factor (%) 0 -> 90, max degree of parallelism 8 -> 4, show advanced options 1 -> 0` {
		t.Errorf("description %q", d)
	}

	// Revert: one sp_configure per setting back to the value in use, no RECONFIGURE.
	p, _, _ = BuildReconfigureFix(f, map[string]string{OptionReconfigureAction: "revert"})
	if p.Statement != "EXEC sp_configure 'fill factor (%)', 0; EXEC sp_configure 'max degree of parallelism', 8; EXEC sp_configure 'show advanced options', 1;" ||
		strings.Contains(p.Statement, "RECONFIGURE") ||
		p.Rollback != "EXEC sp_configure 'fill factor (%)', 90; EXEC sp_configure 'max degree of parallelism', 4; EXEC sp_configure 'show advanced options', 0;" ||
		!reflect.DeepEqual(p.Changes[1], Drift{"max degree of parallelism", "pending 4", "stays 8"}) || p.Warnings[0] != reconfigureRevertOK {
		t.Errorf("revert %+v", p)
	}
	if d := ReconfigureDescription(ReconfigureRevert, "S", p.Changes[1:2]); d != "Pending settings on S set back to the values in use: max degree of parallelism 8 (was pending 4)" {
		t.Errorf("revert description %q", d)
	}
	// An advanced setting cannot be set back while 'show advanced options' is off in use.
	g := suPendingFacts()
	g.ShowAdvanced = 0
	if p, _, _ := BuildReconfigureFix(g, map[string]string{OptionReconfigureAction: "revert"}); len(p.Changes) != 0 || p.Statement != "" ||
		!strings.HasPrefix(p.Warnings[0], "fill factor (%) is an advanced setting") {
		t.Errorf("advanced revert %+v", p)
	}

	// A wrong action, or a pending list that changed since the preview: nothing to apply.
	p, _, _ = BuildReconfigureFix(f, map[string]string{OptionReconfigureAction: "drop"})
	if len(p.Changes) != 0 || p.Statement != "" || p.Inputs[0].Warning != reconfigureBadWarn || p.Inputs[0].Value != "drop" {
		t.Errorf("bad action %+v", p)
	}
	p, _, _ = BuildReconfigureFix(f, map[string]string{OptionReconfigureAction: "activate", OptionPendingSeen: "x=1/2"})
	if len(p.Changes) != 0 || p.Warnings[0] != reconfigureChanged {
		t.Errorf("changed %+v", p)
	}

	if _, _, err := BuildReconfigureFix(ServerConfigFacts{Pending: []PendingSetting{{Name: "min server memory (MB)", Value: 0, ValueInUse: 16}}}, nil); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("nothing pending: %v", err)
	}

	// Read-back: activate leaves only the restart settings; revert none.
	after := ServerConfigFacts{Pending: []PendingSetting{{Name: "fill factor (%)", Value: 90, ValueInUse: 0}}}
	names := []string{"fill factor (%)", "max degree of parallelism"}
	if s, ok := ReconfigureReadBack(ReconfigureActivate, names, after); !ok || s != "still pending: fill factor (%) (takes effect after a restart)" {
		t.Errorf("activate read back %q %v", s, ok)
	}
	if _, ok := ReconfigureReadBack(ReconfigureRevert, names, after); ok {
		t.Error("revert with one left is not verified")
	}
	if s, ok := ReconfigureReadBack(ReconfigureRevert, names, ServerConfigFacts{}); !ok || s != "none pending" {
		t.Errorf("revert read back %q", s)
	}
}

func TestMinServerMemoryFix(t *testing.T) {
	f := ServerConfigFacts{ServerName: `SQL01\WMS`, ProductVersion: "16.0.4255.3", MinServerMemoryMB: 26000, MaxServerMemoryMB: 28672,
		Pending: []PendingSetting{{Name: "cost threshold for parallelism", Value: 50, ValueInUse: 5, Dynamic: true}}}
	p, fix, err := BuildMinServerMemoryFix(f)
	if err != nil {
		t.Fatal(err)
	}
	if p.Statement != "EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'min server memory (MB)', 0; RECONFIGURE;" ||
		p.Rollback != "EXEC sp_configure 'show advanced options', 1; RECONFIGURE; EXEC sp_configure 'min server memory (MB)', 26000; RECONFIGURE;" ||
		!reflect.DeepEqual(p.Changes, []Drift{{"min server memory (MB)", "26000 MB", "0 MB"}}) {
		t.Errorf("preview %+v", p)
	}
	if p.Warnings[0] != wantServer || p.Warnings[2] != "RECONFIGURE also activates these pending changes: cost threshold for parallelism 5 -> 50." ||
		p.Warnings[len(p.Warnings)-1] != wantServerPerm {
		t.Errorf("warnings %q", p.Warnings)
	}
	if d := MinServerMemoryDescription(fix, f); d != `min server memory (MB) on SQL01\WMS: 26000 MB -> 0 MB; RECONFIGURE also activated: cost threshold for parallelism 5 -> 50` {
		t.Errorf("description %q", d)
	}
	f.MinServerMemoryMB = 25000
	if _, _, err := BuildMinServerMemoryFix(f); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("87%%: %v", err)
	}
	if s, ok := MinServerMemoryReadBack(16); s != "16 MB" || !ok {
		t.Error("16 MB in use is 0 configured")
	}
	if _, ok := MinServerMemoryReadBack(26000); ok {
		t.Error("26000 is not 0")
	}
}

func TestTraceFlagOffFix(t *testing.T) {
	f := TraceFlagFacts{ServerName: `SQL01\WMS`, ProductVersion: "16.0.4255.3", Flags: []int{9999, 1117}, StartupFlags: []int{1117}}
	p, fix, err := BuildTraceFlagOffFix(f, map[string]string{OptionTraceFlag: "1117"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Statement != "DBCC TRACEOFF(1117, -1);" || p.Rollback != "DBCC TRACEON(1117, -1);" ||
		!reflect.DeepEqual(p.Changes, []Drift{{"Trace flag 1117", "on (global)", "off"}}) ||
		p.Warnings[0] != "-T1117 is in the startup parameters, so it comes back at the next restart: copy this for IT to remove it in Configuration Manager." ||
		!strings.HasPrefix(p.Warnings[1], "What trace flag 1117 does: Grows all files") ||
		p.Warnings[len(p.Warnings)-1] != `Needs sysadmin on SQL01\WMS (DBCC TRACEOFF).` {
		t.Errorf("preview %+v", p)
	}
	if !reflect.DeepEqual(p.Inputs[0].Options, []string{"1117", "9999"}) || p.Inputs[0].Value != "1117" || p.Options[OptionTraceFlag] != "1117" {
		t.Errorf("inputs %+v", p.Inputs)
	}
	if fix.ID != FixTraceFlagOff || fix.CheckID != "INS15" || FixChangeDescription(fix, f.ServerName, "") != `Trace flag 1117 on SQL01\WMS: on -> off` {
		t.Errorf("fix %+v", fix)
	}
	// Default: the lowest flag on. An unknown flag, startup parameters not readable.
	f.StartupError = "denied"
	p, _, _ = BuildTraceFlagOffFix(f, map[string]string{OptionTraceFlag: "9999"})
	if p.Warnings[0] != "If the flag is in the startup parameters (-T9999), it comes back at the next restart: copy this for IT to remove it in Configuration Manager." ||
		p.Warnings[1] != "kakapo does not know what trace flag 9999 does: look it up first; the application vendor may need it." {
		t.Errorf("unknown %q", p.Warnings)
	}
	f.StartupError, f.StartupFlags = "", nil
	if p, _, _ := BuildTraceFlagOffFix(f, nil); p.Statement != "DBCC TRACEOFF(1117, -1);" || !strings.HasPrefix(p.Warnings[0], "-T1117 is not in the startup parameters") {
		t.Errorf("default %+v", p)
	}
	// Bad options: nothing to apply, the select says why.
	for _, bad := range []string{"x", "1211", "-1"} {
		p, _, err := BuildTraceFlagOffFix(f, map[string]string{OptionTraceFlag: bad})
		if err != nil || len(p.Changes) != 0 || p.Statement != "" || p.Inputs[0].Warning == "" || p.Inputs[0].Value != bad {
			t.Errorf("flag %q: %+v %v", bad, p, err)
		}
	}
	if _, _, err := BuildTraceFlagOffFix(TraceFlagFacts{}, nil); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("no flags: %v", err)
	}
	if s, ok := TraceFlagOffReadBack(1117, []int{9999}); s != "off" || !ok {
		t.Error("read back off")
	}
	if _, ok := TraceFlagOffReadBack(1117, []int{1117}); ok {
		t.Error("read back still on")
	}
	for arg, want := range map[string]int{"-T1117": 1117, " -t4199 ": 4199, "-dC:\\data\\master.mdf": 0, "-T": 0, "-Tx": 0} {
		if n, ok := ParseStartupFlag(arg); n != want && ok {
			t.Errorf("%q: %d", arg, n)
		} else if want > 0 && !ok {
			t.Errorf("%q not read", arg)
		}
	}
	if TraceFlagOffOffered(3608) || !TraceFlagOffOffered(1117) || !TraceFlagOffOffered(7745) {
		t.Error("offered")
	}
}

func suScopedFacts() ScopedConfigFacts {
	return ScopedConfigFacts{ServerName: `SQL01\WMS`, ProductVersion: "16.0.4255.3", Database: "GraniteLive", ServerMaxDop: 8,
		Configs: []ScopedConfig{{"MAXDOP", "4"}, {"LEGACY_CARDINALITY_ESTIMATION", "1"}, {"PARAMETER_SNIFFING", "1"}, {"ELEVATE_ONLINE", "OFF"}}}
}

func TestScopedConfigResetFix(t *testing.T) {
	f := suScopedFacts()
	p, fix, err := BuildScopedConfigResetFix(f, map[string]string{OptionScopedName: "maxdop"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Statement != "USE [GraniteLive];\nALTER DATABASE SCOPED CONFIGURATION SET MAXDOP = 0;" ||
		p.Rollback != "USE [GraniteLive];\nALTER DATABASE SCOPED CONFIGURATION SET MAXDOP = 4;" ||
		!reflect.DeepEqual(p.Changes, []Drift{{"MAXDOP", "4", "0"}}) ||
		p.Warnings[0] != "Queries in GraniteLive follow the server's MAXDOP (8) again." ||
		p.Warnings[len(p.Warnings)-1] != "Needs the ALTER ANY DATABASE SCOPED CONFIGURATION permission in GraniteLive (db_owner)." {
		t.Errorf("maxdop %+v", p)
	}
	if !reflect.DeepEqual(p.Inputs[0].Options, []string{"LEGACY_CARDINALITY_ESTIMATION", "MAXDOP"}) || p.Options[OptionScopedName] != "MAXDOP" {
		t.Errorf("inputs %+v", p.Inputs)
	}
	if fix.ID != FixScopedConfigReset || fix.CheckID != "DBC14" || FixChangeDescription(fix, f.ServerName, f.Database) != "MAXDOP on GraniteLive: 4 -> 0" {
		t.Errorf("fix %+v", fix)
	}
	// The default name is the first that differs; a flag is written ON/OFF.
	p, _, _ = BuildScopedConfigResetFix(f, nil)
	if p.Statement != "USE [GraniteLive];\nALTER DATABASE SCOPED CONFIGURATION SET LEGACY_CARDINALITY_ESTIMATION = OFF;" ||
		!strings.HasSuffix(p.Rollback, "LEGACY_CARDINALITY_ESTIMATION = ON;") || !strings.Contains(p.Warnings[0], "cardinality estimator") {
		t.Errorf("legacy CE %+v", p)
	}
	// Bad options: a name kakapo does not know, or one at its default.
	for name, warn := range map[string]string{
		"DROP TABLE x":       "kakapo resets only a configuration that differs from its default: choose one of LEGACY_CARDINALITY_ESTIMATION, MAXDOP.",
		"PARAMETER_SNIFFING": "PARAMETER_SNIFFING already has its default value (ON): choose another.",
	} {
		p, _, err := BuildScopedConfigResetFix(f, map[string]string{OptionScopedName: name})
		if err != nil || len(p.Changes) != 0 || p.Statement != "" || p.Inputs[0].Warning != warn || p.Warnings[0] != warn {
			t.Errorf("%q: %+v %v", name, p, err)
		}
	}
	if _, _, err := BuildScopedConfigResetFix(ScopedConfigFacts{Configs: []ScopedConfig{{"MAXDOP", "0"}}, ProductVersion: "16.0.1"}, nil); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("defaults: %v", err)
	}
	after := suScopedFacts()
	after.Configs[0].Value = "0"
	if s, ok := ScopedConfigReadBack("MAXDOP", after); s != "0" || !ok {
		t.Errorf("read back %q", s)
	}
	if s, ok := ScopedConfigReadBack("LEGACY_CARDINALITY_ESTIMATION", after); s != "ON" || ok {
		t.Errorf("read back legacy %q", s)
	}
}

// AvailableFixes lists the actions once per finding, and each finding finds its own entry the way
// the card page matches them: the entry with its fix id whose options name the finding's object.
func TestAvailableSetupActionFixes(t *testing.T) {
	f := goodFacts()
	f.UserAccess = "SINGLE_USER"
	f.TraceFlags = []int{1117, 9999, 3608}
	f.ScopedConfigs[0].Value, f.ScopedConfigs[1].Value = "4", "1"
	f.PendingConfig = append(f.PendingConfig, PendingConfig{Name: "cost threshold for parallelism", Value: 50, ValueInUse: 5})
	f.MinServerMemoryMB = 26000
	_, fs, _ := runSetup(f)
	fixes := AvailableFixes(f, fs)
	count := map[string]int{}
	for _, x := range fixes {
		count[x.ID]++
	}
	if count[FixMultiUser] != 1 || count[FixReconfigure] != 1 || count[FixTraceFlagOff] != 2 || count[FixScopedConfigReset] != 2 || count[FixMinServerMemory] != 1 {
		t.Errorf("fixes %+v", fixes)
	}
	for _, x := range fs {
		if x.FixID != FixTraceFlagOff && x.FixID != FixScopedConfigReset {
			continue
		}
		matches := 0
		for _, fix := range fixes {
			for _, v := range fix.Options {
				if fix.ID == x.FixID && v == x.Object {
					matches++
				}
			}
		}
		if matches != 1 {
			t.Errorf("%s %s: %d entries match", x.CheckID, x.Object, matches)
		}
	}
	for _, fix := range fixes {
		switch {
		case fix.ID == FixTraceFlagOff && fix.Options[OptionTraceFlag] == "1117":
			if fix.Setting != "Trace flag 1117" || fix.Current != "on" || fix.Target != "off" {
				t.Errorf("trace flag fix %+v", fix)
			}
		case fix.ID == FixScopedConfigReset && fix.Options[OptionScopedName] == "MAXDOP":
			if fix.Current != "4" || fix.Target != "0" {
				t.Errorf("scoped fix %+v", fix)
			}
		case fix.ID == FixReconfigure:
			if fix.Options[OptionReconfigureAction] != ReconfigureActivate || fix.Current != "1 pending" {
				t.Errorf("reconfigure fix %+v", fix)
			}
		case fix.ID == FixMultiUser:
			if fix.Current != "SINGLE_USER" || fix.Target != "MULTI_USER" || fix.Scope != ScopeDatabase {
				t.Errorf("multi-user fix %+v", fix)
			}
		case fix.ID == FixMinServerMemory:
			if fix.Current != "26000 MB" || fix.Target != "0 MB" {
				t.Errorf("min memory fix %+v", fix)
			}
		}
	}
}
