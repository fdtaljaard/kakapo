package domain

import (
	"errors"
	"strings"
	"testing"
)

func boolp(b bool) *bool { return &b }

// keepTempdbData leaves n of tempdb's data files.
func keepTempdbData(f *StorageFacts, n int) {
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

func stoHasWarning(p FixPreview, prefix string) bool {
	for _, w := range p.Warnings {
		if strings.HasPrefix(w, prefix) {
			return true
		}
	}
	return false
}

// STO02 on the data file: the finding carries the fix, the fixes list carries the file, the preview
// builds the statement with the default 256 MB and the rollback with the percentage.
func TestAutogrowthPercentDataFile(t *testing.T) {
	f := sfacts()
	g := file(&f, "GraniteLive")
	g.IsPercentGrowth, g.Growth = true, 10
	c, fs, _ := sbuild(f)
	x := findingOf(t, fs, "STO02")
	if x.FixID != FixAutogrowth || x.Object != "GraniteLive" || c.Fixable != 1 {
		t.Fatalf("STO02 %+v fixable %d", x, c.Fixable)
	}
	fixes := AvailableStorageFixes(f, fs)
	// The TempDB row's entry is always last (docs/phase4e-contract.md).
	if len(fixes) != 2 || fixes[1].ID != FixTempdbFiles || fixes[0].ID != FixAutogrowth || fixes[0].CheckID != "STO02" || fixes[0].Current != "10%" || fixes[0].Target != "256 MB" ||
		fixes[0].Options[OptionGrowthFile] != "GraniteLive" || len(fixes[0].Options) != 1 {
		t.Errorf("fixes %+v", fixes)
	}

	st := StorageFileState{IFI: boolp(true)}
	p, fix, err := BuildAutogrowthFix(f, st, fixes[0].Options)
	if err != nil {
		t.Fatal(err)
	}
	if p.Statement != "ALTER DATABASE [GraniteLive] MODIFY FILE (NAME = N'GraniteLive', FILEGROWTH = 256MB);" ||
		p.Rollback != "ALTER DATABASE [GraniteLive] MODIFY FILE (NAME = N'GraniteLive', FILEGROWTH = 10%);" {
		t.Errorf("statement %q rollback %q", p.Statement, p.Rollback)
	}
	if len(p.Changes) != 1 || p.Changes[0] != (Drift{"Autogrowth of GraniteLive", "10%", "256 MB"}) || fix.CheckID != "STO02" || fix.Target != "256 MB" {
		t.Errorf("changes %+v fix %+v", p.Changes, fix)
	}
	if len(p.Inputs) != 1 || p.Inputs[0].Name != OptionGrowthMB || p.Inputs[0].Value != "256" || p.Options[OptionGrowthMB] != "256" ||
		p.Options[OptionGrowthFile] != "GraniteLive" {
		t.Errorf("inputs %+v options %+v", p.Inputs, p.Options)
	}
	if !stoHasWarning(p, "Instant file initialization is on") || p.Warnings[len(p.Warnings)-1] != "Needs the ALTER permission on the database GraniteLive." {
		t.Errorf("warnings %q", p.Warnings)
	}
	// IFI off and unknown say so.
	p, _, _ = BuildAutogrowthFix(f, StorageFileState{IFI: boolp(false)}, fixes[0].Options)
	if !stoHasWarning(p, "Instant file initialization is off (INS07): each 256 MB growth") {
		t.Errorf("IFI off %q", p.Warnings)
	}
	p, _, _ = BuildAutogrowthFix(f, StorageFileState{IFIError: "the login does not have VIEW SERVER STATE."}, fixes[0].Options)
	if !stoHasWarning(p, "Could not tell whether instant file initialization is on (the login does not have VIEW SERVER STATE)") {
		t.Errorf("IFI unknown %q", p.Warnings)
	}
	// A growth the user typed.
	p, fix, _ = BuildAutogrowthFix(f, st, map[string]string{OptionGrowthFile: "GraniteLive", OptionGrowthDatabase: "GraniteLive", OptionGrowthMB: " 512 "})
	if !strings.Contains(p.Statement, "FILEGROWTH = 512MB") || fix.Target != "512 MB" || p.Options[OptionGrowthMB] != "512" {
		t.Errorf("512: %q %+v", p.Statement, fix)
	}
}

// A bad growth, a missing or unknown file: the warning and no changes, so Apply is disabled.
func TestAutogrowthBadOptions(t *testing.T) {
	f := sfacts()
	file(&f, "GraniteLive").IsPercentGrowth, file(&f, "GraniteLive").Growth = true, 10
	for _, bad := range []string{"0", "4097", "12.5", "abc", "-1", ""} {
		p, _, err := BuildAutogrowthFix(f, StorageFileState{}, map[string]string{OptionGrowthFile: "GraniteLive", OptionGrowthMB: bad})
		if err != nil || len(p.Changes) != 0 || p.Statement != "" || p.Warnings[0] != growthMBWarn || p.Inputs[0].Warning != growthMBWarn || p.Inputs[0].Value != bad {
			t.Errorf("%q: %v %+v", bad, err, p)
		}
	}
	p, _, err := BuildAutogrowthFix(f, StorageFileState{}, nil)
	if err != nil || len(p.Changes) != 0 || !strings.HasPrefix(p.Warnings[0], "Name the file") {
		t.Errorf("no file: %v %+v", err, p)
	}
	p, _, err = BuildAutogrowthFix(f, StorageFileState{}, map[string]string{OptionGrowthFile: "Nope"})
	if err != nil || len(p.Changes) != 0 || p.Warnings[0] != "There is no file named Nope in GraniteLive." {
		t.Errorf("unknown file: %v %+v", err, p)
	}
	// A file STO02/STO03 did not find: nothing to apply.
	if _, _, err := BuildAutogrowthFix(f, StorageFileState{}, map[string]string{OptionGrowthFile: "GraniteLive_log"}); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("healthy file: %v", err)
	}
}

