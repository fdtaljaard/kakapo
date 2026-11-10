package domain

import (
	"errors"
	"strings"
	"testing"
)

func compatFacts() CompatFacts {
	return CompatFacts{ServerName: "SQL1", ProductVersion: "15.0.4385.2", Database: "Granite", CompatLevel: 130,
		QueryStoreState: "READ_WRITE", QueryStoreDays: 30.5}
}

func TestCompatLevelFix(t *testing.T) {
	p, fix, err := BuildCompatLevelFix(compatFacts(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Statement != "ALTER DATABASE [Granite] SET COMPATIBILITY_LEVEL = 150;" || p.Rollback != "ALTER DATABASE [Granite] SET COMPATIBILITY_LEVEL = 130;" ||
		fix.Current != "130" || fix.Target != "150" || len(p.Changes) != 1 || p.Inputs[0].Value != "150" ||
		strings.Join(p.Inputs[0].Options, ",") != "140,150" {
		t.Fatalf("preview %+v fix %+v", p, fix)
	}
	all := strings.Join(p.Warnings, "\n")
	for _, want := range []string{"Query Store holds 30 days of history", "at 140, interleaved execution", "at 150, scalar UDF inlining",
		"quiet hour", "COMPATIBILITY_LEVEL = 130) and takes effect at once", "force its old plan"} {
		if !strings.Contains(all, want) {
			t.Errorf("warnings miss %q:\n%s", want, all)
		}
	}

	// One step at a time.
	p, fix, _ = BuildCompatLevelFix(compatFacts(), map[string]string{OptionCompatTarget: "140"})
	if p.Statement != "ALTER DATABASE [Granite] SET COMPATIBILITY_LEVEL = 140;" || fix.Target != "140" || strings.Contains(strings.Join(p.Warnings, " "), "at 150") {
		t.Errorf("140: %+v", p)
	}
	// A level the server does not offer: no statement, so Apply stays off.
	if p, _, _ = BuildCompatLevelFix(compatFacts(), map[string]string{OptionCompatTarget: "160"}); p.Statement != "" || p.Inputs[0].Warning == "" {
		t.Errorf("160: %+v", p)
	}

	// Query Store off, and too young.
	f := compatFacts()
	f.QueryStoreState = "OFF"
	if p, _, _ = BuildCompatLevelFix(f, nil); !strings.HasPrefix(p.Warnings[0], "Query Store is off, so nothing records") {
		t.Errorf("off: %q", p.Warnings[0])
	}
	f.QueryStoreState, f.QueryStoreDays = "READ_WRITE", 2.4
	if p, _, _ = BuildCompatLevelFix(f, nil); !strings.HasPrefix(p.Warnings[0], "Query Store holds only 2 days of history") {
		t.Errorf("young: %q", p.Warnings[0])
	}
	// Below 120 the cardinality estimator changes too.
	f.CompatLevel = 110
	if p, _, _ = BuildCompatLevelFix(f, nil); !strings.Contains(strings.Join(p.Warnings, " "), "at 120, the newer cardinality estimator") {
		t.Errorf("110: %v", p.Warnings)
	}

	// Already at the server's level.
	f.CompatLevel = 150
	if _, _, err := BuildCompatLevelFix(f, nil); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("current: %v", err)
	}
	if v, ok := CompatReadBack(CompatFacts{CompatLevel: 150}, "150"); v != "150" || !ok {
		t.Errorf("read back %s %v", v, ok)
	}
}
