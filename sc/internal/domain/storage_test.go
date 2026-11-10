package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var storageDef, _ = FindCardDef(CardStorage)

func i64(n int64) *int64     { return &n }
func f64(x float64) *float64 { return &x }
func intp(n int) *int        { return &n }

// sfacts is a healthy database: data on D:\ (100 GB, half free), log on L:\, tempdb with 8 equal
// files on T:\, 8 CPUs, 100 VLFs, tempdb 12% full, 3 GB of tables, quick disks.
func sfacts() StorageFacts {
	f := StorageFacts{Database: "WarehouseLive", State: "ONLINE", RecoveryModel: "FULL", LogReuseWait: "NOTHING",
		EngineEdition: 3, EngineVersion: "16.0.1000.6", CPUCount: 8, VLFCount: intp(100),
		TempdbAllocMB: i64(1024), TempdbFreeMB: i64(900), TempdbLogUsedPct: f64(5), LogSizeMB: i64(1024), LogUsedPct: f64(10),
		TableCount: 120, TotalDataMB: 2048, TotalIndexMB: 1024,
		Tables: []TableSize{
			{Name: "[dbo].[Transaction]", DataMB: 800, IndexMB: 400.4, IndexCount: 3, Rows: 1234567},
			{Name: "[dbo].[SalesOrder]", DataMB: 100, IndexMB: 20, IndexCount: 2, Rows: 5000},
		},
		GrowingTables: []GrowingTable{}, Identities: []IdentityColumn{},
		ServerNow: serverNow, UTCOffsetMinutes: 120}
	f.Files = []StorageFile{
		{DatabaseID: 7, Database: "WarehouseLive", FileID: 1, Type: "ROWS", LogicalName: "WarehouseLive", PhysicalName: `D:\Data\WarehouseLive.mdf`,
			SizeMB: 4096, Growth: 32768, MaxSizePages: -1, UsedMB: i64(3500), Volume: `D:\`, VolumeTotalMB: 102400, VolumeFreeMB: 51200,
			HasIO: true, Reads: 5000, Writes: 100, ReadStallMs: 25000, WriteStallMs: 100},
		{DatabaseID: 7, Database: "WarehouseLive", FileID: 2, Type: "LOG", LogicalName: "WarehouseLive_log", PhysicalName: `L:\Log\WarehouseLive_log.ldf`,
			SizeMB: 1024, Growth: 32768, MaxSizePages: 268435456, UsedMB: i64(100), Volume: `L:\`, VolumeTotalMB: 51200, VolumeFreeMB: 40960,
			HasIO: true, Reads: 10, Writes: 8000, ReadStallMs: 10, WriteStallMs: 8000},
	}
	for i := 1; i <= 8; i++ {
		f.Files = append(f.Files, StorageFile{DatabaseID: 2, Database: "tempdb", FileID: i + 2, Type: "ROWS",
			LogicalName: "temp" + string(rune('0'+i)), PhysicalName: `T:\TempDB\temp` + string(rune('0'+i)) + ".ndf",
			SizeMB: 128, Growth: 8192, MaxSizePages: -1, Volume: `T:\`, VolumeTotalMB: 20480, VolumeFreeMB: 10240})
	}
	f.Files = append(f.Files, StorageFile{DatabaseID: 2, Database: "tempdb", FileID: 2, Type: "LOG", LogicalName: "templog",
		PhysicalName: `T:\TempDB\templog.ldf`, SizeMB: 256, Growth: 8192, MaxSizePages: -1, Volume: `T:\`, VolumeTotalMB: 20480, VolumeFreeMB: 10240})
	return f
}

func sbuild(f StorageFacts) (Card, []Finding, []CheckStatus) {
	return BuildStorageCard(f, storageDef, nil, nil)
}

// file returns a pointer to the file with this logical name, to change it in a test.
func file(f *StorageFacts, logical string) *StorageFile {
	for i := range f.Files {
		if f.Files[i].LogicalName == logical {
			return &f.Files[i]
		}
	}
	panic("no file " + logical)
}

func allOf(fs []Finding, id string) []Finding {
	out := []Finding{}
	for _, x := range fs {
		if x.CheckID == id {
			out = append(out, x)
		}
	}
	return out
}

func wantStorageFinding(t *testing.T, f Finding, severity Severity, when, effort, object, finding string) {
	t.Helper()
	if f.Severity != severity || f.When != when || f.Effort != effort || f.Object != object || f.Finding != finding {
		t.Errorf("%s: %s %s %s %q %q\nwant %s %s %s %q %q", f.CheckID, f.Severity, f.When, f.Effort, f.Object, f.Finding,
			severity, when, effort, object, finding)
	}
	chk, _ := CheckByID(f.CheckID)
	if f.Owner != chk.Owner || f.Title != chk.Title || f.ThenCheck != chk.ThenCheck || f.Why == "" || f.Evidence == "" {
		t.Errorf("%s: incomplete finding %+v", f.CheckID, f)
	}
	if f.DoThis == "" && f.CheckID != "IDX01" {
		t.Errorf("%s: no Do this", f.CheckID)
	}
}

func checkOf(checks []CheckStatus, id string) CheckStatus {
	for _, c := range checks {
		if c.CheckID == id {
			return c
		}
	}
	return CheckStatus{}
}

func factOf(c Card, label string) Fact {
	for _, x := range c.Facts {
		if x.Label == label {
			return x
		}
	}
	return Fact{}
}

func TestStorageCatalogue(t *testing.T) {
	ids := []string{}
	for _, c := range ChecksFor(CardStorage) {
		ids = append(ids, c.ID)
		if c.Title == "" || c.Question == "" || c.ThenCheck == "" {
			t.Errorf("incomplete check %+v", c)
		}
		clientIT := map[string]bool{"STO01": true, "STO05": true, "STO06": true, "STO09": true, "STO10": true, "STO12": true}[c.ID]
		vendor := c.ID == "IDX10" // the application's owner decides what may be deleted
		if (c.Owner == OwnerClientIT) != clientIT || (c.Owner == OwnerVendor) != vendor ||
			(c.Owner != OwnerClientIT && c.Owner != OwnerUs && c.Owner != OwnerVendor) {
			t.Errorf("%s owner %q", c.ID, c.Owner)
		}
	}
	want := "STO01,STO02,STO03,STO04,STO05,STO06,STO07,STO08,STO09,TMP01,TMP02,TMP03,IDX01,IDX06,IDX07,IDX10,IDX13,STO10,STO11,STO12"
	if strings.Join(ids, ",") != want {
		t.Errorf("storage checks %v", ids)
	}
}

func TestStorageGreen(t *testing.T) {
	c, fs, checks := sbuild(sfacts())
	if len(fs) != 1 || fs[0].CheckID != "IDX01" {
		t.Fatalf("findings %+v", fs)
	}
	wantStorageFinding(t, fs[0], SeverityInfo, WhenNone, EffortNone, "WarehouseLive",
		"The biggest table, [dbo].[Transaction], has 1,200 MB in total: 800 MB data and 400 MB in 3 nonclustered indexes.")
	if !strings.HasPrefix(fs[0].Evidence, "Top 2: [dbo].[Transaction]: 1,200 MB total, 800 MB data, 400 MB index, 1,234,567 rows; [dbo].[SalesOrder]: 120 MB total") ||
		!strings.HasSuffix(fs[0].Evidence, "All 120 tables: 2,048 MB data, 1,024 MB indexes.") {
		t.Errorf("IDX01 evidence %q", fs[0].Evidence)
	}
	if c.Status != StatusGreen || c.StatusText != "No problem found" || c.ToDo != 0 || c.ForClientIT != 0 || c.Fixable != 0 {
		t.Errorf("card %+v", c)
	}
	if c.Verdict != `WarehouseLive is 3.0 GB on D:\, which has 50 GB free.` {
		t.Errorf("verdict %q", c.Verdict)
	}
	if c.AsOf == nil || !c.AsOf.Equal(time.Date(2026, 11, 3, 7, 21, 0, 0, time.UTC)) {
		t.Errorf("asOf %v", c.AsOf)
	}
	wantFacts(t, c, `Tightest drive=D:\ 50% free (50 GB)/normal`, "Database size=4.0 GB allocated, 3.4 GB used/normal",
		"Log=1.0 GB, 10% used/normal", "TempDB=12% full of 1.0 GB/normal")
	for _, s := range checks {
		want := CheckPassed
		if s.CheckID == "STO10" || s.CheckID == "STO12" {
			want = CheckSkipped
		}
		if s.Status != want {
			t.Errorf("check %+v", s)
		}
	}
	if n := checkOf(checks, "STO10").Note; n != Sto10NotRun {
		t.Errorf("STO10 note %q", n)
	}
	if n := checkOf(checks, "STO12").Note; n != "Needs samples a day or more apart." {
		t.Errorf("STO12 note without samples %q", n)
	}
}

func TestStorageSTO01(t *testing.T) {
	for _, c := range []struct {
		total, free int64
		want        Severity // "" when it does not fire
		pctFull     string
		tone        string
	}{
		{102400, 20480, "", "", ToneNormal},              // exactly 20% and 20 GB
		{102400, 20479, SeverityMedium, "80", ToneBad},   // just under 20%
		{102400, 10240, SeverityMedium, "90", ToneBad},   // exactly 10%
		{102400, 10239, SeverityHigh, "90", ToneWorse},   // under 10%
		{20000, 5119, SeverityHigh, "74", ToneWorse},     // 25% free but under 5 GB
		{20000, 5120, "", "", ToneNormal},                // 25% free and 5 GB
		{1024000, 204800, "", "", ToneNormal},            // 20% of a big disk
		{1024000, 204799, SeverityMedium, "80", ToneBad}, // just under 20% of a big disk
	} {
		f := sfacts()
		d := file(&f, "WarehouseLive")
		d.VolumeTotalMB, d.VolumeFreeMB = c.total, c.free
		card, fs, _ := sbuild(f)
		got := allOf(fs, "STO01")
		if c.want == "" {
			if len(got) != 0 {
				t.Errorf("%d/%d: unexpected %+v", c.free, c.total, got)
			}
			continue
		}
		if len(got) != 1 {
			t.Fatalf("%d/%d: STO01 %+v", c.free, c.total, got)
		}
		wantStorageFinding(t, got[0], c.want, WhenOnline, EffortModerate, `D:\`, `Volume D:\ is `+c.pctFull+`% full.`)
		if got[0].Owner != OwnerClientIT || card.ForClientIT != 1 {
			t.Errorf("STO01 is for Client IT: %+v %+v", got[0], card)
		}
		if tf := factOf(card, "Tightest drive"); tf.Tone != c.tone {
			t.Errorf("%d/%d: tightest drive %+v", c.free, c.total, tf)
		}
	}
	// The scan's evidence: free and total in whole GB.
	f := sfacts()
	file(&f, "WarehouseLive").VolumeFreeMB = 9000
	_, fs, _ := sbuild(f)
	if e := allOf(fs, "STO01")[0].Evidence; e != "Free: 8 GB of 100 GB." {
		t.Errorf("STO01 evidence %q", e)
	}
	// Only volumes of this database count: a full T:\ under tempdb alone is not STO01.
	f = sfacts()
	for i := range f.Files {
		if f.Files[i].DatabaseID == 2 {
			f.Files[i].VolumeFreeMB = 100
		}
	}
	if _, fs, _ := sbuild(f); len(allOf(fs, "STO01")) != 0 {
		t.Errorf("tempdb's volume is not this database's: %+v", allOf(fs, "STO01"))
	}
}

func TestStorageSTO02(t *testing.T) {
	f := sfacts()
	d := file(&f, "WarehouseLive")
	d.IsPercentGrowth, d.Growth, d.SizeMB = true, 10, 2048
	t3 := file(&f, "temp3")
	t3.IsPercentGrowth, t3.Growth = true, 25
	l := file(&f, "WarehouseLive_log")
	l.IsPercentGrowth, l.Growth = true, 0 // percent but 0: STO03, not STO02
	_, fs, _ := sbuild(f)
	got := allOf(fs, "STO02")
	if len(got) != 2 {
		t.Fatalf("STO02 per file: %+v", got)
	}
	wantStorageFinding(t, got[0], SeverityMedium, WhenOnline, EffortQuickWin, "WarehouseLive", "File WarehouseLive (rows) grows by 10%.")
	if got[0].DoThis != "Use a fixed growth: ALTER DATABASE [WarehouseLive] MODIFY FILE (NAME = N'WarehouseLive', FILEGROWTH = 256MB);" ||
		got[0].Evidence != "Current size: 2048 MB." {
		t.Errorf("STO02 do/evidence %q %q", got[0].DoThis, got[0].Evidence)
	}
	wantStorageFinding(t, got[1], SeverityMedium, WhenOnline, EffortQuickWin, "temp3", "File temp3 (rows) grows by 25%.")
	if got[1].DoThis != "Use a fixed growth: ALTER DATABASE [tempdb] MODIFY FILE (NAME = N'temp3', FILEGROWTH = 64MB);" {
		t.Errorf("tempdb STO02 do %q", got[1].DoThis)
	}
	for size, mb := range map[int64]string{1023: "64MB", 1024: "256MB", 10239: "256MB", 10240: "1024MB"} {
		f := sfacts()
		d := file(&f, "WarehouseLive")
		d.IsPercentGrowth, d.Growth, d.SizeMB = true, 10, size
		_, fs, _ := sbuild(f)
		if x := allOf(fs, "STO02"); len(x) != 1 || !strings.Contains(x[0].DoThis, "FILEGROWTH = "+mb) {
			t.Errorf("size %d: %+v", size, x)
		}
	}
}

func TestStorageSTO03(t *testing.T) {
	f := sfacts()
	file(&f, "WarehouseLive_log").Growth = 0
	d := file(&f, "WarehouseLive")
	d.Growth, d.SizeMB = 4096, 1024 // 32 MB on 1 GB
	file(&f, "temp5").Growth = 0
	_, fs, _ := sbuild(f)
	got := allOf(fs, "STO03")
	if len(got) != 3 {
		t.Fatalf("STO03 per file: %+v", got)
	}
	wantStorageFinding(t, got[0], SeverityLow, WhenOnline, EffortQuickWin, "WarehouseLive", "File WarehouseLive grows in small steps of 32 MB.")
	if got[0].Why != sto03WhySmall || got[0].DoThis != "ALTER DATABASE [WarehouseLive] MODIFY FILE (NAME = N'WarehouseLive', FILEGROWTH = 256MB);" {
		t.Errorf("STO03 small: %+v", got[0])
	}
	wantStorageFinding(t, got[1], SeverityLow, WhenOnline, EffortQuickWin, "WarehouseLive_log", "File WarehouseLive_log has autogrowth switched off.")
	if got[1].Why != sto03WhyOff {
		t.Errorf("STO03 off why %q", got[1].Why)
	}
	wantStorageFinding(t, got[2], SeverityLow, WhenOnline, EffortQuickWin, "temp5", "File temp5 has autogrowth switched off.")

	// Thresholds: 64 MB growth, or a file under 1 GB, does not fire.
	for _, c := range []struct{ growth, size int64 }{{8192, 1024}, {4096, 1023}} {
		f := sfacts()
		d := file(&f, "WarehouseLive")
		d.Growth, d.SizeMB = c.growth, c.size
		if _, fs, _ := sbuild(f); len(allOf(fs, "STO03")) != 0 {
			t.Errorf("growth %d size %d: %+v", c.growth, c.size, allOf(fs, "STO03"))
		}
	}
	f = sfacts()
	d = file(&f, "WarehouseLive")
	d.Growth, d.SizeMB = 0, 20480
	_, fs, _ = sbuild(f)
	if x := allOf(fs, "STO03"); len(x) != 1 || !strings.Contains(x[0].DoThis, "FILEGROWTH = 1024MB") {
		t.Errorf("big file: %+v", x)
	}
}

func TestStorageSTO04(t *testing.T) {
	f := sfacts()
	d := file(&f, "WarehouseLive")
	d.MaxSizePages, d.SizeMB = 128000, 900 // max 1000 MB, at 90%
	_, fs, _ := sbuild(f)
	got := allOf(fs, "STO04")
	if len(got) != 1 {
		t.Fatalf("STO04: %+v", got)
	}
	wantStorageFinding(t, got[0], SeverityHigh, WhenOnline, EffortQuickWin, "WarehouseLive", "File WarehouseLive is at 90% of its maximum size.")
	if got[0].DoThis != "Raise or remove the limit (check disk space first): ALTER DATABASE [WarehouseLive] MODIFY FILE (NAME = N'WarehouseLive', MAXSIZE = UNLIMITED);" ||
		got[0].Evidence != "Size: 900 MB, max: 1000 MB." {
		t.Errorf("STO04 %+v", got[0])
	}
	d = file(&f, "WarehouseLive")
	d.SizeMB = 899
	if _, fs, _ := sbuild(f); len(allOf(fs, "STO04")) != 0 {
		t.Errorf("89.9%%: %+v", allOf(fs, "STO04"))
	}

	// Express: 8 GB of data Medium, 9 GB High; Decide first / Project.
	for size, sev := range map[int64]Severity{8191: "", 8192: SeverityMedium, 9215: SeverityMedium, 9216: SeverityHigh} {
		f := sfacts()
		f.EngineEdition = 4
		file(&f, "WarehouseLive").SizeMB = size
		_, fs, _ := sbuild(f)
		got := allOf(fs, "STO04")
		if sev == "" {
			if len(got) != 0 {
				t.Errorf("express %d: %+v", size, got)
			}
			continue
		}
		if len(got) != 1 {
			t.Fatalf("express %d: %+v", size, got)
		}
		wantStorageFinding(t, got[0], sev, WhenDecideFirst, EffortProject, "WarehouseLive", "Database is close to the 10 GB limit of SQL Server Express.")
	}

	// SQL Server 2025 Express holds 50 GB: 9 GB is nothing to say, 40 GB is 80%.
	f = sfacts()
	f.EngineEdition, f.EngineVersion = 4, "17.0.1000.7"
	file(&f, "WarehouseLive").SizeMB = 9216
	if c, fs, _ := sbuild(f); len(allOf(fs, "STO04")) != 0 || !strings.Contains(c.Verdict, "of the 50 GB SQL Server Express allows") ||
		!strings.HasSuffix(c.Facts[1].Value, "of Express's 50 GB") {
		t.Errorf("2025 at 9 GB: %+v %q %+v", allOf(fs, "STO04"), c.Verdict, c.Facts[1])
	}
	file(&f, "WarehouseLive").SizeMB = 40960
	if got := allOf(fs2(f), "STO04"); len(got) != 1 || got[0].Finding != "Database is close to the 50 GB limit of SQL Server Express." ||
		got[0].Severity != SeverityMedium || got[0].Evidence != "Data size: 40960 MB of the 51200 MB limit." {
		t.Errorf("2025 at 40 GB: %+v", got)
	}
}

func fs2(f StorageFacts) []Finding {
	_, fs, _ := sbuild(f)
	return fs
}

func TestStorageSTO05AndSTO06(t *testing.T) {
	f := sfacts()
	for _, n := range []string{"WarehouseLive", "WarehouseLive_log"} {
		x := file(&f, n)
		x.Volume, x.VolumeTotalMB, x.VolumeFreeMB = `C:\`, 512000, 256000
	}
	_, fs, _ := sbuild(f)
	got := allOf(fs, "STO05")
	if len(got) != 1 {
		t.Fatalf("STO05 once per database: %+v", got)
	}
	wantStorageFinding(t, got[0], SeverityMedium, WhenWindow, EffortModerate, "WarehouseLive", "Database files are on the system drive (C:).")
	if got[0].Evidence != `Files: D:\Data\WarehouseLive.mdf, L:\Log\WarehouseLive_log.ldf` {
		t.Errorf("STO05 evidence %q", got[0].Evidence)
	}
	got = allOf(fs, "STO06")
	if len(got) != 1 {
		t.Fatalf("STO06: %+v", got)
	}
	wantStorageFinding(t, got[0], SeverityLow, WhenWindow, EffortProject, "WarehouseLive", "The data and log files are on the same volume.")
	if got[0].Evidence != "Databases: WarehouseLive" {
		t.Errorf("STO06 evidence %q", got[0].Evidence)
	}
	// Separate volumes: neither.
	if _, fs, _ := sbuild(sfacts()); len(allOf(fs, "STO05"))+len(allOf(fs, "STO06")) != 0 {
		t.Errorf("separate volumes: %+v", fs)
	}
}

func TestStorageSTO07(t *testing.T) {
	for _, c := range []struct {
		reuse, do string
	}{
		{"LOG_BACKUP", "The log is waiting for a log backup. Schedule regular log backups (BAK02), then shrink the log once to a sensible size in a quiet period."},
		{"ACTIVE_TRANSACTION", "A long-running open transaction is holding the log. Find it with DBCC OPENTRAN and have it finished, then shrink the log once."},
		{"REPLICATION", "Replication or CDC is behind. Ask a DBA to check the log reader agent."},
		{"AVAILABILITY_REPLICA", "An Availability Group replica is behind. Check the AG dashboard and the secondary replica."},
		{"NOTHING", "Find out why the log grew (often a one-off large operation). Once resolved, shrink the log once to a sensible size in a quiet period."},
	} {
		f := sfacts()
		f.LogReuseWait = c.reuse
		file(&f, "WarehouseLive_log").SizeMB = 4097
		_, fs, _ := sbuild(f)
		got := allOf(fs, "STO07")
		if len(got) != 1 {
			t.Fatalf("%s: %+v", c.reuse, got)
		}
		wantStorageFinding(t, got[0], SeverityMedium, WhenOnline, EffortModerate, "WarehouseLive", "The log file (4097 MB) is larger than the data (4096 MB).")
		if got[0].DoThis != c.do || got[0].Evidence != "Log reuse is waiting on: "+c.reuse {
			t.Errorf("%s: %q %q", c.reuse, got[0].DoThis, got[0].Evidence)
		}
	}
	// Equal, or bigger but under 1 GB: no finding.
	for _, c := range []struct{ data, log int64 }{{4096, 4096}, {500, 1023}} {
		f := sfacts()
		file(&f, "WarehouseLive").SizeMB = c.data
		file(&f, "WarehouseLive_log").SizeMB = c.log
		if _, fs, _ := sbuild(f); len(allOf(fs, "STO07")) != 0 {
			t.Errorf("%+v: %+v", c, allOf(fs, "STO07"))
		}
	}
}

func TestStorageSTO08(t *testing.T) {
	for n, sev := range map[int]Severity{500: "", 501: SeverityLow, 1000: SeverityLow, 1001: SeverityMedium} {
		f := sfacts()
		f.VLFCount = intp(n)
		_, fs, _ := sbuild(f)
		got := allOf(fs, "STO08")
		if sev == "" {
			if len(got) != 0 {
				t.Errorf("%d VLFs: %+v", n, got)
			}
			continue
		}
		if len(got) != 1 {
			t.Fatalf("%d VLFs: %+v", n, got)
		}
		wantStorageFinding(t, got[0], sev, WhenWindow, EffortModerate, "WarehouseLive",
			"The transaction log is split into "+itoa(int64(n))+" virtual log files (VLFs).")
	}
	f := sfacts()
	f.VLFCount, f.VLFUnsupported = nil, true
	if _, _, checks := sbuild(f); checkOf(checks, "STO08") != (CheckStatus{"STO08", "Virtual log file (VLF) count", CheckSkipped, vlfOldNote}) {
		t.Errorf("before 2016 SP2: %+v", checkOf(checks, "STO08"))
	}
	f = sfacts()
	f.VLFCount, f.VLFError = nil, "permission denied"
	if _, _, checks := sbuild(f); checkOf(checks, "STO08").Note != "Could not count VLFs for this database: permission denied" {
		t.Errorf("VLF error: %+v", checkOf(checks, "STO08"))
	}
}

func TestStorageSTO09(t *testing.T) {
	set := func(f *StorageFacts, name string, reads, readStall, writes, writeStall int64) {
		x := file(f, name)
		x.Reads, x.ReadStallMs, x.Writes, x.WriteStallMs = reads, readStall, writes, writeStall
	}
	for _, c := range []struct {
		name                             string
		reads, readStall, writes, wStall int64
		want                             Severity
		finding                          string
	}{
		{"WarehouseLive", 1000, 20000, 0, 0, SeverityMedium, `Data reads average 20 ms on D:\Data\WarehouseLive.mdf.`},
		{"WarehouseLive", 1000, 50000, 0, 0, SeverityHigh, `Data reads average 50 ms on D:\Data\WarehouseLive.mdf.`},
		{"WarehouseLive", 999, 99900, 0, 0, "", ""},
		{"WarehouseLive", 1000, 19999, 0, 0, "", ""},
		{"WarehouseLive", 0, 0, 1000, 90000, "", ""}, // slow data writes are not STO09
		{"WarehouseLive_log", 0, 0, 1000, 10000, SeverityMedium, `Log writes average 10 ms on L:\Log\WarehouseLive_log.ldf.`},
		{"WarehouseLive_log", 0, 0, 1000, 20000, SeverityHigh, `Log writes average 20 ms on L:\Log\WarehouseLive_log.ldf.`},
		{"WarehouseLive_log", 0, 0, 999, 99900, "", ""},
	} {
		f := sfacts()
		set(&f, "WarehouseLive", 5000, 25000, 100, 100)
		set(&f, "WarehouseLive_log", 10, 10, 8000, 8000)
		set(&f, c.name, c.reads, c.readStall, c.writes, c.wStall)
		card, fs, _ := sbuild(f)
		got := allOf(fs, "STO09")
		if c.want == "" {
			if len(got) != 0 {
				t.Errorf("%+v: %+v", c, got)
			}
			continue
		}
		if len(got) != 1 {
			t.Fatalf("%+v: %+v", c, got)
		}
		wantStorageFinding(t, got[0], c.want, WhenDecideFirst, EffortProject, c.name, c.finding)
		// Latency is a finding, no longer a fact (the Log fact took its place).
		if card.Status != StatusAmber || factOf(card, "Disk latency").Label != "" {
			t.Errorf("%+v: card %+v", c, card)
		}
	}
	// One finding per file.
	f := sfacts()
	set(&f, "WarehouseLive", 2000, 60000, 0, 0)
	set(&f, "WarehouseLive_log", 0, 0, 2000, 30000)
	_, fs, _ := sbuild(f)
	got := allOf(fs, "STO09")
	if len(got) != 2 || got[0].Evidence != "Reads: 2,000 (avg 30 ms)." || got[1].Evidence != "Writes: 2,000 (avg 15 ms)." {
		t.Errorf("per file: %+v", got)
	}
	// Latency unreadable: skipped, the card still answers.
	f = sfacts()
	f.LatencyError = "the login does not have VIEW SERVER STATE."
	card, _, checks := sbuild(f)
	if checkOf(checks, "STO09").Status != CheckSkipped || card.Status != StatusGreen {
		t.Errorf("latency unreadable: %+v %+v", checkOf(checks, "STO09"), card)
	}
}

func TestStorageTMP01AndTMP02(t *testing.T) {
	keep := func(f *StorageFacts, n int) {
		out := []StorageFile{}
		data := 0
		for _, x := range f.Files {
			if x.DatabaseID == 2 && x.Type == "ROWS" {
				if data >= n {
					continue
				}
				data++
			}
			out = append(out, x)
		}
		f.Files = out
	}
	f := sfacts()
	keep(&f, 4)
	_, fs, _ := sbuild(f)
	got := allOf(fs, "TMP01")
	if len(got) != 1 {
		t.Fatalf("TMP01: %+v", got)
	}
	wantStorageFinding(t, got[0], SeverityLow, WhenOnline, EffortQuickWin, "tempdb", "TempDB has 4 data files; 8 is recommended for this server.")
	if got[0].DoThis != `Add data files until there are 8, all the same size and growth. Example: ALTER DATABASE tempdb ADD FILE (NAME = N'tempdev5', FILENAME = N'T:\TempDB\tempdb5.ndf', SIZE = 128MB, FILEGROWTH = 256MB);  No restart needed.` ||
		got[0].Evidence != "Data files: 4. Logical CPUs: 8." {
		t.Errorf("TMP01 %q %q", got[0].DoThis, got[0].Evidence)
	}
	f = sfacts()
	keep(&f, 1)
	_, fs, _ = sbuild(f)
	if x := allOf(fs, "TMP01"); len(x) != 1 || x[0].Severity != SeverityMedium {
		t.Errorf("one file: %+v", x)
	}
	f = sfacts()
	f.CPUCount = 2
	keep(&f, 2)
	if _, fs, _ := sbuild(f); len(allOf(fs, "TMP01")) != 0 {
		t.Errorf("2 files for 2 CPUs: %+v", allOf(fs, "TMP01"))
	}
	f = sfacts()
	f.CPUCount = 64 // capped at 8
	if _, fs, _ := sbuild(f); len(allOf(fs, "TMP01")) != 0 {
		t.Errorf("8 files for 64 CPUs: %+v", allOf(fs, "TMP01"))
	}
	f = sfacts()
	f.CPUCount, f.CPUError = 0, "the login does not have VIEW SERVER STATE."
	if _, _, checks := sbuild(f); checkOf(checks, "TMP01").Note != "Could not read the number of CPUs: the login does not have VIEW SERVER STATE." {
		t.Errorf("no CPUs: %+v", checkOf(checks, "TMP01"))
	}

	f = sfacts()
	file(&f, "temp2").SizeMB = 256
	t4 := file(&f, "temp4")
	t4.IsPercentGrowth, t4.Growth = true, 10
	_, fs, _ = sbuild(f)
	got = allOf(fs, "TMP02")
	if len(got) != 1 {
		t.Fatalf("TMP02: %+v", got)
	}
	wantStorageFinding(t, got[0], SeverityLow, WhenOnline, EffortQuickWin, "tempdb", "TempDB data files are not all the same size and growth.")
	if !strings.HasPrefix(got[0].Evidence, "Files: temp1 128 MB / growth 64 MB, temp2 256 MB / growth 64 MB, temp3 128 MB / growth 64 MB, temp4 128 MB / growth 10%,") {
		t.Errorf("TMP02 evidence %q", got[0].Evidence)
	}
}

func TestStorageTMP03(t *testing.T) {
	for _, c := range []struct {
		alloc, free int64
		logPct      float64
		volFree     int64
		noGrowth    bool
		want        Severity
		finding     string
	}{
		{1024, 900, 5, 10240, false, "", ""},
		{1024, 512, 5, 10240, false, SeverityMedium, "TempDB is 50% full (512 MB of 1,024 MB)."},
		{256, 128, 5, 10240, false, "", ""}, // half full but small
		{256, 51, 5, 10240, false, SeverityMedium, "TempDB is 80% full (205 MB of 256 MB)."},
		{4096, 400, 5, 10240, false, SeverityMedium, "TempDB is 90% full (3,696 MB of 4,096 MB)."},
		{4096, 400, 5, 10240, true, SeverityHigh, "TempDB is 90% full (3,696 MB of 4,096 MB) and its files cannot grow."},
		{4096, 400, 5, 1000, false, SeverityHigh, "TempDB is 90% full (3,696 MB of 4,096 MB) and the disk has only 1,000 MB free."},
		{1024, 900, 70, 10240, false, SeverityMedium, "The TempDB log is 70.0% full."},
		{1024, 900, 95, 100, false, SeverityHigh, "The TempDB log is 95.0% full and the disk has only 100 MB free."},
	} {
		f := sfacts()
		f.TempdbAllocMB, f.TempdbFreeMB, f.TempdbLogUsedPct = i64(c.alloc), i64(c.free), f64(c.logPct)
		for i := range f.Files {
			if f.Files[i].DatabaseID == 2 {
				f.Files[i].VolumeFreeMB = c.volFree
				if c.noGrowth {
					f.Files[i].Growth = 0
				}
			}
		}
		card, fs, _ := sbuild(f)
		got := allOf(fs, "TMP03")
		if c.want == "" {
			if len(got) != 0 {
				t.Errorf("%+v: %+v", c, got)
			}
			continue
		}
		if len(got) != 1 {
			t.Fatalf("%+v: %+v", c, got)
		}
		wantStorageFinding(t, got[0], c.want, WhenNone, EffortModerate, "tempdb", c.finding)
		if tf := factOf(card, "TempDB"); tf.Tone != ToneBad {
			t.Errorf("%+v: tempdb fact %+v", c, tf)
		}
	}
	f := sfacts()
	f.TempdbAllocMB, f.TempdbFreeMB, f.TempdbError = nil, nil, "denied"
	card, _, checks := sbuild(f)
	if checkOf(checks, "TMP03").Note != "Could not read how full TempDB is: denied" || factOf(card, "TempDB").Value != "could not be read" {
		t.Errorf("tempdb unreadable: %+v %+v", checkOf(checks, "TMP03"), card.Facts)
	}
}

func TestStorageIDX07(t *testing.T) {
	f := sfacts()
	d := file(&f, "WarehouseLive")
	d.SizeMB, d.UsedMB = 6000, i64(2000)
	l := file(&f, "WarehouseLive_log")
	l.SizeMB, l.UsedMB = 3000, i64(120)
	f.LogReuseWait = "LOG_BACKUP"
	_, fs, _ := sbuild(f)
	got := allOf(fs, "IDX07")
	if len(got) != 2 {
		t.Fatalf("IDX07: %+v", got)
	}
	wantStorageFinding(t, got[0], SeverityMedium, WhenOnline, EffortModerate, "WarehouseLive",
		"The log file (3,000 MB) is bigger than all the data in the database (2,000 MB).")
	if !strings.HasPrefix(got[0].DoThis, "The log is waiting for a log backup.") || got[0].Evidence != "Log used: 120 MB. Waiting on: LOG_BACKUP. Recovery model: FULL." {
		t.Errorf("IDX07 log %+v", got[0])
	}
	wantStorageFinding(t, got[1], SeverityLow, WhenWindow, EffortModerate, "WarehouseLive", "Data file is 5.9 GB but only 2.0 GB is used.")
	if !strings.HasSuffix(got[1].DoThis, "DBCC SHRINKFILE (N'WarehouseLive', 2500);") || got[1].Evidence != "Allocated 6,000 MB, used 2,000 MB." {
		t.Errorf("IDX07 file %+v", got[1])
	}
	// Thresholds: under 5 GB, half used, a log under 2 GB.
	f = sfacts()
	d = file(&f, "WarehouseLive")
	d.SizeMB, d.UsedMB = 5119, i64(100)
	file(&f, "WarehouseLive_log").SizeMB = 2047
	if _, fs, _ := sbuild(f); len(allOf(fs, "IDX07")) != 0 {
		t.Errorf("under the thresholds: %+v", allOf(fs, "IDX07"))
	}
	f = sfacts()
	d = file(&f, "WarehouseLive")
	d.SizeMB, d.UsedMB = 6000, i64(3000)
	if _, fs, _ := sbuild(f); len(allOf(fs, "IDX07")) != 0 {
		t.Errorf("half used: %+v", allOf(fs, "IDX07"))
	}
	f = sfacts()
	f.SpaceUsedError = "denied"
	if _, _, checks := sbuild(f); checkOf(checks, "IDX07").Status != CheckSkipped {
		t.Errorf("space used unreadable: %+v", checkOf(checks, "IDX07"))
	}
}

func TestStorageIDX10ArchiveTables(t *testing.T) {
	oldest := time.Date(2024, 1, 5, 10, 22, 0, 0, time.UTC)
	f := sfacts()
	f.GrowingTables = []GrowingTable{
		{Schema: "archive", Name: "IntegrationLog", Rows: 2000000, MB: 973, DateCol: "At", Oldest: &oldest}, // kakapo's own archive
		{Schema: "dbo", Name: "Audit", Rows: 2000000, MB: 700, DateCol: "At", Oldest: &oldest},              // archived, no job left
	}
	f.ArchiveTables = []string{"Audit", "IntegrationLog"}
	_, fs, _ := sbuild(f)
	got := allOf(fs, "IDX10")
	if len(got) != 1 || got[0].Object != "[dbo].[Audit]" || got[0].Severity != SeverityInfo ||
		got[0].Finding != "dbo.Audit has 2,000,000 rows (700 MB). archive.Audit holds older rows, but no archive job runs on this server." {
		t.Errorf("IDX10: %+v", got)
	}
}

func TestStorageIDX01QueryStoreAndIndexes(t *testing.T) {
	f := sfacts()
	f.Tables[0].IndexCount = 0
	qs, max := int64(576), int64(2048)
	f.QueryStoreMB, f.QueryStoreMaxMB = &qs, &max
	_, fs, _ := sbuild(f)
	if !strings.Contains(fs[0].Finding, "800 MB data and 400 MB in no nonclustered index.") ||
		!strings.HasSuffix(fs[0].Evidence, " Query Store: 576 MB of the 2,048 MB it may use.") {
		t.Errorf("IDX01: %q / %q", fs[0].Finding, fs[0].Evidence)
	}
}

func TestStorageIDX10(t *testing.T) {
	oldest := time.Date(2024, 1, 5, 10, 22, 0, 0, time.UTC)
	for _, c := range []struct {
		rows int64
		mb   float64
		want Severity
	}{
		{999999, 499, ""},
		{1000000, 100, SeverityLow},
		{10, 500, SeverityLow},
		{9999999, 5119, SeverityLow},
		{10000000, 100, SeverityMedium},
		{10, 5120, SeverityMedium},
	} {
		f := sfacts()
		f.GrowingTables = []GrowingTable{{Schema: "dbo", Name: "IntegrationLog", Rows: c.rows, MB: c.mb, DateCol: "LoggedAt", Oldest: &oldest}}
		card, fs, _ := sbuild(f)
		got := allOf(fs, "IDX10")
		if c.want == "" {
			if len(got) != 0 {
				t.Errorf("%+v: %+v", c, got)
			}
			continue
		}
		if len(got) != 1 {
			t.Fatalf("%+v: %+v", c, got)
		}
		if got[0].Severity != c.want || got[0].When != WhenDecideFirst || got[0].Effort != EffortModerate ||
			got[0].Object != "[dbo].[IntegrationLog]" || got[0].Owner != OwnerVendor {
			t.Errorf("%+v: %+v", c, got[0])
		}
		if fact := factOf(card, "Tables that only grow"); fact.Tone != ToneBad || !strings.HasPrefix(fact.Value, "1, biggest dbo.IntegrationLog ") {
			t.Errorf("%+v: fact %+v", c, fact)
		}
	}

	f := sfacts()
	f.GrowingTables = []GrowingTable{
		// A history table whose first date column goes back almost three years.
		{Schema: "dbo", Name: "OrderHistory", Rows: 2000000, MB: 700, DateCol: "CreatedAt", Oldest: &oldest},
		// A log table without a date column: how far back it goes is unknown.
		{Schema: "app", Name: "IntegrationLog", Rows: 1500000, MB: 1500.4},
		// Big, but its name does not say log or history: no finding.
		{Schema: "dbo", Name: "Transaction", Rows: 50000000, MB: 20000},
		// 10 million rows: Medium.
		{Schema: "dbo", Name: "AuditTrail", Rows: 12000000, MB: 4000, DateCol: "At"},
	}
	card, fs, _ := sbuild(f)
	got := allOf(fs, "IDX10")
	if len(got) != 3 {
		t.Fatalf("IDX10 per table: %+v", got)
	}
	audit, history, integration := got[0], got[1], got[2] // Medium first
	wantStorageFinding(t, history, SeverityLow, WhenDecideFirst, EffortModerate, "[dbo].[OrderHistory]",
		"dbo.OrderHistory has 2,000,000 rows (700 MB) and looks like a log or history table; its oldest row is from 2024-01-05 (33 months ago), so nothing seems to remove old rows.")
	if history.Why != idx10Why || history.DoThis != idx10Do || history.Evidence != "Rows: 2,000,000. Size: 700 MB. Oldest CreatedAt: 2024-01-05 10:22." {
		t.Errorf("IDX10 why, do, evidence: %q %q %q", history.Why, history.DoThis, history.Evidence)
	}
	wantStorageFinding(t, integration, SeverityLow, WhenDecideFirst, EffortModerate, "[app].[IntegrationLog]",
		"app.IntegrationLog has 1,500,000 rows (1,500 MB) and looks like a log or history table.")
	if integration.Evidence != "Rows: 1,500,000. Size: 1,500 MB. It has no date column, so how far back its rows go is unknown." {
		t.Errorf("IDX10 evidence without a date column: %q", integration.Evidence)
	}
	wantStorageFinding(t, audit, SeverityMedium, WhenDecideFirst, EffortModerate, "[dbo].[AuditTrail]",
		"dbo.AuditTrail has 12,000,000 rows (4,000 MB) and looks like a log or history table.")
	if audit.Evidence != "Rows: 12,000,000. Size: 4,000 MB. Its date column At is not indexed, so how far back its rows go was not read." {
		t.Errorf("IDX10 evidence with an unindexed date column: %q", audit.Evidence)
	}
	if fact := factOf(card, "Tables that only grow"); fact.Value != "3, biggest dbo.AuditTrail 4,000 MB" {
		t.Errorf("tables that only grow fact %+v", fact)
	}

	// The minimum age: an oldest row 12 months old fires, 11 months old is being cleaned up. An
	// indexed date column that could not be read leaves the age unknown: the finding fires and
	// says nothing about removal.
	twelve, eleven := time.Date(2025, 11, 3, 0, 0, 0, 0, time.UTC), time.Date(2025, 11, 4, 0, 0, 0, 0, time.UTC)
	f = sfacts()
	f.GrowingTables = []GrowingTable{
		{Schema: "dbo", Name: "EventLog", Rows: 3000000, MB: 900, DateCol: "At", DateIndexed: true, Oldest: &twelve},
		{Schema: "dbo", Name: "PickHistory", Rows: 3000000, MB: 800, DateCol: "At", DateIndexed: true, Oldest: &eleven},
		{Schema: "dbo", Name: "MailLog", Rows: 2000000, MB: 700, DateCol: "SentAt", DateIndexed: true},
	}
	_, fs, _ = sbuild(f)
	got = allOf(fs, "IDX10")
	if len(got) != 2 {
		t.Fatalf("IDX10 at the age boundary: %+v", got)
	}
	wantStorageFinding(t, got[0], SeverityLow, WhenDecideFirst, EffortModerate, "[dbo].[EventLog]",
		"dbo.EventLog has 3,000,000 rows (900 MB) and looks like a log or history table; its oldest row is from 2025-11-03 (12 months ago), so nothing seems to remove old rows.")
	wantStorageFinding(t, got[1], SeverityLow, WhenDecideFirst, EffortModerate, "[dbo].[MailLog]",
		"dbo.MailLog has 2,000,000 rows (700 MB) and looks like a log or history table.")
	if got[1].Evidence != "Rows: 2,000,000. Size: 700 MB. Its date column SentAt could not be read, so how far back its rows go is unknown." {
		t.Errorf("IDX10 evidence with an unreadable date column: %q", got[1].Evidence)
	}

	// A queue that keeps its processed rows: its date column is not indexed, but its clustered key is
	// an ascending identity, so the oldest row's date was read through the key. Its kind is "queue".
	// A queue whose key read found nothing says its date column could not be read.
	queued := time.Date(2025, 9, 25, 16, 19, 2, 0, time.UTC)
	f = sfacts()
	f.GrowingTables = []GrowingTable{
		{Schema: "dbo", Name: "IntegrationDocumentQueue", Rows: 5284503, MB: 523.1, DateCol: "LastUpdateDateTime", KeyCol: "ID", Oldest: &queued},
		{Schema: "dbo", Name: "MessageQueue", Rows: 2000000, MB: 300, DateCol: "QueuedAt", KeyCol: "ID"},
	}
	_, fs, _ = sbuild(f)
	got = allOf(fs, "IDX10")
	if len(got) != 2 {
		t.Fatalf("IDX10 for queues: %+v", got)
	}
	wantStorageFinding(t, got[0], SeverityLow, WhenDecideFirst, EffortModerate, "[dbo].[IntegrationDocumentQueue]",
		"dbo.IntegrationDocumentQueue has 5,284,503 rows (523 MB) and looks like a queue table; its oldest row is from 2025-09-25 (13 months ago), so nothing seems to remove old rows.")
	if got[0].Evidence != "Rows: 5,284,503. Size: 523 MB. Oldest LastUpdateDateTime: 2025-09-25 16:19 (the row with the lowest ID)." {
		t.Errorf("IDX10 evidence read through the key: %q", got[0].Evidence)
	}
	if got[1].Evidence != "Rows: 2,000,000. Size: 300 MB. Its date column QueuedAt could not be read, so how far back its rows go is unknown." {
		t.Errorf("IDX10 evidence when the key read found nothing: %q", got[1].Evidence)
	}
	if growingKind("AuditQueue") != "log or history" || !LooksLikeLogTable("IntegrationDocumentQueue") {
		t.Error("queue words")
	}
	if chk, _ := CheckByID("IDX10"); chk.Title != "Application tables that only grow" || chk.Question != "Which tables grow for ever?" ||
		chk.Owner != OwnerVendor || chk.ThenCheck != "Re-run after the cleanup: the row count stopped growing." {
		t.Errorf("IDX10 catalogue row %+v", chk)
	}
}

func TestLooksLikeLogTable(t *testing.T) {
	for name, want := range map[string]bool{
		"IntegrationLog": true, "ORDERHISTORY": true, "audit_trail": true, "SqlTrace": true, "EventQueue": true,
		"Journal": true, "Sales_Archive": true, "Transaction": false, "Item": false, "SalesOrder": false,
	} {
		if got := LooksLikeLogTable(name); got != want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

func TestWholeMonths(t *testing.T) {
	now := time.Date(2026, 11, 3, 9, 21, 0, 0, time.UTC)
	for _, c := range []struct {
		at   time.Time
		want int
	}{
		{time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), 0},
		{time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), 1},
		{time.Date(2025, 11, 4, 0, 0, 0, 0, time.UTC), 11},
		{time.Date(2025, 11, 3, 0, 0, 0, 0, time.UTC), 12},
		{time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC), 33},
	} {
		if got := wholeMonths(c.at, now); got != c.want {
			t.Errorf("%s: %d, want %d", c.at.Format("2006-01-02"), got, c.want)
		}
	}
}

func TestStorageIDX13(t *testing.T) {
	for _, c := range []struct {
		pct      float64
		want     Severity
		status   string
		factTone string
	}{
		{49.99, "", StatusGreen, ""},
		{50, SeverityLow, StatusAmber, ""},
		{70, SeverityMedium, StatusAmber, ToneBad},
		{90, SeverityHigh, StatusAmber, ToneWorse},
		{99, SeverityCritical, StatusRed, ToneWorse},
	} {
		f := sfacts()
		f.Identities = []IdentityColumn{{Table: "[dbo].[Transaction]", Column: "ID", Type: "int", LastValue: 1932735283, Seed: 1, Increment: 1, PctUsed: c.pct}}
		card, fs, _ := sbuild(f)
		got := allOf(fs, "IDX13")
		if card.Status != c.status {
			t.Errorf("%v: card %s", c.pct, card.Status)
		}
		fact := factOf(card, "Identity range")
		if c.factTone == "" {
			if fact.Label != "" {
				t.Errorf("%v: unexpected fact %+v", c.pct, fact)
			}
		} else if fact.Tone != c.factTone || fact.Value != itoa(int64(c.pct))+"% used ([dbo].[Transaction])" {
			t.Errorf("%v: fact %+v", c.pct, fact)
		}
		if c.want == "" {
			if len(got) != 0 {
				t.Errorf("%v: %+v", c.pct, got)
			}
			continue
		}
		if len(got) != 1 {
			t.Fatalf("%v: %+v", c.pct, got)
		}
		if got[0].Severity != c.want || got[0].When != WhenDecideFirst || got[0].Effort != EffortProject || got[0].Object != "[dbo].[Transaction].ID" {
			t.Errorf("%v: %+v", c.pct, got[0])
		}
		wantDo := idx13DoLow
		if c.pct >= 90 {
			wantDo = idx13DoHigh
		}
		if got[0].DoThis != wantDo || got[0].Evidence != "Type int, seed 1, increment 1." {
			t.Errorf("%v: %q %q", c.pct, got[0].DoThis, got[0].Evidence)
		}
	}
	f := sfacts()
	f.Identities = []IdentityColumn{{Table: "[dbo].[Transaction]", Column: "ID", Type: "int", LastValue: 1982000000, Seed: 1, Increment: 1, PctUsed: 92.29}}
	_, fs, _ := sbuild(f)
	if x := allOf(fs, "IDX13")[0]; x.Finding != "Identity column has used 92.3% of its int range (last value 1,982,000,000 of 2,147,483,647)." {
		t.Errorf("IDX13 sentence %q", x.Finding)
	}
}

func TestStorageFourthFact(t *testing.T) {
	grows := []GrowingTable{{Schema: "dbo", Name: "IntegrationLog", Rows: 2000000, MB: 800}}
	ident := func(pct float64) []IdentityColumn {
		return []IdentityColumn{{Table: "[dbo].[SalesOrder]", Column: "ID", Type: "int", LastValue: 1, PctUsed: pct}}
	}
	for _, c := range []struct {
		name  string
		ids   []IdentityColumn
		grows []GrowingTable
		label string
	}{
		{"identity at 70% wins", ident(70), grows, "Identity range"},
		{"identity under 70% gives way to tables that only grow", ident(69), grows, "Tables that only grow"},
		{"tables that only grow", nil, grows, "Tables that only grow"},
		{"tempdb otherwise", ident(60), nil, "TempDB"},
	} {
		f := sfacts()
		f.Identities, f.GrowingTables = c.ids, c.grows
		card, _, _ := sbuild(f)
		if len(card.Facts) != 4 || card.Facts[3].Label != c.label {
			t.Errorf("%s: %+v", c.name, card.Facts)
		}
	}
}

// The answer line is the room left when the worst finding is a to-do that is not about room
// (TempDB's files, files on C:), and on Express it says the size against the 10 GB limit.
func TestStorageVerdictRoomFirst(t *testing.T) {
	f := sfacts()
	f.Files = f.Files[:3] // one TempDB data file: TMP01 Medium, not about room
	c, fs, _ := sbuild(f)
	if fs[0].CheckID != "TMP01" || c.Status != StatusAmber || c.Verdict != `WarehouseLive is 3.0 GB on D:\, which has 50 GB free.` {
		t.Errorf("top %s, verdict %q", fs[0].CheckID, c.Verdict)
	}
	f.EngineEdition = 4
	c, _, _ = sbuild(f)
	if c.Verdict != `WarehouseLive is 3.0 GB of the 10 GB SQL Server Express allows; D:\ has 50 GB free.` {
		t.Errorf("Express verdict %q", c.Verdict)
	}
	wantFacts(t, c, `Tightest drive=D:\ 50% free (50 GB)/normal`, "Database size=4.0 GB allocated, 3.4 GB used of Express's 10 GB/normal",
		"Log=1.0 GB, 10% used/normal", "TempDB=12% full of 1.0 GB/normal")
	// From 8 GB STO04 fires and, being about room, is the answer.
	file(&f, "WarehouseLive").SizeMB = 9300
	c, _, _ = sbuild(f)
	if c.Verdict != sto04ExpressFinding(10) || c.Facts[1].Tone != ToneWorse {
		t.Errorf("Express near the limit: %q %+v", c.Verdict, c.Facts[1])
	}
}

func TestStorageCounts(t *testing.T) {
	f := sfacts()
	file(&f, "WarehouseLive").VolumeFreeMB = 15000                                    // STO01 Medium, Client IT
	file(&f, "WarehouseLive_log").Growth = 0                                          // STO03 Low, Us
	f.GrowingTables = []GrowingTable{{Schema: "dbo", Name: "MailLog", Rows: 1000000}} // IDX10 Low, Vendor
	card, fs, _ := sbuild(f)
	// Fixable: STO03 carries the autogrowth fix since phase 4d.
	if card.Status != StatusAmber || card.StatusText != "3 things to do" || card.ToDo != 3 || card.ForClientIT != 1 || card.Fixable != 1 {
		t.Errorf("card %+v", card)
	}
	if card.Verdict != `Volume D:\ is 85% full.` || fs[0].CheckID != "STO01" {
		t.Errorf("verdict %q, top %s", card.Verdict, fs[0].CheckID)
	}
	f = sfacts()
	file(&f, "WarehouseLive_log").Growth = 0
	if card, _, _ := sbuild(f); card.StatusText != "1 thing to do" || card.ForClientIT != 0 {
		t.Errorf("one: %+v", card)
	}
}

func TestStorageGrey(t *testing.T) {
	f := sfacts()
	f.State = "RESTORING"
	card, fs, checks := sbuild(f)
	note := "WarehouseLive is RESTORING, not online, so its storage was not checked."
	if card.Status != StatusGrey || card.StatusText != "Could not be read" || card.Note != note || card.AsOf != nil || len(card.Facts) != 0 || len(fs) != 0 {
		t.Errorf("not online: %+v %+v", card, fs)
	}
	for _, c := range checks {
		if c.Status != CheckSkipped || c.Note != note {
			t.Errorf("not online check %+v", c)
		}
	}
	if len(checks) != 20 {
		t.Errorf("%d checks", len(checks))
	}

	card, _, checks = sbuild(StorageFacts{Database: "WarehouseLive", Error: "could not read the storage facts: timeout"})
	if card.Status != StatusGrey || card.Note != "could not read the storage facts: timeout" || checkOf(checks, "IDX01").Status != CheckSkipped {
		t.Errorf("not read: %+v", card)
	}

	// Volumes unreadable: grey, the checks that need them skipped, the rest still run.
	f = sfacts()
	f.VolumeError = "the login does not have VIEW SERVER STATE."
	file(&f, "WarehouseLive_log").Growth = 0
	card, fs, checks = sbuild(f)
	if card.Status != StatusGrey || card.Note != "Could not read the volumes (sys.dm_os_volume_stats): the login does not have VIEW SERVER STATE." {
		t.Errorf("volumes: %+v", card)
	}
	for _, id := range []string{"STO01", "STO05", "STO06"} {
		if checkOf(checks, id).Status != CheckSkipped {
			t.Errorf("volumes: %s %+v", id, checkOf(checks, id))
		}
	}
	if checkOf(checks, "STO03").Status != CheckFinding || len(allOf(fs, "STO03")) != 1 || checkOf(checks, "IDX01").Status != CheckPassed {
		t.Errorf("volumes: the rest still run: %+v", checks)
	}

	// Files unreadable: grey; the instance and inside-the-database checks still run.
	f = sfacts()
	f.FilesError = "sys.master_files shows no files of this database to this login (it needs VIEW ANY DEFINITION)."
	f.Files = f.TempdbFiles()
	card, _, checks = sbuild(f)
	if card.Status != StatusGrey || !strings.HasPrefix(card.Note, "Could not read the database files: sys.master_files") {
		t.Errorf("files: %+v", card)
	}
	for _, id := range []string{"STO01", "STO02", "STO03", "STO04", "STO05", "STO06", "STO07", "STO09", "IDX07"} {
		if checkOf(checks, id).Status != CheckSkipped {
			t.Errorf("files: %s %+v", id, checkOf(checks, id))
		}
	}
	for _, id := range []string{"TMP01", "TMP02", "TMP03", "IDX01", "IDX10", "IDX13", "STO08"} {
		if checkOf(checks, id).Status != CheckPassed {
			t.Errorf("files: %s should still run: %+v", id, checkOf(checks, id))
		}
	}

	// Inside the database unreadable: those checks skipped, the card still answers.
	f = sfacts()
	f.TablesError, f.GrowingTablesError, f.IdentityError = "denied", "denied", "denied"
	card, _, checks = sbuild(f)
	if card.Status != StatusGreen || factOf(card, "Database size").Value != "4.0 GB allocated, 3.4 GB used" || checkOf(checks, "IDX01").Note != "Could not read the table sizes: denied" ||
		checkOf(checks, "IDX10").Status != CheckSkipped || checkOf(checks, "IDX13").Status != CheckSkipped {
		t.Errorf("inside: %+v %+v", card, checks)
	}
	if card.Verdict != `WarehouseLive is 4.0 GB on D:\, which has 50 GB free.` {
		t.Errorf("verdict from the data files: %q", card.Verdict)
	}
}

func TestStorageSTO10Status(t *testing.T) {
	room := func(sev Severity) *CheckdbRoom {
		return &CheckdbRoom{Finding: Finding{CheckID: "STO10", Severity: sev}}
	}
	for sev, want := range map[Severity]string{SeverityInfo: CheckPassed, SeverityMedium: CheckFinding, SeverityHigh: CheckFinding} {
		card, fs, checks := BuildStorageCard(sfacts(), storageDef, room(sev), nil)
		if s := checkOf(checks, "STO10"); s.Status != want || s.Note != "" {
			t.Errorf("%s: %+v", sev, s)
		}
		if card.Status != StatusGreen || len(allOf(fs, "STO10")) != 0 {
			t.Errorf("%s: the stored result does not change the card: %+v", sev, card)
		}
	}
	f := sfacts()
	f.State = "OFFLINE"
	if _, _, checks := BuildStorageCard(f, storageDef, room(SeverityHigh), nil); checkOf(checks, "STO10").Status != CheckSkipped {
		t.Errorf("not online: %+v", checkOf(checks, "STO10"))
	}
}

func TestCheckdbRoomVerdicts(t *testing.T) {
	at := time.Date(2026, 11, 3, 7, 30, 0, 0, time.UTC)
	base := CheckdbRoomInputs{Database: "WarehouseLive", CapturedAt: at, Duration: 1500 * time.Millisecond,
		EstimateKB: 1048576, TempdbFreeMB: 4096, TempdbRoomMB: 20480, DataVolume: `D:\`, DataFreeMB: 51200, DataSizeMB: 40960}

	// Passed: 2 GB needed, 4 GB free, plenty on D:.
	r := BuildCheckdbRoom(base)
	if r.NeedMB != 2048 || r.EstimateKB != 1048576 || r.DurationSeconds != 1.5 || !r.CapturedAt.Equal(at) || r.DataVolume != `D:\` {
		t.Errorf("numbers %+v", r)
	}
	wantStorageFinding(t, r.Finding, SeverityInfo, WhenDecideFirst, EffortModerate, "WarehouseLive",
		"CHECKDB on WarehouseLive needs about 2.0 GB of tempdb; tempdb has 4.0 GB free and the data volume has 50.0 GB free.")
	if r.Finding.Owner != OwnerClientIT || r.Finding.ThenCheck != "Run the estimate again: STO10 passed." ||
		r.Finding.DoThis != `Free space on D:\ or let tempdb grow to 2.0 GB. Alternatives: run DBCC CHECKDB WITH PHYSICAL_ONLY (needs far less), or run the full check on a restored copy of the last backup, which also tests the backup. Run it in a quiet period.` {
		t.Errorf("passed finding %+v", r.Finding)
	}
	if r.Finding.Evidence != `Estimate: 1,048,576 KB. Need: 2,048 MB (twice the estimate). TempDB free: 4,096 MB, room to grow to: 20,480 MB. Data volume D:\: 51,200 MB free. Data size: 40,960 MB. The estimate took 1.5 seconds.` {
		t.Errorf("evidence %q", r.Finding.Evidence)
	}

	// Medium: needs more than is free but tempdb can grow.
	in := base
	in.TempdbFreeMB = 500
	r = BuildCheckdbRoom(in)
	wantStorageFinding(t, r.Finding, SeverityMedium, WhenDecideFirst, EffortModerate, "WarehouseLive",
		"CHECKDB on WarehouseLive needs about 2.0 GB of tempdb; tempdb has 500 MB free and would have to grow by 1.5 GB.")

	// High: more than tempdb can reach.
	in.TempdbRoomMB = 1000
	r = BuildCheckdbRoom(in)
	wantStorageFinding(t, r.Finding, SeverityHigh, WhenDecideFirst, EffortModerate, "WarehouseLive",
		"CHECKDB on WarehouseLive needs about 2.0 GB of tempdb, but tempdb can only reach 1,000 MB.")

	// Data volume: under 10% of the data size Medium, under 2% High.
	for free, sev := range map[int64]Severity{4096: "", 4095: SeverityMedium, 820: SeverityMedium, 819: SeverityHigh} {
		in := base
		in.DataFreeMB = free
		r := BuildCheckdbRoom(in)
		if sev == "" {
			if r.Finding.Severity != SeverityInfo {
				t.Errorf("data free %d: %+v", free, r.Finding)
			}
			continue
		}
		wantStorageFinding(t, r.Finding, sev, WhenDecideFirst, EffortModerate, "WarehouseLive",
			"The data volume has "+mbText(free)+" free for a database of 40.0 GB; the snapshot CHECKDB takes needs room to grow while it runs.")
	}

	// Both: one finding, the worse severity, both sentences.
	in = base
	in.TempdbFreeMB, in.DataFreeMB = 500, 100
	r = BuildCheckdbRoom(in)
	wantStorageFinding(t, r.Finding, SeverityHigh, WhenDecideFirst, EffortModerate, "WarehouseLive",
		"CHECKDB on WarehouseLive needs about 2.0 GB of tempdb; tempdb has 500 MB free and would have to grow by 1.5 GB. The data volume has 100 MB free for a database of 40.0 GB; the snapshot CHECKDB takes needs room to grow while it runs.")

	if NeedMB(1) != 1 || NeedMB(512) != 1 || NeedMB(513) != 2 || NeedMB(4604522) != 8994 {
		t.Errorf("NeedMB %d %d %d %d", NeedMB(1), NeedMB(512), NeedMB(513), NeedMB(4604522))
	}
}

func TestCheckdbRoomInputsFrom(t *testing.T) {
	f := sfacts()
	in, err := CheckdbRoomInputsFrom(f)
	// tempdb: 900 MB free; 8 growing files on T:\ with 10 GB free, capped at 10 GB.
	if err != nil || in.TempdbFreeMB != 900 || in.TempdbRoomMB != 900+10240 || in.DataVolume != `D:\` || in.DataFreeMB != 51200 || in.DataSizeMB != 3500 {
		t.Errorf("inputs %+v %v", in, err)
	}
	// A max size caps a file's growth; a file that does not grow adds nothing.
	for i := range f.Files {
		x := &f.Files[i]
		if x.DatabaseID == 2 && x.Type == "ROWS" {
			x.MaxSizePages = 256 * 128 // 256 MB, 128 MB to go
		}
	}
	file(&f, "temp1").Growth = 0
	if in, _ := CheckdbRoomInputsFrom(f); in.TempdbRoomMB != 900+7*128 {
		t.Errorf("capped room %d", in.TempdbRoomMB)
	}
	f = sfacts()
	f.TempdbFreeMB, f.TempdbError = nil, "denied"
	if _, err := CheckdbRoomInputsFrom(f); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Errorf("no tempdb: %v", err)
	}
	f = sfacts()
	f.VolumeError = "no VIEW SERVER STATE"
	if _, err := CheckdbRoomInputsFrom(f); err == nil {
		t.Error("no volumes should fail")
	}
}

func TestCardDetailJSONCheckdbRoom(t *testing.T) {
	storage := CardDetail{Card: Card{ID: CardStorage}, Findings: []Finding{}, Checks: []CheckStatus{}}
	b, _ := json.Marshal(storage)
	if !strings.Contains(string(b), `"checkdbRoom":null`) {
		t.Errorf("storage without a result: %s", b)
	}
	r := BuildCheckdbRoom(CheckdbRoomInputs{Database: "WarehouseLive", EstimateKB: 1024, TempdbFreeMB: 10, TempdbRoomMB: 10, DataFreeMB: 10, DataSizeMB: 10})
	storage.CheckdbRoom = &r
	b, _ = json.Marshal(storage)
	if !strings.Contains(string(b), `"checkdbRoom":{"capturedAt":`) || !strings.Contains(string(b), `"needMB":2`) || strings.Count(string(b), "checkdbRoom") != 1 {
		t.Errorf("storage with a result: %s", b)
	}
	b, _ = json.Marshal(CardDetail{Card: Card{ID: CardBackups}, Findings: []Finding{}, Checks: []CheckStatus{}})
	if strings.Contains(string(b), "checkdbRoom") || !strings.Contains(string(b), `"findings":[]`) {
		t.Errorf("backups: %s", b)
	}
}

func TestBuildCardsWithStorage(t *testing.T) {
	f := sfacts()
	cards := BuildCards(nil, nil, &f, nil, nil, nil, nil)
	if cards[2].ID != CardStorage || cards[2].Status != StatusGreen || len(cards[2].Facts) != 4 || cards[2].Verdict == "" {
		t.Errorf("storage card %+v", cards[2])
	}
	if cards[0].Status != StatusGrey || cards[3].Status != StatusGrey {
		t.Errorf("others grey: %+v %+v", cards[0], cards[3])
	}
}

func TestNumberFormats(t *testing.T) {
	for x, want := range map[float64]string{0: "0", 999.5: "1,000", 1234567: "1,234,567", 2.5: "3", -1234.5: "-1,235"} {
		if got := n0(x); got != want {
			t.Errorf("n0(%v) = %q, want %q", x, got, want)
		}
	}
	for x, want := range map[float64]string{5.8203125: "5.8", 92.35: "92.4", 1234.56: "1,234.6"} {
		if got := n1(x); got != want {
			t.Errorf("n1(%v) = %q, want %q", x, got, want)
		}
	}
	if n0int(9223372036854775807) != "9,223,372,036,854,775,807" {
		t.Error(n0int(9223372036854775807))
	}
}

// TestStorageSTO09PerVolume covers docs/phase3f-contract.md item 17: slow files grouped per volume
// and kind, the rank the worst file's.
func TestStorageSTO09PerVolume(t *testing.T) {
	f := sfacts()
	d2 := file(&f, "WarehouseLive")
	d2.Reads, d2.ReadStallMs = 2000, 50000 // 25 ms
	extra := *d2
	extra.FileID, extra.LogicalName, extra.PhysicalName = 3, "WarehouseLive2", `D:\Data\WarehouseLive2.ndf`
	extra.Reads, extra.ReadStallMs, extra.Writes, extra.WriteStallMs = 1000, 60000, 10, 20 // 60 ms
	quick := extra
	quick.FileID, quick.LogicalName, quick.PhysicalName = 4, "WarehouseLive3", `D:\Data\WarehouseLive3.ndf`
	quick.Reads, quick.ReadStallMs = 5000, 5000 // 1 ms: not slow, not in the group
	f.Files = append(f.Files, extra, quick)
	_, fs, _ := sbuild(f)
	got := allOf(fs, "STO09")
	if len(got) != 1 {
		t.Fatalf("STO09 per volume: %+v", got)
	}
	wantStorageFinding(t, got[0], SeverityHigh, WhenDecideFirst, EffortProject, `D:\`,
		`Data reads average 36 ms on 2 files on D:\ (WarehouseLive2 is the slowest at 60 ms).`)
	if got[0].Evidence != `D:\Data\WarehouseLive.mdf: reads 2000 (avg 25 ms), writes 100 (avg 1 ms); D:\Data\WarehouseLive2.ndf: reads 1000 (avg 60 ms), writes 10 (avg 2 ms).` {
		t.Errorf("evidence %q", got[0].Evidence)
	}

	// Data and log files on one volume: one finding per kind.
	f = sfacts()
	l := file(&f, "WarehouseLive_log")
	l.Volume, l.Writes, l.WriteStallMs = `D:\`, 2000, 30000
	d := file(&f, "WarehouseLive")
	d.Reads, d.ReadStallMs = 2000, 50000
	_, fs, _ = sbuild(f)
	got = allOf(fs, "STO09")
	if len(got) != 2 || got[0].Object != "WarehouseLive" || got[1].Object != "WarehouseLive_log" {
		t.Errorf("one file per kind on one volume: %+v", got)
	}
}

// TestStorageTMP01PageLatch covers item 18: PAGELATCH waits of 1% of uptime or more raise TMP01.
func TestStorageTMP01PageLatch(t *testing.T) {
	keep4 := func(f *StorageFacts) {
		out, data := []StorageFile{}, 0
		for _, x := range f.Files {
			if x.DatabaseID == 2 && x.Type == "ROWS" {
				if data >= 4 {
					continue
				}
				data++
			}
			out = append(out, x)
		}
		f.Files = out
	}
	for _, c := range []struct {
		latchMs int64
		hours   float64
		want    Severity
		finding string
		latch   string
	}{
		{35999, 100, SeverityLow, "TempDB has 4 data files; 8 is recommended for this server.", " PAGELATCH waits: 0.0% of uptime."},
		{3600000, 100, SeverityMedium, "TempDB has 4 data files; 8 is recommended for this server, and the server is likely already queuing on TempDB allocation pages (1.0% of uptime in PAGELATCH waits).", " PAGELATCH waits: 1.0% of uptime."},
		{180000, 0.5, SeverityMedium, "TempDB has 4 data files; 8 is recommended for this server, and the server is likely already queuing on TempDB allocation pages (5.0% of uptime in PAGELATCH waits).", " PAGELATCH waits: 5.0% of uptime."},
	} {
		f := sfacts()
		keep4(&f)
		f.PageLatchMs, f.UptimeHours = i64(c.latchMs), c.hours
		_, fs, _ := sbuild(f)
		x := allOf(fs, "TMP01")
		if len(x) != 1 {
			t.Fatalf("%+v: %+v", c, x)
		}
		wantStorageFinding(t, x[0], c.want, WhenOnline, EffortQuickWin, "tempdb", c.finding)
		if x[0].Evidence != "Data files: 4. Logical CPUs: 8."+c.latch {
			t.Errorf("%+v: evidence %q", c, x[0].Evidence)
		}
	}
	// One file and latch waits: Medium becomes High. Not read: as before.
	f := sfacts()
	out := []StorageFile{}
	for _, x := range f.Files {
		if x.DatabaseID != 2 || x.Type != "ROWS" || x.LogicalName == "temp1" {
			out = append(out, x)
		}
	}
	f.Files = out
	f.PageLatchMs, f.UptimeHours = i64(36000000), 100
	if _, fs, _ := sbuild(f); allOf(fs, "TMP01")[0].Severity != SeverityHigh {
		t.Errorf("one file: %+v", allOf(fs, "TMP01"))
	}
	f.PageLatchError = "denied"
	if _, fs, _ := sbuild(f); allOf(fs, "TMP01")[0].Severity != SeverityMedium || allOf(fs, "TMP01")[0].Evidence != "Data files: 1. Logical CPUs: 8." {
		t.Errorf("not read: %+v", allOf(fs, "TMP01"))
	}

	// Express on 8 vCPUs uses 4 of them: 4 files is the target, and the evidence says why.
	f.OnlineSchedulers = 4
	if _, fs, _ := sbuild(f); allOf(fs, "TMP01")[0].Finding != "TempDB has 1 data file; 4 is recommended for this server." ||
		allOf(fs, "TMP01")[0].Evidence != "Data files: 1. Logical CPUs: 8, of which SQL Server uses 4." {
		t.Errorf("Express: %+v", allOf(fs, "TMP01"))
	}
	// One socket of one core: one file is right, so no finding.
	f.OnlineSchedulers = 1
	if _, fs, _ := sbuild(f); len(allOf(fs, "TMP01")) != 0 {
		t.Errorf("one CPU in use: %+v", allOf(fs, "TMP01"))
	}
}

// TestStorageTMP03Filler covers item 18: what fills tempdb decides the sentence and the Do this.
func TestStorageTMP03Filler(t *testing.T) {
	base := func() StorageFacts {
		f := sfacts()
		f.TempdbAllocMB, f.TempdbFreeMB = i64(4096), i64(1024)
		f.TempdbSessions = []TempdbSession{{SessionID: 57, Login: `CORP\report`, Program: "SSRS", MB: 2100.4}, {SessionID: 61, MB: 300}}
		return f
	}
	f := base()
	f.UserObjectsMB, f.InternalObjectsMB, f.VersionStoreMB = 2500, 400, 100
	_, fs, _ := sbuild(f)
	x := allOf(fs, "TMP03")[0]
	if x.Finding != "TempDB is 75% full (3,072 MB of 4,096 MB). Mostly user objects (temp tables)." || x.DoThis != tmp03DoUser ||
		x.Evidence != `Data 75% used, log 5.0% used. Volume free: 10,240 MB. Can grow: yes. User objects 2,500 MB, internal objects 400 MB, version store 100 MB. Top sessions: session 57 (CORP\report, SSRS) 2,100 MB; session 61 (?, ?) 300 MB.` {
		t.Errorf("user objects %+v", x)
	}

	f = base()
	f.UserObjectsMB, f.InternalObjectsMB, f.VersionStoreMB = 100, 2800, 100
	_, fs, _ = sbuild(f)
	if x = allOf(fs, "TMP03")[0]; !strings.HasSuffix(x.Finding, " Mostly internal objects (sorts, hashes, spools).") || x.DoThis != tmp03DoIntern {
		t.Errorf("internal objects %+v", x)
	}

	f = base()
	f.UserObjectsMB, f.InternalObjectsMB, f.VersionStoreMB = 100, 200, 2700
	f.OldestSnapshot = &SnapshotTransaction{SessionID: 88, ElapsedSeconds: 2520}
	_, fs, _ = sbuild(f)
	x = allOf(fs, "TMP03")[0]
	if !strings.HasSuffix(x.Finding, " Mostly the version store (a transaction has held row versions for 42 minutes).") ||
		!strings.HasPrefix(x.DoThis, "The version store is held by the oldest snapshot transaction: session 88, open for 42 minutes.") ||
		!strings.HasSuffix(x.Evidence, " Oldest snapshot transaction: session 88, 42 minutes.") {
		t.Errorf("version store %+v", x)
	}
	f.OldestSnapshot = nil
	_, fs, _ = sbuild(f)
	if x = allOf(fs, "TMP03")[0]; !strings.HasSuffix(x.Finding, " Mostly the version store.") {
		t.Errorf("version store without the transaction %+v", x)
	}

	// No breakdown read: the sentence and Do this as before.
	f = base()
	f.TempdbSessions = nil
	_, fs, _ = sbuild(f)
	if x = allOf(fs, "TMP03")[0]; x.Finding != "TempDB is 75% full (3,072 MB of 4,096 MB)." || x.DoThis != tmp03Do {
		t.Errorf("no breakdown %+v", x)
	}
}

// TestStorageSlowGrowthAnd2022 covers items 16 and 20: the slowest growth on STO02/STO03 of that
// file, and the 64 MB log growth note on SQL Server 2022 and later.
func TestStorageSlowGrowthAnd2022(t *testing.T) {
	at := time.Date(2026, 11, 2, 21, 0, 0, 0, time.UTC)
	f := sfacts()
	l := file(&f, "WarehouseLive_log")
	l.IsPercentGrowth, l.Growth = true, 10
	d := file(&f, "WarehouseLive")
	d.Growth = 0
	f.SlowGrowth = &SlowGrowth{Ms: 4230, File: "WarehouseLive_log", At: at}
	_, fs, _ := sbuild(f)
	s2 := allOf(fs, "STO02")
	if len(s2) != 1 || s2[0].Evidence != "Current size: 1024 MB. The slowest growth took 4.2 s (WarehouseLive_log, 2026-11-02 21:00)." ||
		!strings.HasSuffix(s2[0].DoThis, sto02Log2022) {
		t.Errorf("STO02 log %+v", s2)
	}
	s3 := allOf(fs, "STO03")
	if len(s3) != 1 || strings.Contains(s3[0].Evidence, "slowest") {
		t.Errorf("STO03 of another file %+v", s3)
	}
	// The slow file is the data file: its STO03 says so; under a second says nothing.
	f.SlowGrowth = &SlowGrowth{Ms: 1500, File: "WarehouseLive", At: at}
	_, fs, _ = sbuild(f)
	if s3 = allOf(fs, "STO03"); !strings.HasSuffix(s3[0].Evidence, " The slowest growth took 1.5 s (WarehouseLive, 2026-11-02 21:00).") {
		t.Errorf("STO03 slow %+v", s3)
	}
	f.SlowGrowth.Ms = 999
	_, fs, _ = sbuild(f)
	if s3 = allOf(fs, "STO03"); strings.Contains(s3[0].Evidence, "slowest") {
		t.Errorf("under a second %+v", s3)
	}
	// Before SQL Server 2022, and on data files, no 64 MB note.
	f = sfacts()
	f.EngineVersion = "15.0.4355.3"
	l = file(&f, "WarehouseLive_log")
	l.IsPercentGrowth, l.Growth = true, 10
	d = file(&f, "WarehouseLive")
	d.IsPercentGrowth, d.Growth = true, 10
	_, fs, _ = sbuild(f)
	for _, x := range allOf(fs, "STO02") {
		if strings.Contains(x.DoThis, "64 MB is instant") {
			t.Errorf("2019: %+v", x)
		}
	}
	f.EngineVersion = "16.0.1000.6"
	_, fs, _ = sbuild(f)
	for _, x := range allOf(fs, "STO02") {
		if strings.Contains(x.DoThis, "64 MB is instant") != (x.Object == "WarehouseLive_log") {
			t.Errorf("2022: %+v", x)
		}
	}
}

// TMP02's second trigger (docs/phase4e-contract.md): TempDB grew 20 or more times in 7 days (Low;
// 100 or more Medium), with the growth numbers in the evidence whenever the trace was read, also
// when the finding fires for unequal files. Both triggers carry the tempdb-files fix.
func TestStorageTMP02Growth(t *testing.T) {
	at := time.Date(2026, 11, 3, 14, 5, 0, 0, time.UTC)
	growth := func(n int) *TraceGrowth {
		return &TraceGrowth{Count: n, Seconds: 4, LogGrows: 3, LastAt: at, SlowestMs: 1250, SlowestFile: "temp1", SlowestAt: at}
	}
	f := sfacts()
	f.TempdbGrowth = growth(19)
	if _, fs, _ := sbuild(f); len(allOf(fs, "TMP02")) != 0 {
		t.Errorf("19 growths: %+v", allOf(fs, "TMP02"))
	}
	f.TempdbGrowth = growth(25)
	_, fs, _ := sbuild(f)
	got := allOf(fs, "TMP02")
	if len(got) != 1 {
		t.Fatalf("25 growths: %+v", got)
	}
	wantStorageFinding(t, got[0], SeverityLow, WhenOnline, EffortQuickWin, "tempdb",
		"TempDB grew 25 times in the last 7 days (22 data, 3 log growths; the slowest took 1.3 s): its files start smaller than the workload needs, so users wait while they grow.")
	if got[0].FixID != FixTempdbFiles || got[0].Why != tmp02GrowWhy || got[0].DoThis != tmp02GrowDo ||
		!strings.HasSuffix(got[0].Evidence, ". TempDB growths in the last 7 days: 25 (22 data, 3 log), 4 s spent growing, the last at 2026-11-03 14:05; the slowest took 1.3 s (temp1, 2026-11-03 14:05).") {
		t.Errorf("TMP02 growth %+v", got[0])
	}
	f.TempdbGrowth = growth(100)
	if _, fs, _ := sbuild(f); allOf(fs, "TMP02")[0].Severity != SeverityMedium {
		t.Errorf("100 growths: %+v", allOf(fs, "TMP02"))
	}

	// Unequal and growing: one finding saying both.
	file(&f, "temp2").SizeMB = 256
	_, fs, _ = sbuild(f)
	got = allOf(fs, "TMP02")
	if len(got) != 1 || got[0].Severity != SeverityMedium || !strings.HasSuffix(got[0].Finding, tmp02BothFinding) ||
		got[0].DoThis != tmp02BothDo || !strings.HasSuffix(got[0].Why, tmp02Why) {
		t.Errorf("both: %+v", got)
	}

	// Unequal, few growths: the unequal finding with the growth numbers; none in the trace or not read
	// says so.
	f.TempdbGrowth = growth(5)
	_, fs, _ = sbuild(f)
	got = allOf(fs, "TMP02")
	if len(got) != 1 || got[0].Finding != tmp02Finding || got[0].Severity != SeverityLow || got[0].FixID != FixTempdbFiles ||
		got[0].DoThis != tmp02Do || !strings.Contains(got[0].Evidence, ". TempDB growths in the last 7 days: 5 (2 data, 3 log)") {
		t.Errorf("unequal: %+v", got)
	}
	f.TempdbGrowth = &TraceGrowth{}
	if _, fs, _ := sbuild(f); !strings.HasSuffix(allOf(fs, "TMP02")[0].Evidence, ". No TempDB growth in the default trace in the last 7 days.") {
		t.Errorf("none: %q", allOf(fs, "TMP02")[0].Evidence)
	}
	f.TempdbGrowth, f.SlowGrowthError = nil, "the default trace is off."
	if _, fs, _ := sbuild(f); !strings.HasSuffix(allOf(fs, "TMP02")[0].Evidence, ". TempDB growth history not read: the default trace is off.") {
		t.Errorf("trace off: %q", allOf(fs, "TMP02")[0].Evidence)
	}
	f.SlowGrowthError = ""
	if _, fs, _ := sbuild(f); !strings.HasSuffix(allOf(fs, "TMP02")[0].Evidence, " / growth 64 MB.") {
		t.Errorf("not read, no reason: %q", allOf(fs, "TMP02")[0].Evidence)
	}
}

// IDX06: a heap of 100 MB or a million rows is a Medium finding ending in the clustered index to
// add; one with no other index says every query reads all of it; a small heap passes.
func TestStorageIDX06(t *testing.T) {
	f := sfacts()
	f.Heaps = []TableSize{
		{Name: "[dbo].[ImportRows]", DataMB: 1434, Rows: 13000000},
		{Name: "[dbo].[Audit]", DataMB: 40, IndexMB: 12, IndexCount: 2, Rows: 1200000},
		{Name: "[dbo].[Small]", DataMB: 99, Rows: 999999},
	}
	_, fs, checks := sbuild(f)
	got := allOf(fs, "IDX06")
	if len(got) != 2 || checkOf(checks, "IDX06").Status != CheckFinding {
		t.Fatalf("IDX06 %+v", got)
	}
	x := got[0]
	if x.Object != "[dbo].[ImportRows]" || x.Severity != SeverityMedium || x.When != WhenWindow || x.Effort != EffortModerate || x.Owner != OwnerUs ||
		x.Finding != "[dbo].[ImportRows] has no index at all (a heap with no nonclustered index): 1.4 GB, 13,000,000 rows." ||
		x.Why != idx06Why+idx06WhyNone || x.DoThis != idx06Do || x.Evidence != "Data 1,434 MB, 13,000,000 rows, no nonclustered index." ||
		x.ThenCheck != "Re-run after adding the clustered index: IDX06 gone." {
		t.Errorf("no index: %+v", x)
	}
	if y := got[1]; y.Finding != "[dbo].[Audit] has no clustered index (it is a heap): 40 MB, 1,200,000 rows." || y.Why != idx06Why ||
		y.Evidence != "Data 40 MB, 1,200,000 rows, 2 nonclustered indexes, 12 MB." {
		t.Errorf("heap with indexes: %+v", y)
	}

	f.Heaps = f.Heaps[2:]
	if _, fs, checks = sbuild(f); len(allOf(fs, "IDX06")) != 0 || checkOf(checks, "IDX06").Status != CheckPassed {
		t.Errorf("small heap: %+v", allOf(fs, "IDX06"))
	}
	f.TablesError = "denied"
	if _, _, checks = sbuild(f); checkOf(checks, "IDX06").Status != CheckSkipped || checkOf(checks, "IDX06").Note != "Could not read the table sizes: denied" {
		t.Errorf("unreadable: %+v", checkOf(checks, "IDX06"))
	}
}