// STO03 on a log file with growth off and a cap: the unlimited option, default on, lifts the cap;
// the rollback puts growth and cap back. Off, only the growth changes. Log growth on 2022 is instant
// up to 64 MB.
func TestAutogrowthOffAndCapped(t *testing.T) {
	f := sfacts()
	l := file(&f, "GraniteLive_log")
	l.Growth, l.MaxSizePages = 0, 262144 // 2 GB
	_, fs, _ := sbuild(f)
	x := findingOf(t, fs, "STO03")
	if x.FixID != FixAutogrowth {
		t.Fatalf("STO03 %+v", x)
	}
	opts := map[string]string{OptionGrowthFile: "GraniteLive_log", OptionGrowthDatabase: "GraniteLive"}
	p, fix, err := BuildAutogrowthFix(f, StorageFileState{}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if p.Statement != "ALTER DATABASE [GraniteLive] MODIFY FILE (NAME = N'GraniteLive_log', FILEGROWTH = 64MB, MAXSIZE = UNLIMITED);" ||
		p.Rollback != "ALTER DATABASE [GraniteLive] MODIFY FILE (NAME = N'GraniteLive_log', FILEGROWTH = 0, MAXSIZE = 2048MB);" {
		t.Errorf("statement %q\nrollback %q", p.Statement, p.Rollback)
	}
	if len(p.Inputs) != 2 || p.Inputs[1].Name != OptionGrowthUnlimited || p.Inputs[1].Value != "true" || fix.Current != "off" ||
		fix.Target != "64 MB, no size limit" || len(p.Changes) != 2 || p.Changes[1] != (Drift{"Size limit of GraniteLive_log", "2048 MB", "none"}) {
		t.Errorf("inputs %+v changes %+v fix %+v", p.Inputs, p.Changes, fix)
	}
	if !stoHasWarning(p, "On SQL Server 2022 and later a log growth of 64 MB or less is instant") {
		t.Errorf("2022 log %q", p.Warnings)
	}
	opts[OptionGrowthUnlimited] = "false"
	opts[OptionGrowthMB] = "128"
	p, _, _ = BuildAutogrowthFix(f, StorageFileState{}, opts)
	if p.Statement != "ALTER DATABASE [GraniteLive] MODIFY FILE (NAME = N'GraniteLive_log', FILEGROWTH = 128MB);" || len(p.Changes) != 1 ||
		!stoHasWarning(p, "On SQL Server 2022 and later only log growth up to 64 MB is instant; a 128 MB growth") {
		t.Errorf("capped kept: %q %q", p.Statement, p.Warnings)
	}
	f.EngineVersion = "15.0.4000.1"
	p, _, _ = BuildAutogrowthFix(f, StorageFileState{}, opts)
	if !stoHasWarning(p, "New log space is always written with zeros") {
		t.Errorf("2019 log %q", p.Warnings)
	}
	// Growth off but no cap: no unlimited input.
	l.MaxSizePages = -1
	p, _, _ = BuildAutogrowthFix(f, StorageFileState{}, map[string]string{OptionGrowthFile: "GraniteLive_log"})
	if len(p.Inputs) != 1 || strings.Contains(p.Statement, "MAXSIZE") {
		t.Errorf("uncapped: %+v %q", p.Inputs, p.Statement)
	}
}

// A TempDB file found by STO02: the fix targets tempdb, says to keep TempDB's files equal, and its
// options name tempdb.
func TestAutogrowthTempdbFile(t *testing.T) {
	f := sfacts()
	t3 := file(&f, "temp3")
	t3.IsPercentGrowth, t3.Growth = true, 10
	_, fs, _ := sbuild(f)
	fixes := AvailableStorageFixes(f, fs)
	// The percentage growth also makes TempDB's files unequal: TMP02 offers the TempDB file set fix
	// too (docs/phase4e-contract.md).
	if len(fixes) != 2 || fixes[0].Options[OptionGrowthDatabase] != "tempdb" || fixes[0].Options[OptionGrowthFile] != "temp3" ||
		fixes[1].ID != FixTempdbFiles {
		t.Fatalf("fixes %+v", fixes)
	}
	p, _, err := BuildAutogrowthFix(f, StorageFileState{IFI: boolp(true)}, fixes[0].Options)
	if err != nil || p.Statement != "ALTER DATABASE [tempdb] MODIFY FILE (NAME = N'temp3', FILEGROWTH = 256MB);" ||
		!stoHasWarning(p, "This is a TempDB data file") || p.Warnings[len(p.Warnings)-1] != "Needs the ALTER permission on the database tempdb." {
		t.Errorf("tempdb file: %v %q %q", err, p.Statement, p.Warnings)
	}
}

// The read-back compares sys.master_files with the growth (and the cap) the preview set.
func TestAutogrowthReadBack(t *testing.T) {
	opts := map[string]string{OptionGrowthFile: "GraniteLive_log", OptionGrowthDatabase: "GraniteLive", OptionGrowthMB: "64", OptionGrowthUnlimited: "true"}
	st := StorageFileState{Files: []StorageFile{{DatabaseID: 7, Database: "GraniteLive", Type: "LOG", LogicalName: "GraniteLive_log", Growth: 8192, MaxSizePages: 268435456}}}
	if after, ok := AutogrowthReadBack(st, opts); !ok || after != "growth 64 MB, no size limit" {
		t.Errorf("verified: %q %v", after, ok)
	}
	st.Files[0].Growth = 10
	st.Files[0].IsPercentGrowth = true
	if after, ok := AutogrowthReadBack(st, opts); ok || after != "growth 10%, no size limit" {
		t.Errorf("differs: %q %v", after, ok)
	}
	if after, ok := AutogrowthReadBack(StorageFileState{}, opts); ok || after != "the file GraniteLive_log was not found in GraniteLive" {
		t.Errorf("missing: %q %v", after, ok)
	}
	fix := Fix{Current: "off", Target: "64 MB", Options: map[string]string{OptionGrowthFile: "GraniteLive_log", OptionGrowthDatabase: "GraniteLive"}}
	if d := AutogrowthChangeDescription(fix); d != "Autogrowth of GraniteLive_log in GraniteLive: off -> 64 MB" {
		t.Errorf("description %q", d)
	}
}

// TMP01 with 4 files for 8 CPUs: the gap of 4 by default, in the first file's folder with its size
// and growth; the rollback removes them in reverse; the count can be lower, not higher.
func TestAddTempdbFiles(t *testing.T) {
	f := sfacts()
	keepTempdbData(&f, 4)
	c, fs, _ := sbuild(f)
	if x := findingOf(t, fs, "TMP01"); x.FixID != FixAddTempdbFiles || c.Fixable != 1 {
		t.Fatalf("TMP01 %+v", x)
	}
	fixes := AvailableStorageFixes(f, fs)
	if len(fixes) != 2 || fixes[1].ID != FixTempdbFiles || fixes[0].ID != FixAddTempdbFiles || fixes[0].Current != "4 data files" || fixes[0].Target != "8 data files" {
		t.Errorf("fixes %+v", fixes)
	}
	p, fix, err := BuildAddTempdbFilesFix(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantStmt := `ALTER DATABASE tempdb ADD FILE (NAME = N'tempdev5', FILENAME = N'T:\TempDB\tempdev5.ndf', SIZE = 128MB, FILEGROWTH = 64MB);
ALTER DATABASE tempdb ADD FILE (NAME = N'tempdev6', FILENAME = N'T:\TempDB\tempdev6.ndf', SIZE = 128MB, FILEGROWTH = 64MB);
ALTER DATABASE tempdb ADD FILE (NAME = N'tempdev7', FILENAME = N'T:\TempDB\tempdev7.ndf', SIZE = 128MB, FILEGROWTH = 64MB);
ALTER DATABASE tempdb ADD FILE (NAME = N'tempdev8', FILENAME = N'T:\TempDB\tempdev8.ndf', SIZE = 128MB, FILEGROWTH = 64MB);`
	wantBack := `ALTER DATABASE tempdb REMOVE FILE [tempdev8];
ALTER DATABASE tempdb REMOVE FILE [tempdev7];
ALTER DATABASE tempdb REMOVE FILE [tempdev6];
ALTER DATABASE tempdb REMOVE FILE [tempdev5];`
	if p.Statement != wantStmt || p.Rollback != wantBack {
		t.Errorf("statement\n%s\nrollback\n%s", p.Statement, p.Rollback)
	}
	if len(p.Changes) != 5 || p.Changes[0] != (Drift{"TempDB data files", "4", "8"}) ||
		p.Changes[1] != (Drift{"tempdev5", "none", `T:\TempDB\tempdev5.ndf, 128 MB, growth 64 MB`}) || fix.Target != "8 data files" || p.Database != "" {
		t.Errorf("changes %+v fix %+v", p.Changes, fix)
	}
	if got := AddedTempdbFiles(p); strings.Join(got, ",") != "tempdev5,tempdev6,tempdev7,tempdev8" {
		t.Errorf("added %q", got)
	}
	if !stoHasWarning(p, "The new files take 512 MB (4 x 128 MB) on T:\\, which has 10240 MB free.") || !stoHasWarning(p, tempdbAtOnce) ||
		!stoHasWarning(p, "The rollback removes each added file") || p.Warnings[len(p.Warnings)-1] != "Needs the ALTER permission on the database tempdb." ||
		stoHasWarning(p, "The existing TempDB data files are not the same size") {
		t.Errorf("warnings %q", p.Warnings)
	}
	p, fix, _ = BuildAddTempdbFilesFix(f, map[string]string{OptionTempdbCount: "2"})
	if strings.Count(p.Statement, "ADD FILE") != 2 || fix.Target != "6 data files" || p.Options[OptionTempdbCount] != "2" {
		t.Errorf("two: %q %+v", p.Statement, fix)
	}
	for _, bad := range []string{"0", "5", "x", "2.5"} {
		p, _, err := BuildAddTempdbFilesFix(f, map[string]string{OptionTempdbCount: bad})
		if err != nil || len(p.Changes) != 0 || p.Statement != "" || p.Warnings[0] != "Type how many files to add: a whole number from 1 to 4." {
			t.Errorf("count %q: %v %+v", bad, err, p)
		}
	}
}

// Unequal files warn; a volume without room blocks; a name already taken is skipped; no TMP01, no fix.
func TestAddTempdbFilesGuards(t *testing.T) {
	f := sfacts()
	keepTempdbData(&f, 2)
	file(&f, "temp2").SizeMB = 512
	file(&f, "temp1").LogicalName = "tempdev3" // a name the fix would pick
	p, _, err := BuildAddTempdbFilesFix(f, map[string]string{OptionTempdbCount: "1"})
	if err != nil || !stoHasWarning(p, "The existing TempDB data files are not the same size and growth: make the existing files 512 MB first") ||
		!stoHasWarning(p, tempdbSetupFirst) {
		t.Errorf("unequal: %v %q", err, p.Warnings)
	}
	if !strings.Contains(p.Statement, "NAME = N'tempdev4'") {
		t.Errorf("taken name: %q", p.Statement)
	}
	for i := range f.Files {
		if f.Files[i].DatabaseID == 2 {
			f.Files[i].VolumeFreeMB = 200
		}
	}
	p, _, _ = BuildAddTempdbFilesFix(f, nil)
	if len(p.Changes) != 0 || !stoHasWarning(p, `The folder T:\TempDB\ needs 768 MB for the new files (6 x 128 MB) and volume T:\ has 200 MB free`) {
		t.Errorf("no room: %+v", p)
	}
	if _, _, err := BuildAddTempdbFilesFix(sfacts(), nil); !errors.Is(err, ErrFixNotApplicable) {
		t.Errorf("8 files: %v", err)
	}
}

func TestTempdbFilesReadBack(t *testing.T) {
	f := sfacts()
	keepTempdbData(&f, 6)
	st := StorageFileState{Files: f.Files}
	st.Files = append(st.Files, StorageFile{DatabaseID: 2, Type: "ROWS", LogicalName: "tempdev7"})
	if after, ok := TempdbFilesReadBack(st, []string{"tempdev7", "tempdev8"}); ok || after != "7 TempDB data files; not found: tempdev8" {
		t.Errorf("missing: %q", after)
	}
	st.Files = append(st.Files, StorageFile{DatabaseID: 2, Type: "ROWS", LogicalName: "tempdev8"})
	if after, ok := TempdbFilesReadBack(st, []string{"tempdev7", "tempdev8"}); !ok || after != "8 TempDB data files (tempdev7, tempdev8 added)" {
		t.Errorf("verified: %q", after)
	}
}

// A database whose data file has its own name: only that file's entry carries the name, so the card
// page cannot give the data file's finding the log file's options.
func TestAutogrowthOptionsMatchOneFile(t *testing.T) {
	f := sfacts()
	g := file(&f, "GraniteLive")
	g.IsPercentGrowth, g.Growth = true, 10
	file(&f, "GraniteLive_log").Growth = 0
	_, fs, _ := sbuild(f)
	fixes := AvailableStorageFixes(f, fs)
	if len(fixes) != 3 || fixes[2].ID != FixTempdbFiles {
		t.Fatalf("fixes %+v", fixes)
	}
	named := 0
	for _, x := range fixes {
		for _, v := range x.Options {
			if v == "GraniteLive" {
				named++
			}
		}
	}
	if named != 1 {
		t.Errorf("%d entries carry the name GraniteLive: %+v", named, fixes)
	}
}

// tempdbEach changes every TempDB file of a kind ("ROWS" or "LOG").
func tempdbEach(f *StorageFacts, kind string, change func(x *StorageFile)) {
	for i := range f.Files {
		if f.Files[i].DatabaseID == 2 && f.Files[i].Type == kind {
			change(&f.Files[i])
		}
	}
}

// An unequal set (TMP02 on size): the largest rounded up to 64 MB for every data file, SIZE only
// where a file is smaller, every file's growth, the room needed on T:\ and the previous growths as
// the rollback. TMP02 carries the fix; the fixes list it once, with no options.
func TestTempdbFilesUnequal(t *testing.T) {
	f := sfacts()
	file(&f, "temp2").SizeMB = 200
	c, fs, _ := sbuild(f)
	if x := findingOf(t, fs, "TMP02"); x.FixID != FixTempdbFiles || c.Fixable != 1 {
		t.Fatalf("TMP02 %+v fixable %d", x, c.Fixable)
	}
	fixes := AvailableStorageFixes(f, fs)
	if len(fixes) != 1 || fixes[0].ID != FixTempdbFiles || fixes[0].CheckID != "TMP02" || fixes[0].Scope != ScopeServer ||
		fixes[0].Title != "Set up the TempDB files" || !strings.HasPrefix(fixes[0].Options[OptionTempdbSummary], "8 data files of 128 to 200 MB, growth 64 MB; log 256 MB, growth 64 MB.") ||
		fixes[0].Current != "8 data files of 128 to 200 MB, log 256 MB" || fixes[0].Target != "8 data files of 256 MB, growth 64 MB; log 256 MB, growth 64 MB" {
		t.Errorf("fixes %+v", fixes)
	}

	p, fix, err := BuildTempdbFilesFix(f, StorageFileState{IFI: boolp(true)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(p.Statement, "\n")
	if len(lines) != 9 || lines[0] != "ALTER DATABASE tempdb MODIFY FILE (NAME = N'temp1', SIZE = 256MB, FILEGROWTH = 64MB);" ||
		lines[1] != "ALTER DATABASE tempdb MODIFY FILE (NAME = N'temp2', SIZE = 256MB, FILEGROWTH = 64MB);" ||
		lines[8] != "ALTER DATABASE tempdb MODIFY FILE (NAME = N'templog', FILEGROWTH = 64MB);" {
		t.Errorf("statement\n%s", p.Statement)
	}
	backs := strings.Split(p.Rollback, "\n")
	if len(backs) != 9 || backs[0] != "ALTER DATABASE tempdb MODIFY FILE (NAME = N'temp1', FILEGROWTH = 64MB);" {
		t.Errorf("rollback\n%s", p.Rollback)
	}
	if len(p.Changes) != 10 || p.Changes[0] != (Drift{"temp1", "128 MB, growth 64 MB", "256 MB, growth 64 MB"}) ||
		p.Changes[1] != (Drift{"temp2", "200 MB, growth 64 MB", "256 MB, growth 64 MB"}) ||
		p.Changes[8] != (Drift{"templog", "256 MB, growth 64 MB", "256 MB, growth 64 MB"}) ||
		p.Changes[9] != (Drift{"Disk room needed", "", `952 MB on T:\ (10240 MB free)`}) {
		t.Errorf("changes %+v", p.Changes)
	}
	if fix.Current != "8 data files of 128 to 200 MB, log 256 MB" || fix.Target != "8 data files of 256 MB, growth 64 MB; log 256 MB, growth 64 MB" ||
		fix.CheckID != "TMP02" || p.Database != "" {
		t.Errorf("fix %+v", fix)
	}
	if len(p.Inputs) != 4 || p.Inputs[0].Name != OptionTempdbDataSizeMB || p.Inputs[0].Value != "256" || p.Inputs[1].Value != "64" ||
		p.Inputs[2].Name != OptionTempdbLogSizeMB || p.Inputs[2].Value != "256" || p.Inputs[3].Value != "64" || p.Inputs[0].Kind != "text" ||
		p.Options[OptionTempdbDataSizeMB] != "256" || p.Options[OptionTempdbLogGrowthMB] != "64" {
		t.Errorf("inputs %+v options %+v", p.Inputs, p.Options)
	}
	if !stoHasWarning(p, "Instant file initialization is on (INS07)") || !stoHasWarning(p, tempdbSetupAtOnce) || !stoHasWarning(p, tempdbSetupRestart) ||
		!stoHasWarning(p, "The rollback puts the previous growths back. The sizes cannot be rolled back") ||
		p.Warnings[len(p.Warnings)-1] != tempdbSetupPerm || stoHasWarning(p, "The 0 MB") || stoHasWarning(p, "Data file") {
		t.Errorf("warnings %q", p.Warnings)
	}
	// IFI off and unknown say what the increase costs.
	p, _, _ = BuildTempdbFilesFix(f, StorageFileState{IFI: boolp(false)}, nil)
	if !stoHasWarning(p, "Instant file initialization is off (INS07): the 952 MB added to the data files is written with zeros first") {
		t.Errorf("IFI off %q", p.Warnings)
	}
	p, _, _ = BuildTempdbFilesFix(f, StorageFileState{IFIError: "the login does not have VIEW SERVER STATE."}, nil)
	if !stoHasWarning(p, "Could not tell whether instant file initialization is on (the login does not have VIEW SERVER STATE) (INS07); without it the 952 MB") {
		t.Errorf("IFI unknown %q", p.Warnings)
	}
	// Equal files and no growth history: no TMP02, but the fix is still offered (the Storage card's
	// TempDB row pre-sizes a healthy set) and its Current describes the files.
	if _, fix, err := BuildTempdbFilesFix(sfacts(), StorageFileState{}, nil); err != nil || !strings.HasPrefix(fix.Options[OptionTempdbSummary], "8 data files of 128 MB, growth 64 MB; log 256 MB, growth ") {
		t.Errorf("healthy: %v %q", err, fix.Current)
	}
}

// A size typed below a larger file: that file keeps its size and the preview says how to make the
// set equal; a percentage growth rolls back to it; a capped file gets MAXSIZE = UNLIMITED and its
// cap back on rollback.
func TestTempdbFilesKeptAndCapped(t *testing.T) {
	f := sfacts()
	file(&f, "temp2").SizeMB = 256
	t3 := file(&f, "temp3")
	t3.IsPercentGrowth, t3.Growth = true, 10
	file(&f, "temp4").MaxSizePages = 65536 // 512 MB
	p, _, err := BuildTempdbFilesFix(f, StorageFileState{IFI: boolp(true)}, map[string]string{OptionTempdbDataSizeMB: "192"})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(p.Statement, "\n")
	backs := strings.Split(p.Rollback, "\n")
	if lines[1] != "ALTER DATABASE tempdb MODIFY FILE (NAME = N'temp2', FILEGROWTH = 64MB);" ||
		lines[3] != "ALTER DATABASE tempdb MODIFY FILE (NAME = N'temp4', SIZE = 192MB, FILEGROWTH = 64MB, MAXSIZE = UNLIMITED);" ||
		backs[2] != "ALTER DATABASE tempdb MODIFY FILE (NAME = N'temp3', FILEGROWTH = 10%);" ||
		backs[3] != "ALTER DATABASE tempdb MODIFY FILE (NAME = N'temp4', FILEGROWTH = 64MB, MAXSIZE = 512MB);" {
		t.Errorf("statement\n%s\nrollback\n%s", p.Statement, p.Rollback)
	}
	if p.Changes[1] != (Drift{"temp2", "256 MB, growth 64 MB", "kept at 256 MB, growth 64 MB"}) ||
		p.Changes[2] != (Drift{"temp3", "128 MB, growth 10%", "192 MB, growth 64 MB"}) ||
		p.Changes[3] != (Drift{"temp4", "128 MB, growth 64 MB, limit 512 MB", "192 MB, growth 64 MB, no size limit"}) {
		t.Errorf("changes %+v", p.Changes)
	}
	if !stoHasWarning(p, "Data file temp2 (256 MB) is larger than the 192 MB set here and is left as it is: to make the data files equal, raise the size to 256 MB, or shrink that file by hand in a restart window.") ||
		!stoHasWarning(p, "The rollback puts the previous growths and size limits back.") {
		t.Errorf("warnings %q", p.Warnings)
	}
	// Two larger files, and a larger log.
	file(&f, "temp5").SizeMB = 300
	p, _, _ = BuildTempdbFilesFix(f, StorageFileState{}, map[string]string{OptionTempdbDataSizeMB: "192", OptionTempdbLogSizeMB: "128"})
	if !stoHasWarning(p, "Data files temp2 (256 MB) and temp5 (300 MB) are larger than the 192 MB set here and are left as they are: to make the data files equal, raise the size to 300 MB") ||
		!stoHasWarning(p, "The log templog is larger than the 128 MB set here and keeps its size.") || len(p.Changes) == 0 {
		t.Errorf("two kept %q", p.Warnings)
	}
}

// The defaults come from the files: the largest rounded up to 64 MB (64 MB more when a file is
// already that size but a restart recreates it smaller), 512 MB growth from 1 GB; the log at least
// 256 MB, 256 MB growth from 1 GB. A growth of the log says it is zeroed.
func TestTempdbFilesDefaults(t *testing.T) {
	f := sfacts()
	f.TempdbGrowth = &TraceGrowth{Count: 30, LogGrows: 2, SlowestMs: 300}
	tempdbEach(&f, "ROWS", func(x *StorageFile) { x.SizeMB = 520 })
	tempdbEach(&f, "LOG", func(x *StorageFile) { x.SizeMB = 100 })
	p, _, err := BuildTempdbFilesFix(f, StorageFileState{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Options[OptionTempdbDataSizeMB] != "576" || p.Options[OptionTempdbDataGrowthMB] != "64" ||
		p.Options[OptionTempdbLogSizeMB] != "256" || p.Options[OptionTempdbLogGrowthMB] != "64" {
		t.Errorf("small defaults %+v", p.Options)
	}
	if !stoHasWarning(p, "The 156 MB added to the log is written with zeros first") {
		t.Errorf("log zeroed %q", p.Warnings)
	}

	tempdbEach(&f, "ROWS", func(x *StorageFile) { x.SizeMB, x.StartupSizeMB = 1536, 1024 })
	tempdbEach(&f, "LOG", func(x *StorageFile) { x.SizeMB = 1100 })
	for i := range f.Files {
		if f.Files[i].DatabaseID == 2 {
			f.Files[i].VolumeFreeMB = 100000
		}
	}
	p, fix, _ := BuildTempdbFilesFix(f, StorageFileState{}, nil)
	if p.Options[OptionTempdbDataSizeMB] != "1600" || p.Options[OptionTempdbDataGrowthMB] != "512" ||
		p.Options[OptionTempdbLogSizeMB] != "1152" || p.Options[OptionTempdbLogGrowthMB] != "256" ||
		!strings.HasPrefix(p.Statement, "ALTER DATABASE tempdb MODIFY FILE (NAME = N'temp1', SIZE = 1600MB, FILEGROWTH = 512MB);") ||
		fix.Target != "8 data files of 1600 MB, growth 512 MB; log 1152 MB, growth 256 MB" {
		t.Errorf("grown defaults %+v %q", p.Options, p.Statement)
	}
	// The size it is now, typed: no SIZE, and the preview says the starting size stays.
	p, _, _ = BuildTempdbFilesFix(f, StorageFileState{}, map[string]string{OptionTempdbDataSizeMB: "1536"})
	if strings.Contains(p.Statement, "N'temp1', SIZE") ||
		!stoHasWarning(p, "After a restart these files start smaller than the 1536 MB set here: temp1 (at 1024 MB), temp2 (at 1024 MB)") ||
		!strings.Contains(strings.Join(p.Warnings, "|"), "so type 1600 MB or more to make that their starting size.") {
		t.Errorf("same size %q %q", p.Statement, p.Warnings)
	}
	// Without the startup sizes, the largest rounded up as it is.
	tempdbEach(&f, "ROWS", func(x *StorageFile) { x.StartupSizeMB = 0 })
	if p, _, _ := BuildTempdbFilesFix(f, StorageFileState{}, nil); p.Options[OptionTempdbDataSizeMB] != "1536" {
		t.Errorf("unknown startup %+v", p.Options)
	}
}

// A bad value or a volume without room: the warning first and no changes, so Apply is disabled.
func TestTempdbFilesBlocking(t *testing.T) {
	f := sfacts()
	file(&f, "temp2").SizeMB = 200
	for _, c := range []struct{ name, value, warn string }{
		{OptionTempdbDataSizeMB, "63", tempdbDataSizeWarn}, {OptionTempdbDataSizeMB, "262145", tempdbDataSizeWarn},
		{OptionTempdbDataSizeMB, "1 GB", tempdbDataSizeWarn}, {OptionTempdbDataGrowthMB, "4097", tempdbDataGrowthWarn},
		{OptionTempdbLogSizeMB, "", tempdbLogSizeWarn}, {OptionTempdbLogGrowthMB, "10", tempdbLogGrowthWarn},
	} {
		p, _, err := BuildTempdbFilesFix(f, StorageFileState{}, map[string]string{c.name: c.value})
		bad := FixInput{}
		for _, in := range p.Inputs {
			if in.Name == c.name {
				bad = in
			}
		}
		if err != nil || len(p.Changes) != 0 || p.Statement != "" || p.Warnings[0] != c.warn || bad.Warning != c.warn || bad.Value != strings.TrimSpace(c.value) {
			t.Errorf("%s %q: %v %+v", c.name, c.value, err, p)
		}
	}
	tempdbEach(&f, "ROWS", func(x *StorageFile) { x.VolumeFreeMB = 500 })
	tempdbEach(&f, "LOG", func(x *StorageFile) { x.VolumeFreeMB = 500 })
	p, _, err := BuildTempdbFilesFix(f, StorageFileState{}, nil)
	if err != nil || len(p.Changes) != 0 || p.Statement != "" ||
		p.Warnings[0] != `The TempDB files need 952 MB more on T:\ and it has 500 MB free: free space there first (STO01) or type smaller sizes.` {
		t.Errorf("no room: %v %+v", err, p)
	}
	// A volume that could not be read: the room row says so, and it does not block.
	tempdbEach(&f, "ROWS", func(x *StorageFile) { x.Volume, x.VolumeTotalMB = "", 0 })
	tempdbEach(&f, "LOG", func(x *StorageFile) { x.Volume, x.VolumeTotalMB = "", 0 })
	p, _, _ = BuildTempdbFilesFix(f, StorageFileState{}, nil)
	if len(p.Changes) != 10 || p.Changes[9] != (Drift{"Disk room needed", "", "952 MB (kakapo could not read the volume)"}) {
		t.Errorf("unknown volume %+v", p.Changes)
	}
}

// The read-back is every file at least its size with the growth set and no size limit; the change
// check sees a file that changed after a failed batch.
func TestTempdbSetupReadBack(t *testing.T) {
	opts := map[string]string{OptionTempdbDataSizeMB: "256", OptionTempdbDataGrowthMB: "64", OptionTempdbLogSizeMB: "256", OptionTempdbLogGrowthMB: "64"}
	f := sfacts()
	tempdbEach(&f, "ROWS", func(x *StorageFile) { x.SizeMB = 256 })
	st := StorageFileState{Files: f.Files}
	if after, ok := TempdbSetupReadBack(st, opts); !ok || after != "8 data files of 256 MB, growth 64 MB; log 256 MB, growth 64 MB" {
		t.Errorf("verified: %q %v", after, ok)
	}
	f2 := sfacts()
	file(&f2, "temp2").SizeMB = 256
	t3 := file(&f2, "temp3")
	t3.IsPercentGrowth, t3.Growth = true, 10
	st2 := StorageFileState{Files: f2.Files}
	after, ok := TempdbSetupReadBack(st2, opts)
	if ok || !strings.HasPrefix(after, "not as set: temp1 128 MB, growth 64 MB; temp3 128 MB, growth 10%;") {
		t.Errorf("differs: %q %v", after, ok)
	}
	if after, ok := TempdbSetupReadBack(StorageFileState{}, opts); ok || after != "TempDB's files were not found" {
		t.Errorf("none: %q", after)
	}
	if after, ok := TempdbSetupReadBack(st, nil); ok || after != "the preview's options are incomplete" {
		t.Errorf("no options: %q", after)
	}

	// The preview's current values against a read-back.
	file(&f2, "temp3").SizeMB = 128
	f2.TempdbGrowth = &TraceGrowth{}
	p, fix, err := BuildTempdbFilesFix(f2, StorageFileState{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if TempdbSetupChanged(p, StorageFileState{Files: f2.Files}) {
		t.Error("nothing ran, yet a change was seen")
	}
	if !TempdbSetupChanged(p, StorageFileState{Files: f.Files}) {
		t.Error("the files grew, yet no change was seen")
	}
	if d := TempdbSetupChangeDescription(fix); d != "TempDB files: 8 data files of 128 to 256 MB, log 256 MB -> 8 data files of 256 MB, growth 64 MB; log 256 MB, growth 64 MB" {
		t.Errorf("description %q", d)
	}
}
