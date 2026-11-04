package domain

import (
	"strings"
	"testing"
	"time"
)

// Tests of docs/phase3b-contract.md, "Addendum (2026-11-03)": A (free space inside the files,
// STO03 High), B (STO11 and the Log fact), C (samples and STO12) and D (STO10 wording).

func TestStorageSizeFact(t *testing.T) {
	f := sfacts()
	file(&f, "GraniteLive").UsedMB = nil
	if card, _, _ := sbuild(f); factOf(card, "Database size").Value != "4.0 GB allocated" {
		t.Errorf("used not read: %+v", factOf(card, "Database size"))
	}
	f = sfacts()
	f.Files = append(f.Files, StorageFile{DatabaseID: 7, Database: "GraniteLive", FileID: 3, Type: "ROWS", LogicalName: "GraniteLive2",
		PhysicalName: `D:\Data\GraniteLive2.ndf`, SizeMB: 2048, Growth: 32768, MaxSizePages: -1, UsedMB: i64(100),
		Volume: `D:\`, VolumeTotalMB: 102400, VolumeFreeMB: 51200})
	if card, _, _ := sbuild(f); factOf(card, "Database size") != (Fact{"Database size", "6.0 GB allocated, 3.5 GB used", ToneNormal}) {
		t.Errorf("two data files: %+v", factOf(card, "Database size"))
	}
}

func TestStorageSTO03Full(t *testing.T) {
	for _, c := range []struct {
		used int64
		want Severity
	}{{3687, SeverityHigh}, {3686, SeverityLow}} { // 90.01% and 89.99% of 4096 MB
		f := sfacts()
		d := file(&f, "GraniteLive")
		d.Growth, d.UsedMB = 0, i64(c.used)
		_, fs, _ := sbuild(f)
		got := allOf(fs, "STO03")
		if len(got) != 1 {
			t.Fatalf("%d: %+v", c.used, got)
		}
		if c.want == SeverityLow {
			wantStorageFinding(t, got[0], SeverityLow, WhenOnline, EffortQuickWin, "GraniteLive", "File GraniteLive has autogrowth switched off.")
			if got[0].Why != sto03WhyOff {
				t.Errorf("89.99%%: why %q", got[0].Why)
			}
			continue
		}
		wantStorageFinding(t, got[0], SeverityHigh, WhenOnline, EffortQuickWin, "GraniteLive", "File GraniteLive has autogrowth switched off and is 90% full.")
		if got[0].Why != "Once the file is full, inserts and updates fail." ||
			got[0].DoThis != "ALTER DATABASE [GraniteLive] MODIFY FILE (NAME = N'GraniteLive', FILEGROWTH = 256MB);" ||
			got[0].Evidence != "Current size: 4096 MB, 90.0% used." {
			t.Errorf("90%%: %+v", got[0])
		}
	}

	// The log uses used_log_space_in_percent; tempdb's log uses tempdb's.
	for pct, want := range map[float64]Severity{90: SeverityHigh, 89.9: SeverityLow} {
		f := sfacts()
		f.LogUsedPct, f.TempdbLogUsedPct = f64(pct), f64(pct)
		file(&f, "GraniteLive_log").Growth = 0
		file(&f, "templog").Growth = 0
		_, fs, _ := sbuild(f)
		got := allOf(fs, "STO03")
		if len(got) != 2 || got[0].Severity != want || got[1].Severity != want {
			t.Fatalf("log at %v%%: %+v", pct, got)
		}
		if want == SeverityHigh && (got[0].Finding != "File GraniteLive_log has autogrowth switched off and is 90% full." ||
			got[1].Finding != "File templog has autogrowth switched off and is 90% full.") {
			t.Errorf("log sentences %q %q", got[0].Finding, got[1].Finding)
		}
	}

	// Small growth is Low however full the file is.
	f := sfacts()
	d := file(&f, "GraniteLive")
	d.Growth, d.SizeMB, d.UsedMB = 4096, 1024, i64(1024)
	if _, fs, _ := sbuild(f); len(allOf(fs, "STO03")) != 1 || allOf(fs, "STO03")[0].Severity != SeverityLow {
		t.Errorf("small growth, full: %+v", allOf(fs, "STO03"))
	}
}

// logFacts is sfacts with a 10 GB log, pct used and waiting on reuse; noGrow switches its growth off.
func logFacts(pct float64, reuse string, noGrow bool) StorageFacts {
	f := sfacts()
	f.LogSizeMB, f.LogUsedPct, f.LogReuseWait = i64(10240), f64(pct), reuse
	l := file(&f, "GraniteLive_log")
	l.SizeMB = 10240
	file(&f, "GraniteLive").SizeMB = 20480 // keep STO07 out of the way
	if noGrow {
		l.MaxSizePages = 10240 * 128 // at its max size
	}
	return f
}

func TestStorageSTO11(t *testing.T) {
	for _, c := range []struct {
		pct     float64
		reuse   string
		noGrow  bool
		want    Severity
		finding string
		tone    string
	}{
		{79, "LOG_BACKUP", false, "", "", ToneNormal},
		{79, "NOTHING", true, "", "", ToneNormal},
		{80, "NOTHING", false, "", "", ToneBad},
		{80, "LOG_BACKUP", false, SeverityMedium, "The transaction log is 80% full (8.0 GB of 10.0 GB) and is waiting on a log backup.", ToneBad},
		{80, "NOTHING", true, SeverityMedium, "The transaction log is 80% full (8.0 GB of 10.0 GB) and is waiting on nothing: the space is reused at the next checkpoint.", ToneBad},
		{89.9, "NOTHING", true, SeverityMedium, "The transaction log is 89% full (9.0 GB of 10.0 GB) and is waiting on nothing: the space is reused at the next checkpoint.", ToneBad},
		{90, "NOTHING", false, "", "", ToneWorse},
		{90, "LOG_BACKUP", false, SeverityMedium, "The transaction log is 90% full (9.0 GB of 10.0 GB) and is waiting on a log backup.", ToneWorse},
		{90, "LOG_BACKUP", true, SeverityHigh, "The transaction log is 90% full (9.0 GB of 10.0 GB) and is waiting on a log backup. It cannot grow.", ToneWorse},
		{90, "NOTHING", true, SeverityHigh, "The transaction log is 90% full (9.0 GB of 10.0 GB) and is waiting on nothing: the space is reused at the next checkpoint. It cannot grow.", ToneWorse},
	} {
		card, fs, checks := sbuild(logFacts(c.pct, c.reuse, c.noGrow))
		got := allOf(fs, "STO11")
		if lf := factOf(card, "Log"); lf.Tone != c.tone {
			t.Errorf("%+v: Log fact %+v", c, lf)
		}
		if c.want == "" {
			if len(got) != 0 || checkOf(checks, "STO11").Status != CheckPassed {
				t.Errorf("%+v: unexpected %+v", c, got)
			}
			continue
		}
		if len(got) != 1 {
			t.Fatalf("%+v: %+v", c, got)
		}
		wantStorageFinding(t, got[0], c.want, WhenOnline, EffortQuickWin, "GraniteLive", c.finding)
		if got[0].Owner != OwnerUs || got[0].Why != sto11Why || got[0].ThenCheck != "Re-run: STO11 gone and the log under 50% used." ||
			checkOf(checks, "STO11").Status != CheckFinding {
			t.Errorf("%+v: %+v", c, got[0])
		}
		wantDo := sto07Do(c.reuse)
		if c.noGrow {
			wantDo += ` Free space on L:\ or raise the file's growth or maximum size.`
		}
		if got[0].DoThis != wantDo {
			t.Errorf("%+v: do %q", c, got[0].DoThis)
		}
	}

	_, fs, _ := sbuild(logFacts(90, "LOG_BACKUP", true))
	if e := allOf(fs, "STO11")[0].Evidence; e != `Size: 10,240 MB. Used: 90.0% (9,216 MB). Reuse wait: LOG_BACKUP. Growth: 256 MB. Max size: 10,240 MB. Volume free: L:\ 40,960 MB.` {
		t.Errorf("evidence %q", e)
	}

	// Every way the log cannot grow: growth off, at its max size, too little room for one step;
	// a percentage step is taken on the current size.
	for name, change := range map[string]func(l *StorageFile) (canGrow bool){
		"growth off":            func(l *StorageFile) bool { l.Growth = 0; return false },
		"at max size":           func(l *StorageFile) bool { l.MaxSizePages = l.SizeMB * 128; return false },
		"under max size":        func(l *StorageFile) bool { l.MaxSizePages = (l.SizeMB + 1) * 128; return true },
		"2 TB unlimited":        func(l *StorageFile) bool { l.MaxSizePages = 268435456; return true },
		"volume under one step": func(l *StorageFile) bool { l.VolumeFreeMB = 255; return false },
		"volume one step":       func(l *StorageFile) bool { l.VolumeFreeMB = 256; return true },
		"percent step too big":  func(l *StorageFile) bool { l.IsPercentGrowth, l.Growth, l.VolumeFreeMB = true, 10, 1023; return false },
		"percent step fits":     func(l *StorageFile) bool { l.IsPercentGrowth, l.Growth, l.VolumeFreeMB = true, 10, 1024; return true },
	} {
		f := logFacts(95, "NOTHING", false)
		canGrow := change(file(&f, "GraniteLive_log"))
		_, fs, _ := sbuild(f)
		got := allOf(fs, "STO11")
		if canGrow != (len(got) == 0) || (!canGrow && got[0].Severity != SeverityHigh) {
			t.Errorf("%s: %+v", name, got)
		}
	}

	// Not read: skipped with the reason, the fact says so.
	f := sfacts()
	f.LogSizeMB, f.LogUsedPct, f.LogSpaceError = nil, nil, "denied"
	card, _, checks := sbuild(f)
	if checkOf(checks, "STO11") != (CheckStatus{"STO11", "Transaction log usage", CheckSkipped, "Could not read the log space used (sys.dm_db_log_space_usage): denied"}) ||
		factOf(card, "Log").Value != "could not be read" {
		t.Errorf("not read: %+v %+v", checkOf(checks, "STO11"), factOf(card, "Log"))
	}
}

