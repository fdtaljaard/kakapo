package domain

import (
	"strings"
	"testing"
)

// A plan with a clustered index scan, an index seek, a key lookup and a heap's table scan.
const scanPlan = `<ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan"><BatchSequence><Batch><Statements><StmtSimple><QueryPlan>
<RelOp PhysicalOp="Nested Loops" LogicalOp="Inner Join">
  <NestedLoops>
    <RelOp PhysicalOp="Clustered Index Scan" LogicalOp="Clustered Index Scan">
      <IndexScan><Object Database="[Shop]" Schema="[dbo]" Table="[Orders]" Index="[PK_Orders]" /></IndexScan>
    </RelOp>
    <RelOp PhysicalOp="Index Seek" LogicalOp="Index Seek">
      <IndexScan><Object Database="[Shop]" Schema="[dbo]" Table="[Lines]" Index="[IX_Lines_Order]" /></IndexScan>
    </RelOp>
    <RelOp PhysicalOp="Clustered Index Seek" LogicalOp="Clustered Index Seek">
      <IndexScan Lookup="1"><Object Database="[Shop]" Schema="[dbo]" Table="[Lines]" Index="[PK_Lines]" /></IndexScan>
    </RelOp>
    <RelOp PhysicalOp="Index Scan" LogicalOp="Index Scan">
      <IndexScan><Object Database="[Shop]" Schema="[dbo]" Table="[Lines]" Index="[IX_Lines_Date]" /></IndexScan>
    </RelOp>
    <RelOp PhysicalOp="Table Scan" LogicalOp="Table Scan">
      <TableScan><Object Database="[Shop]" Schema="[dbo]" Table="[Log]" /></TableScan>
    </RelOp>
  </NestedLoops>
</RelOp></QueryPlan></StmtSimple></Statements></Batch></BatchSequence></ShowPlanXML>`

// The same index sought in one plan and scanned in another is scanned.
const seekPlan = `<ShowPlanXML xmlns="http://schemas.microsoft.com/sqlserver/2004/07/showplan"><BatchSequence><Batch><Statements><StmtSimple><QueryPlan>
<RelOp PhysicalOp="Index Seek" LogicalOp="Index Seek">
  <IndexScan><Object Database="[Shop]" Schema="[dbo]" Table="[Lines]" Index="[IX_Lines_Date]" /></IndexScan>
</RelOp></QueryPlan></StmtSimple></Statements></Batch></BatchSequence></ShowPlanXML>`

func TestShowplanScan(t *testing.T) {
	scan := map[string]bool{}
	for _, o := range ParseShowplan(scanPlan).Objects {
		scan[o.Table+"."+o.Index] = o.Scan
	}
	want := map[string]bool{"Orders.PK_Orders": true, "Lines.IX_Lines_Order": false, "Lines.PK_Lines": false, "Lines.IX_Lines_Date": true, "Log.": false}
	for k, v := range want {
		if got, ok := scan[k]; !ok || got != v {
			t.Errorf("%s: scan %v (found %v), want %v", k, got, ok, v)
		}
	}
	tables := ShowplanTables("Shop", seekPlan, scanPlan)
	got := map[string]string{}
	for _, o := range tables {
		got[o.Table] = o.Scanned
	}
	if got["Orders"] != "PK_Orders" || got["Lines"] != "IX_Lines_Date" || got["Log"] != "" {
		t.Errorf("scanned %q", got)
	}
}

func shapeFacts() IndexShapeFacts {
	return IndexShapeFacts{ProductVersion: "16.0.4255.3", EngineEdition: 2, Database: "Shop", Recovery: "FULL", Schema: "dbo", Table: "Lines",
		Index: "IX_Lines_Date", TableFound: true, IndexFound: true, Type: 2, PageLocks: true, Pages: 12400, Density: 58, Fragmentation: 41, SizeMB: 97}
}

func TestIndexShapeEstimate(t *testing.T) {
	f := shapeFacts()
	if f.EstimatedPages() != 7192 || roundHalfAway(f.SavedPct(), 1) != 42 {
		t.Errorf("estimate %d, saved %v", f.EstimatedPages(), f.SavedPct())
	}
	f.FillFactor = 80 // packs to 80% full: 12,400 × 58 ÷ 80
	if f.EstimatedPages() != 8990 {
		t.Errorf("fill factor 80: %d", f.EstimatedPages())
	}
	f.Density = 99 // never more pages than now
	if f.EstimatedPages() != f.Pages {
		t.Errorf("full at fill factor 80: %d", f.EstimatedPages())
	}
}

