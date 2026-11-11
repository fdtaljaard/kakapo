package domain

import (
	"strings"
	"testing"
)

func batchChosen(names ...string) []BatchIndex {
	var out []BatchIndex
	for _, n := range names {
		out = append(out, BatchIndex{Schema: "dbo", Table: "Lines", Index: n})
	}
	return out
}

func TestBatchIndexesOption(t *testing.T) {
	list := []BatchIndex{{"dbo", "Lines", "IX_A"}, {"sales", "Or]ders", "IX B"}}
	got, err := ParseBatchIndexes(BatchIndexesOption(list) + "\n\n" + "[dbo].[Lines]\tIX_A\r\n")
	if err != nil || len(got) != 2 || got[0] != list[0] || got[1] != list[1] {
		t.Fatalf("round trip %v %v", got, err)
	}
	for _, bad := range []string{"[dbo].[Lines]", "[dbo].[Lines]\t ", "[a].[b].[c].[d]\tIX"} {
		if _, err := ParseBatchIndexes(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if BatchMaxGB(nil) != 20 || BatchMaxGB(map[string]string{OptionMaxGB: "0"}) != 0 || BatchMaxGB(map[string]string{OptionMaxGB: "-3"}) != 20 || BatchMaxGB(map[string]string{OptionMaxGB: "50"}) != 50 {
		t.Error("maxGB")
	}
}

func batchFacts(recovery string, pages ...int64) BatchFacts {
	f := BatchFacts{ProductVersion: "16.0.4255.3", Recovery: recovery}
	for i, p := range pages {
		f.Basics = append(f.Basics, lensBasics("dbo", "Lines", []string{"IX_A", "IX_B", "IX_C"}[i], 2, p))
	}
	return f
}

func TestBuildReorganizeBatchFix(t *testing.T) {
	pv, fix := BuildReorganizeBatchFix("Shop", batchChosen("IX_A", "IX_B"), batchFacts("FULL", 12400, 3000), nil)
	if fix.ID != FixReorganizeIndexes || fix.CheckID != "QRY01" || len(pv.Changes) != 2 || pv.Rollback != "" || pv.Database != "Shop" {
		t.Fatalf("fix %+v preview %+v", fix, pv)
	}
	if pv.Changes[0].Setting != "dbo.Lines · IX_A" || pv.Changes[0].Current != "12,400 pages" {
		t.Errorf("change %+v", pv.Changes[0])
	}
	// The statements, in the order chosen, each named by a comment.
	want := "-- dbo.Lines · IX_A\nALTER INDEX [IX_A] ON [Shop].[dbo].[Lines] REORGANIZE;\n-- dbo.Lines · IX_B\nALTER INDEX [IX_B] ON [Shop].[dbo].[Lines] REORGANIZE;"
	if pv.Statement != want {
		t.Errorf("statement:\n%s", pv.Statement)
	}
	if !strings.Contains(pv.Lead, "2 indexes to reorganise, one after another: 15,440 pages") {
		t.Errorf("lead %q", pv.Lead)
	}
	all := strings.ToLower(strings.Join(pv.Warnings, "\n"))
	for _, w := range []string{"online on every edition", "stop the run at any time", "if one index fails", "in full recovery", "free space in the data file",
		"does not update statistics", "kakapo must stay open", "own row in the change log", "alter permission"} {
		if !strings.Contains(all, w) {
			t.Errorf("no warning with %q in:\n%s", w, all)
		}
	}
	if strings.Contains(all, "more than the 20 gb") {
		t.Error("the strong sentence under the bound")
	}
	if len(pv.Inputs) != 1 || pv.Inputs[0].Name != OptionMaxGB || !pv.Inputs[0].Advanced || pv.Options[OptionMaxGB] != "20" || pv.Options[OptionIndexes] == "" {
		t.Errorf("inputs %+v options %v", pv.Inputs, pv.Options)
	}
}

func TestBatchWarnings(t *testing.T) {
	// 3 million pages is 23 GB: over the 20 GB bound; a larger bound or 0 silences it.
	pv, _ := BuildReorganizeBatchFix("Shop", batchChosen("IX_A"), batchFacts("FULL", 3_000_000), nil)
	if all := strings.Join(pv.Warnings, "\n"); !strings.Contains(all, "more than the 20 GB kakapo starts a batch with") {
		t.Errorf("no strong sentence:\n%s", all)
	}
	for _, v := range []string{"0", "30"} {
		pv, _ = BuildReorganizeBatchFix("Shop", batchChosen("IX_A"), batchFacts("FULL", 3_000_000), map[string]string{OptionMaxGB: v})
		if strings.Contains(strings.Join(pv.Warnings, "\n"), "more than the") {
			t.Errorf("maxGB %s still warns", v)
		}
	}
	// SIMPLE recovery says the log empties at the checkpoint, not the FULL sentence.
	pv, _ = BuildReorganizeBatchFix("Shop", batchChosen("IX_A"), batchFacts("SIMPLE", 5000), nil)
	if all := strings.Join(pv.Warnings, "\n"); strings.Contains(all, "In FULL recovery") || !strings.Contains(all, "SIMPLE recovery") {
		t.Errorf("SIMPLE:\n%s", all)
	}
	// Stale tables are named.
	f := batchFacts("FULL", 5000)
	f.StaleTables = []string{"dbo.Lines"}
	pv, _ = BuildReorganizeBatchFix("Shop", batchChosen("IX_A"), f, nil)
	if all := strings.Join(pv.Warnings, "\n"); !strings.Contains(all, "Old statistics: dbo.Lines.") {
		t.Errorf("stale:\n%s", all)
	}
}

func TestBatchBlocksAndLeavesOut(t *testing.T) {
	// Nothing chosen: blocked.
	pv, _ := BuildReorganizeBatchFix("Shop", nil, BatchFacts{}, nil)
	if len(pv.Changes) != 0 || pv.Statement != "" || len(pv.Warnings) != 1 || pv.Warnings[0] != "Tick at least one index." {
		t.Errorf("empty %+v", pv)
	}
	// One that cannot be reorganised is left out and named; the rest runs.
	f := batchFacts("FULL", 12400, 3000, 4000)
	f.Basics[1].PageLocks = false
	f.Basics[2].Disabled = true
	pv, _ = BuildReorganizeBatchFix("Shop", batchChosen("IX_A", "IX_B", "IX_C", "IX_Gone"), f, nil)
	all := strings.Join(pv.Warnings, "\n")
	if len(pv.Changes) != 1 || strings.Contains(pv.Statement, "IX_B") || strings.Contains(pv.Statement, "IX_C") ||
		!strings.Contains(all, "Left out: IX_B has ALLOW_PAGE_LOCKS OFF") || !strings.Contains(all, "Left out: IX_C is disabled") ||
		!strings.Contains(all, "Left out: There is no table dbo.Lines in Shop") {
		t.Errorf("preview %+v\n%s", pv.Changes, all)
	}
	// All left out: blocked, the reasons shown.
	f = batchFacts("FULL", 3000)
	f.Basics[0].PageLocks = false
	pv, _ = BuildReorganizeBatchFix("Shop", batchChosen("IX_A"), f, nil)
	if len(pv.Changes) != 0 || pv.Statement != "" || len(pv.Warnings) != 1 || !strings.HasPrefix(pv.Warnings[0], "Left out:") {
		t.Errorf("all left out: %+v", pv)
	}
}

func TestBatchSummaryAndSkip(t *testing.T) {
	items := []RunItem{{State: ItemDone}, {State: ItemDone}, {State: ItemSkipped}, {State: ItemFailed}}
	if s, ok := BatchSummary(items); s != "2 reorganised, 1 skipped, 1 failed" || ok {
		t.Errorf("summary %q %v", s, ok)
	}
	if s, ok := BatchSummary([]RunItem{{State: ItemDone}, {State: ItemSkipped}}); s != "1 reorganised, 1 skipped" || !ok {
		t.Errorf("summary %q %v", s, ok)
	}
	if s, ok := BatchSummary([]RunItem{{State: ItemDone}, {State: ItemStopped}, {State: ItemStopped}}); s != "1 reorganised, 2 not done (stopped)" || ok {
		t.Errorf("stopped %q %v", s, ok)
	}
	if s, ok := BatchSummary(nil); s != "nothing was reorganised" || ok {
		t.Errorf("none %q %v", s, ok)
	}
	f := shapeFacts()
	f.Pages = 999
	if ReorganizeSkip(f) == "" {
		t.Error("999 pages not skipped")
	}
	f = shapeFacts() // 12,400 pages, 58% full, 41% out of order
	if ReorganizeSkip(f) != "" {
		t.Error("a measured gain skipped")
	}
	f.Density, f.Fragmentation = 100, 4.9
	if why := ReorganizeSkip(f); !strings.Contains(why, "already full and in order") {
		t.Errorf("in order: %q", why)
	}
	f.Fragmentation = 5 // out of order enough: not skipped
	if ReorganizeSkip(f) != "" {
		t.Error("5% out of order skipped")
	}
}

func TestIndexShapeGain(t *testing.T) {
	f := shapeFacts() // 58% full: saves 42%
	if x := BuildIndexShape(f, true); !x.Gain || x.Action == nil {
		t.Errorf("gain %+v", x)
	}
	f.Density = 96 // saves 4%: out of order but about as full as it can be
	if x := BuildIndexShape(f, true); x.Gain {
		t.Errorf("4%% saving is a gain: %+v", x)
	}
	f.Density = 95 // saves 5%
	if x := BuildIndexShape(f, true); !x.Gain {
		t.Errorf("5%% saving is not a gain: %+v", x)
	}
	f.Pages = 999
	if x := BuildIndexShape(f, true); x.Gain {
		t.Error("under the floor is a gain")
	}
}