func TestLogReuseReasons(t *testing.T) {
	for reuse, want := range map[string]string{
		"LOG_BACKUP":           "a log backup",
		"ACTIVE_TRANSACTION":   "a transaction that is still open",
		"REPLICATION":          "replication or CDC",
		"AVAILABILITY_REPLICA": "an Availability Group replica",
		"NOTHING":              "nothing: the space is reused at the next checkpoint",
		"OLDEST_PAGE":          "oldest_page",
	} {
		if got := LogReuseReason(reuse); got != want {
			t.Errorf("%s: %q", reuse, got)
		}
		_, fs, _ := sbuild(logFacts(85, reuse, false))
		if reuse == "NOTHING" {
			continue
		}
		if x := allOf(fs, "STO11"); len(x) != 1 || !strings.HasSuffix(x[0].Finding, "and is waiting on "+want+".") || x[0].DoThis != sto07Do(reuse) {
			t.Errorf("%s: %+v", reuse, x)
		}
	}
}

func TestStorageLogFact(t *testing.T) {
	for _, c := range []struct {
		pct   float64
		reuse string
		want  Fact
	}{
		{10, "NOTHING", Fact{"Log", "10.0 GB, 10% used", ToneNormal}},
		{79.9, "LOG_BACKUP", Fact{"Log", "10.0 GB, 79% used, waiting on a log backup", ToneNormal}},
		{80, "ACTIVE_TRANSACTION", Fact{"Log", "10.0 GB, 80% used, waiting on a transaction that is still open", ToneBad}},
		{90, "NOTHING", Fact{"Log", "10.0 GB, 90% used", ToneWorse}},
	} {
		card, _, _ := sbuild(logFacts(c.pct, c.reuse, false))
		if len(card.Facts) != 4 || card.Facts[2] != c.want {
			t.Errorf("%v %s: %+v", c.pct, c.reuse, card.Facts)
		}
	}
}