func TestBuildIndexShape(t *testing.T) {
	x := BuildIndexShape(shapeFacts(), true)
	want := "The plan reads all of IX_Lines_Date (12,400 pages, 97 MB). Its pages are 58% full and 41% out of order. Reorganising it would pack it into about 7,192 pages, so each scan would read about 42% fewer."
	if !x.Measured || x.Text != want || x.Action == nil || x.Action.FixID != FixReorganizeIndex ||
		x.Action.Options[OptionTable] != "[dbo].[Lines]" || x.Action.Options[OptionIndex] != "IX_Lines_Date" {
		t.Errorf("%+v\n%s", x, x.Text)
	}
	if x := BuildIndexShape(shapeFacts(), false); x.Action != nil {
		t.Error("an action on a database that cannot be changed")
	}
	small := shapeFacts()
	small.Pages = 400
	if x := BuildIndexShape(small, true); x.Action != nil || !strings.Contains(x.Text, "under 1,000 pages") {
		t.Errorf("small: %s", x.Text)
	}
	tidy := shapeFacts()
	tidy.Density, tidy.Fragmentation = 97, 2
	if x := BuildIndexShape(tidy, true); x.Action != nil || !strings.HasSuffix(x.Text, "Index work would not help this query.") {
		t.Errorf("tidy: %s", x.Text)
	}
	// Full pages but out of order: offered, without a page estimate.
	order := shapeFacts()
	order.Density, order.Fragmentation = 98, 60
	if x := BuildIndexShape(order, true); x.Action == nil || !strings.Contains(x.Text, "put its pages back in order") {
		t.Errorf("out of order: %s", x.Text)
	}
	for name, f := range map[string]IndexShapeFacts{
		"error":       func() IndexShapeFacts { f := shapeFacts(); f.Error = "VIEW DATABASE STATE permission denied"; return f }(),
		"no table":    func() IndexShapeFacts { f := shapeFacts(); f.TableFound, f.IndexFound = false, false; return f }(),
		"no index":    func() IndexShapeFacts { f := shapeFacts(); f.IndexFound = false; return f }(),
		"disabled":    func() IndexShapeFacts { f := shapeFacts(); f.Disabled = true; return f }(),
		"columnstore": func() IndexShapeFacts { f := shapeFacts(); f.Type = 6; return f }(),
	} {
		if x := BuildIndexShape(f, true); x.Measured || x.Action != nil || x.Text == "" {
			t.Errorf("%s: %+v", name, x)
		}
	}
}

func TestBuildReorganizeIndexFix(t *testing.T) {
	pv, fix := BuildReorganizeIndexFix(shapeFacts(), ReorganizeIndexOptions("dbo", "Lines", "IX_Lines_Date"))
	if pv.Statement != "ALTER INDEX [IX_Lines_Date] ON [Shop].[dbo].[Lines] REORGANIZE;" || pv.Rollback != "" || len(pv.Changes) != 1 ||
		strings.Contains(pv.Statement, "TRAN") {
		t.Errorf("statement %q changes %+v", pv.Statement, pv.Changes)
	}
	if fix.ID != FixReorganizeIndex || fix.Current != "12,400 pages, 58% full, 41% out of order" {
		t.Errorf("fix %+v", fix)
	}
	joined := strings.Join(pv.Warnings, "\n")
	for _, w := range []string{reorgOnline, reorgNoStats, "the next log backup carries roughly 97 MB more", reorgSpace, "Needs the ALTER permission on dbo.Lines"} {
		if !strings.Contains(joined, w) {
			t.Errorf("missing warning %q in\n%s", w, joined)
		}
	}
	simple := shapeFacts()
	simple.Recovery = "SIMPLE"
	if pv, _ := BuildReorganizeIndexFix(simple, nil); !strings.Contains(strings.Join(pv.Warnings, "\n"), "in SIMPLE recovery the log empties") {
		t.Errorf("simple: %q", pv.Warnings)
	}
	for name, f := range map[string]IndexShapeFacts{
		"no page locks": func() IndexShapeFacts { f := shapeFacts(); f.PageLocks = false; return f }(),
		"disabled":      func() IndexShapeFacts { f := shapeFacts(); f.Disabled = true; return f }(),
		"columnstore":   func() IndexShapeFacts { f := shapeFacts(); f.Type = 5; return f }(),
		"no index":      func() IndexShapeFacts { f := shapeFacts(); f.IndexFound = false; return f }(),
		"error":         func() IndexShapeFacts { f := shapeFacts(); f.Error = "lock timeout"; return f }(),
	} {
		if pv, _ := BuildReorganizeIndexFix(f, nil); pv.Statement != "" || len(pv.Changes) != 0 || len(pv.Warnings) != 1 {
			t.Errorf("%s: %+v", name, pv)
		}
	}
}

func TestReorganizeIndexReadBack(t *testing.T) {
	before := shapeFacts()
	after := shapeFacts()
	after.Pages, after.Fragmentation, after.Density = 7250, 1, 99
	text, ok := ReorganizeIndexReadBack(before, after)
	if !ok || text != "IX_Lines_Date: 12,400 pages to 7,250; 41% out of order to 1%, 58% full to 99%." {
		t.Errorf("%v %s", ok, text)
	}
	if _, ok := ReorganizeIndexReadBack(before, before); ok {
		t.Error("unchanged verified")
	}
	if text, ok := ReorganizeIndexDone(after); !ok || text != "IX_Lines_Date: 7,250 pages, 1% out of order, 99% full." {
		t.Errorf("done %v %s", ok, text)
	}
}
