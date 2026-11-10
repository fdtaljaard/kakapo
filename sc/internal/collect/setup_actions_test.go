package collect

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"sqlmonitor/internal/domain"
)

// suFixVerbs are what only a fix statement may hold: the actions' reads must not.
var suFixVerbs = []string{"SP_ADD_", "SP_DELETE_", "SP_UPDATE_", "RECONFIGURE", "TRACEOFF", "TRACEON", "ALTER ", "KILL ", "SP_CONFIGURE"}

// suReadOnly checks one read of the actions: tagged once with the safe session first, writing
// nothing but its table variables, and holding no fix statement. DBCC TRACESTATUS only reports.
func suReadOnly(t *testing.T, name, q string) {
	t.Helper()
	if !strings.HasPrefix(q, KakapoTag+" "+safeSession) || strings.HasPrefix(q, KakapoTag+" "+KakapoTag) {
		t.Errorf("%s query is not tagged once with the safe session first: %.80s", name, q)
	}
	readOnly(t, name, strings.ReplaceAll(q, "DBCC TRACESTATUS(-1)", "TRACESTATUS(-1)"))
	upper := strings.ToUpper(likeLiterals.ReplaceAllString(q, "LIKE N''"))
	for _, verb := range suFixVerbs {
		if strings.Contains(upper, verb) {
			t.Errorf("%s query contains %q, which belongs in a fix statement", name, verb)
		}
	}
}

// The Setup actions' reads are tagged and read-only (docs/phase4d-contract.md, Tests), each area that
// needs a permission in its own TRY with an error column.
func TestSetupActionReads(t *testing.T) {
	for name, q := range map[string]string{"user access": Tag(userAccessQuery), "server config": Tag(serverConfigQuery),
		"trace flags": Tag(traceFlagQuery), "scoped configurations": scopedConfigBatch("Warehouse]Live"), "compatibility level": compatBatch("Ware'house]Live")} {
		suReadOnly(t, name, q)
	}
	if n := strings.Count(traceFlagQuery, "BEGIN TRY"); n != 2 || !strings.Contains(traceFlagQuery, "SELECT @tfError, @argsError") ||
		!strings.Contains(traceFlagQuery, "sys.dm_server_registry WHERE value_name LIKE N'SQLArg%'") || !strings.Contains(traceFlagQuery, "[global] = 1") {
		t.Errorf("trace flag query: %d TRY blocks\n%s", n, traceFlagQuery)
	}
	if n := strings.Count(userAccessQuery, "BEGIN TRY"); n != 1 || !strings.Contains(userAccessQuery, "@sessionsError") ||
		!strings.Contains(userAccessQuery, "s.session_id <> @@SPID") || !strings.Contains(userAccessQuery, "user_access_desc") {
		t.Errorf("user access query\n%s", userAccessQuery)
	}
	if !strings.Contains(serverConfigQuery, "WHERE value <> value_in_use") || !strings.Contains(serverConfigQuery, "is_dynamic") ||
		!strings.Contains(serverConfigQuery, "is_advanced") {
		t.Errorf("server config query\n%s", serverConfigQuery)
	}
	if b := scopedConfigBatch("Warehouse]Live"); !strings.Contains(b, "EXEC [Warehouse]]Live].sys.sp_executesql N'/* kakapo */ SELECT name, CONVERT(nvarchar(400), value) FROM sys.database_scoped_configurations ORDER BY name;';") {
		t.Errorf("scoped batch\n%s", b)
	}
}

// TestCompatLevelLive raises and lowers a scratch database's compatibility level, when
// SQLMONITOR_TEST_HOST (Windows authentication) and SQLMONITOR_TEST_COMPAT_DB name one made for it: the
// preview, the statement, the read-back and the rollback.
func TestCompatLevelLive(t *testing.T) {
	host, dbName := os.Getenv("SQLMONITOR_TEST_HOST"), os.Getenv("SQLMONITOR_TEST_COMPAT_DB")
	if host == "" || dbName == "" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_COMPAT_DB (a scratch database) to run")
	}
	db, err := sql.Open("sqlserver", ConnString(domain.ServerInput{Host: host, Auth: domain.AuthWindows}))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	f, err := ReadCompatFacts(ctx, db, dbName)
	if err != nil {
		t.Fatal(err)
	}
	p, fix, err := domain.BuildCompatLevelFix(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("level %d, Query Store %s, %.2f days; %s; first warning: %s", f.CompatLevel, f.QueryStoreState, f.QueryStoreDays, p.Statement, p.Warnings[0])
	if err := ExecFix(ctx, db, p.Statement); err != nil {
		t.Fatal(err)
	}
	after, _ := ReadCompatFacts(ctx, db, dbName)
	if v, ok := domain.CompatReadBack(after, fix.Target); !ok {
		t.Errorf("read back %s, want %s", v, fix.Target)
	}
	if err := ExecFix(ctx, db, p.Rollback); err != nil {
		t.Fatal(err)
	}
	if back, _ := ReadCompatFacts(ctx, db, dbName); back.CompatLevel != f.CompatLevel {
		t.Errorf("rollback left %d", back.CompatLevel)
	}
}