var trendAt = time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)

// trend is a pair of D:\ samples days apart, from oldFree to newFree.
func trend(days float64, oldFree, newFree int64) *DiskTrend {
	return &DiskTrend{Volume: `D:\`, OldestAt: trendAt.Add(-time.Duration(days * 24 * float64(time.Hour))), OldestFreeMB: oldFree,
		NewestAt: trendAt, NewestFreeMB: newFree}
}

func TestStorageSTO12(t *testing.T) {
	build := func(tr *DiskTrend) (Card, []Finding, []CheckStatus) {
		return BuildStorageCard(sfacts(), storageDef, nil, tr)
	}

	// No pair a day apart: skipped, saying when the first was kept.
	for name, tr := range map[string]*DiskTrend{"nil": nil, "one sample": trend(0, 51200, 51200), "23 hours": trend(23.0/24, 52000, 51200)} {
		card, fs, checks := build(tr)
		note := "Needs samples a day or more apart."
		if tr != nil {
			note = "Needs samples a day or more apart; the first was kept " + tr.OldestAt.Format("2006-01-02 15:04") + " UTC."
		}
		if checkOf(checks, "STO12") != (CheckStatus{"STO12", "Disk growth forecast", CheckSkipped, note}) || len(allOf(fs, "STO12")) != 0 ||
			factOf(card, "Tightest drive").Value != `D:\ 50% free (50 GB)` {
			t.Errorf("%s: %+v", name, checkOf(checks, "STO12"))
		}
	}
	if _, _, checks := build(trend(23.0/24, 52000, 51200)); checkOf(checks, "STO12").Note != "Needs samples a day or more apart; the first was kept 2026-11-02 09:00 UTC." {
		t.Errorf("first kept %q", checkOf(checks, "STO12").Note)
	}

	// Space freed: passed, with how much a day.
	_, fs, checks := build(trend(2, 50176, 51200))
	got := allOf(fs, "STO12")
	if len(got) != 1 || got[0].Severity != SeverityInfo || checkOf(checks, "STO12").Status != CheckPassed ||
		!strings.HasPrefix(got[0].Evidence, "512 MB freed per day over 2 days.") {
		t.Errorf("freed: %+v %+v", got, checkOf(checks, "STO12"))
	}

	// 1 GB a day over 10 days: 29 days left Medium, 6 High, 30 passed.
	for _, c := range []struct {
		left int64
		want Severity
		tone string
	}{{29, SeverityMedium, ToneBad}, {6, SeverityHigh, ToneWorse}, {7, SeverityMedium, ToneBad}, {30, SeverityInfo, ToneNormal}} {
		newFree := c.left * 1024
		card, fs, checks := build(trend(10, newFree+10240, newFree))
		got := allOf(fs, "STO12")
		if len(got) != 1 {
			t.Fatalf("%d days: %+v", c.left, got)
		}
		sentence := "At the rate of the last 10 days (1.0 GB a day), D:\\ will be full in about " + itoa(c.left) + " days."
		wantStorageFinding(t, got[0], c.want, WhenDecideFirst, EffortModerate, `D:\`, sentence)
		if got[0].Owner != OwnerClientIT || got[0].Why != sto12Why || got[0].DoThis != sto12Do || got[0].ThenCheck != "Re-run after a week: the forecast is longer." {
			t.Errorf("%d days: %+v", c.left, got[0])
		}
		drive := factOf(card, "Tightest drive")
		if c.want == SeverityInfo {
			if checkOf(checks, "STO12").Status != CheckPassed || !strings.HasPrefix(got[0].Evidence, "At 1.0 GB/day, full in about 30 days.") ||
				drive.Value != `D:\ 50% free (50 GB)` || drive.Tone != ToneNormal {
				t.Errorf("30 days: %+v %+v %+v", got[0], checkOf(checks, "STO12"), drive)
			}
			continue
		}
		if checkOf(checks, "STO12").Status != CheckFinding || drive.Value != `D:\ 50% free (50 GB), full in ~`+itoa(c.left)+` days` || drive.Tone != c.tone {
			t.Errorf("%d days: %+v %+v", c.left, checkOf(checks, "STO12"), drive)
		}
		if card.ForClientIT != 1 {
			t.Errorf("%d days: for Client IT %d", c.left, card.ForClientIT)
		}
	}

	// The drive's tone is the worse of STO01 and STO12.
	f := sfacts()
	file(&f, "GraniteLive").VolumeFreeMB = 15000 // STO01 Medium
	card, _, _ := BuildStorageCard(f, storageDef, nil, trend(10, 6144+10240, 6144))
	if d := factOf(card, "Tightest drive"); d.Tone != ToneWorse || !strings.HasSuffix(d.Value, ", full in ~6 days") {
		t.Errorf("worse of both: %+v", d)
	}

	// Volumes unreadable: skipped with that reason.
	f = sfacts()
	f.VolumeError = "denied"
	if _, _, checks := BuildStorageCard(f, storageDef, nil, trend(10, 20000, 10000)); checkOf(checks, "STO12").Note != "Could not read the volumes (sys.dm_os_volume_stats): denied" {
		t.Errorf("volumes: %+v", checkOf(checks, "STO12"))
	}
}

func TestStorageSamples(t *testing.T) {
	f := sfacts()
	vols, s := f.Samples()
	if len(vols) != 2 || vols[0] != (VolumeSample{`D:\`, 51200, 102400}) || vols[1] != (VolumeSample{`L:\`, 40960, 51200}) {
		t.Errorf("volumes %+v", vols)
	}
	if s == nil || *s.DataAllocMB != 4096 || *s.DataUsedMB != 3500 || *s.LogSizeMB != 1024 || *s.LogUsedPct != 10 ||
		*s.TempdbAllocMB != 1024 || *s.TempdbUsedMB != 124 {
		t.Errorf("sample %+v", s)
	}
	if v, ok := f.TightestVolume(); !ok || v != `D:\` {
		t.Errorf("tightest %q %v", v, ok)
	}
	file(&f, "GraniteLive_log").VolumeFreeMB = 1000 // L:\ is now the tightest
	if v, _ := f.TightestVolume(); v != `L:\` {
		t.Errorf("tightest %q", v)
	}

	f = sfacts()
	f.VolumeError, f.SpaceUsedError, f.LogSpaceError = "denied", "denied", "denied"
	f.LogSizeMB, f.LogUsedPct = nil, nil
	vols, s = f.Samples()
	if len(vols) != 0 || s == nil || s.DataUsedMB != nil || s.LogUsedPct != nil || *s.LogSizeMB != 1024 || *s.DataAllocMB != 4096 {
		t.Errorf("partly read: %+v %+v", vols, s)
	}
	if _, ok := f.TightestVolume(); ok {
		t.Error("no tightest volume without the volumes")
	}

	f = sfacts()
	f.State = "OFFLINE"
	if vols, s := f.Samples(); vols != nil || s != nil {
		t.Errorf("offline: %+v %+v", vols, s)
	}
}

func TestSTO10Wording(t *testing.T) {
	r := BuildCheckdbRoom(CheckdbRoomInputs{Database: "GraniteLive", EstimateKB: 1024, TempdbFreeMB: 10, TempdbRoomMB: 10,
		DataVolume: `D:\`, DataFreeMB: 10, DataSizeMB: 10})
	if !strings.HasSuffix(r.Finding.Why, " The snapshot grows with every change made while the check runs, so the figure is an estimate; run CHECKDB in a quiet period.") ||
		!strings.HasSuffix(r.Finding.DoThis, "which also tests the backup. Run it in a quiet period.") {
		t.Errorf("STO10 texts %q %q", r.Finding.Why, r.Finding.DoThis)
	}
}
