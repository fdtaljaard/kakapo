package domain

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// The Storage card's fixes (docs/phase4d-contract.md, part 2): a fixed autogrowth on one file (STO02,
// STO03) and more TempDB data files (TMP01), both through the fix registry; and the TempDB file set
// (TMP02, docs/phase4e-contract.md part 1).
const (
	FixAutogrowth     = "autogrowth"
	FixAddTempdbFiles = "add-tempdb-files"
	FixTempdbFiles    = "tempdb-files"
)

// The options of the storage fixes. file and database name the file a finding is about (passed in
// the fix's options); growthMB, unlimited and count are the inputs.
const (
	OptionGrowthFile      = "file"
	OptionGrowthDatabase  = "database"
	OptionGrowthMB        = "growthMB"
	OptionGrowthUnlimited = "unlimited"
	OptionTempdbCount     = "count"
)

// Limits and defaults of the autogrowth fix.
const (
	defaultDataGrowthMB = 256
	defaultLogGrowthMB  = 64
	maxGrowthMB         = 4096
)

// StorageFileState is what the storage fixes read besides the storage facts: the files of the
// database and of tempdb as they are now (tempdb's current sizes), instant file initialization and
// the engine version. It is also their read-back.
type StorageFileState struct {
	Files         []StorageFile // the database's, then tempdb's; Database is set on each
	IFI           *bool         // instant_file_initialization_enabled; nil when not read
	IFIError      string        // why IFI was not read
	EngineVersion string
}

// Texts of the storage fixes.
const (
	growthMBHelp     = "How much the file grows each time it is full, in MB (1 to 4096). 256 MB for a data file and 64 MB for a log file suit most databases."
	growthMBWarn     = "Type a growth in whole MB from 1 to 4096."
	unlimitedHelp    = "The file also has a size limit. Leave this ticked unless someone set that limit on purpose (to protect the disk)."
	tempdbCountWarn  = "Type how many files to add: a whole number from 1 to %d."
	growthAtOnce     = "Takes effect at once and online: nothing is moved or rewritten; the next growth uses the new step."
	tempdbAtOnce     = "Takes effect at once: SQL Server starts using the new files straight away, without a restart."
	tempdbRemoveNote = "The rollback removes each added file with ALTER DATABASE tempdb REMOVE FILE, which works only while the file is empty: run DBCC SHRINKFILE (N'<file>', EMPTYFILE) first, or remove it right after a restart (TempDB starts empty)."
	tempdbSetupFirst = "\"Set up the TempDB files\" (on the TMP02 finding) makes the existing files equal: apply it first, then add the files."
)

// storageFindings are the Storage card's findings on this read, with their fixes.
func storageFindings(f StorageFacts) []Finding {
	def, _ := FindCardDef(CardStorage)
	_, fs, _ := BuildStorageCard(f, def, nil, nil)
	return fs
}

// growthFile finds a file of the database (or of tempdb when fileDB is tempdb) by its logical name.
func growthFile(f StorageFacts, fileDB, name string) (StorageFile, bool) {
	files := f.DatabaseFiles()
	if strings.EqualFold(fileDB, "tempdb") {
		files = f.TempdbFiles()
	}
	for _, x := range files {
		if strings.EqualFold(x.LogicalName, name) {
			if x.Database == "" {
				x.Database = fileDB
			}
			return x, true
		}
	}
	return StorageFile{}, false
}

// fileForFinding is the file an STO02 or STO03 finding is about: the database's file of that name,
// else tempdb's.
func fileForFinding(f StorageFacts, x Finding) (StorageFile, bool) {
	if s, ok := growthFile(f, f.Database, x.Object); ok {
		return s, true
	}
	return growthFile(f, "tempdb", x.Object)
}

// growthText writes a file's autogrowth as a fix shows it: "10%", "off", "64 MB", "512 KB".
func growthText(x StorageFile) string {
	switch {
	case x.IsPercentGrowth:
		return fmt.Sprintf("%d%%", x.Growth)
	case x.Growth <= 0:
		return "off"
	}
	return pagesText(x.Growth)
}

// pagesText writes 8 KB pages as MB, or KB when they are not whole MB.
func pagesText(pages int64) string {
	if pages*8%1024 == 0 {
		return fmt.Sprintf("%d MB", pages*8/1024)
	}
	return fmt.Sprintf("%d KB", pages*8)
}

// pagesSetting writes 8 KB pages as a FILEGROWTH or MAXSIZE value: 256MB, or 520KB.
func pagesSetting(pages int64) string {
	if pages*8%1024 == 0 {
		return fmt.Sprintf("%dMB", pages*8/1024)
	}
	return fmt.Sprintf("%dKB", pages*8)
}

// growthSetting is a file's growth as an ALTER DATABASE ... MODIFY FILE setting.
func growthSetting(x StorageFile) string {
	switch {
	case x.IsPercentGrowth:
		return fmt.Sprintf("FILEGROWTH = %d%%", x.Growth)
	case x.Growth <= 0:
		return "FILEGROWTH = 0"
	}
	return "FILEGROWTH = " + pagesSetting(x.Growth)
}

// maxSizeSetting is a file's max size as a MAXSIZE value.
func maxSizeSetting(pages int64) string {
	if pages == -1 {
		return "UNLIMITED"
	}
	return pagesSetting(pages)
}

// parseWholeNumber reads a whole number from min to max.
func parseWholeNumber(v string, lo, hi int) (int, bool) {
	v = strings.TrimSpace(v)
	n, err := strconv.Atoi(v)
	if err != nil || !allDigits(v) || n < lo || n > hi {
		return 0, false
	}
	return n, true
}

// autogrowthDefaultMB is the growth the fix suggests: 256 MB for a data file, 64 MB for a log.
func autogrowthDefaultMB(x StorageFile) int {
	if x.isLog() {
		return defaultLogGrowthMB
	}
	return defaultDataGrowthMB
}

// offersUnlimited says whether the fix offers to lift the file's size limit: STO03 found growth
// switched off on a file that is also capped.
func offersUnlimited(x StorageFile) bool {
	return !x.IsPercentGrowth && x.Growth == 0 && maxSizeSet(x.MaxSizePages)
}

// autogrowthFinding is the STO02 or STO03 finding about a file on this read.
func autogrowthFinding(fs []Finding, x StorageFile) (Finding, bool) {
	for _, y := range fs {
		if y.FixID == FixAutogrowth && strings.EqualFold(y.Object, x.LogicalName) {
			return y, true
		}
	}
	return Finding{}, false
}

// BuildAutogrowthFix builds the preview of "Set a fixed autogrowth" on one file from the storage
// facts, the file state read for the fix (instant file initialization) and the options: the file
// (file, and database for a TempDB file), the growth in MB and, when growth is switched off on a
// capped file, whether to lift the cap. ErrFixNotApplicable when the file exists but neither STO02
// nor STO03 fired on it. A missing file or a bad growth gives the warning and no changes.
func BuildAutogrowthFix(f StorageFacts, st StorageFileState, options map[string]string) (FixPreview, Fix, error) {
	d, _ := FindFixDef(FixAutogrowth)
	fix := Fix{ID: d.ID, Title: d.Title, Scope: d.Scope, CheckID: d.CheckID, Setting: d.Setting}
	name := strings.TrimSpace(options[OptionGrowthFile])
	fileDB := strings.TrimSpace(options[OptionGrowthDatabase])
	if fileDB == "" {
		fileDB = f.Database
	}
	version := f.EngineVersion
	if version == "" {
		version = st.EngineVersion
	}
	p := FixPreview{Database: f.Database, EngineVersion: version, Changes: []Drift{}, Warnings: []string{},
		Options: map[string]string{OptionGrowthFile: name, OptionGrowthDatabase: fileDB}}
	if name == "" {
		p.Warnings = append(p.Warnings, "Name the file to change: its logical name, as the STO02 or STO03 finding shows it.")
		return p, fix, nil
	}
	x, ok := growthFile(f, fileDB, name)
	if !ok {
		p.Warnings = append(p.Warnings, fmt.Sprintf("There is no file named %s in %s.", name, fileDB))
		return p, fix, nil
	}
	finding, fired := autogrowthFinding(storageFindings(f), x)
	if !fired {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	fix.CheckID = finding.CheckID
	fix.Current = growthText(x)
	fix.Options = map[string]string{OptionGrowthFile: x.LogicalName, OptionGrowthDatabase: x.Database}
	p.Options[OptionGrowthFile] = x.LogicalName
	p.Options[OptionGrowthDatabase] = x.Database

	usable := true
	mb := autogrowthDefaultMB(x)
	in := FixInput{Name: OptionGrowthMB, Label: "Grow by (MB)", Help: growthMBHelp, Kind: "text", Value: strconv.Itoa(mb)}
	if typed, given := options[OptionGrowthMB]; given {
		if n, ok := parseWholeNumber(typed, 1, maxGrowthMB); ok {
			mb = n
			in.Value = strconv.Itoa(n)
		} else {
			in.Value, in.Warning = strings.TrimSpace(typed), growthMBWarn
			p.Warnings = append(p.Warnings, growthMBWarn)
			usable = false
		}
	}
	p.Inputs = []FixInput{in}
	p.Options[OptionGrowthMB] = in.Value

	unlimited := false
	if offersUnlimited(x) {
		unlimited = boolOption(options, OptionGrowthUnlimited)
		p.Inputs = append(p.Inputs, FixInput{Name: OptionGrowthUnlimited, Label: "Lift the size limit of " + pagesText(x.MaxSizePages) + " (MAXSIZE = UNLIMITED)",
			Help: unlimitedHelp, Kind: "bool", Value: fmt.Sprint(unlimited)})
		p.Options[OptionGrowthUnlimited] = fmt.Sprint(unlimited)
	}

	fix.Target = fmt.Sprintf("%d MB", mb)
	if unlimited {
		fix.Target += ", no size limit"
	}

	// Warnings: what the growth costs, room on the volume, TempDB's files, when, permission.
	if x.isLog() {
		switch {
		case EngineMajor(version) >= 16 && mb <= 64:
			p.Warnings = append(p.Warnings, "On SQL Server 2022 and later a log growth of 64 MB or less is instant, so this one does not pause the users.")
		case EngineMajor(version) >= 16:
			p.Warnings = append(p.Warnings, fmt.Sprintf("On SQL Server 2022 and later only log growth up to 64 MB is instant; a %d MB growth is written with zeros first, a short pause for every writer.", mb))
		default:
			p.Warnings = append(p.Warnings, fmt.Sprintf("New log space is always written with zeros before use (instant file initialization does not apply to the log): each %d MB growth pauses every writer for a moment, so keep the step moderate.", mb))
		}
	} else {
		switch {
		case st.IFI != nil && *st.IFI:
			p.Warnings = append(p.Warnings, "Instant file initialization is on (INS07): a data file growth does not wait for zeros to be written.")
		case st.IFI != nil:
			p.Warnings = append(p.Warnings, fmt.Sprintf("Instant file initialization is off (INS07): each %d MB growth is written with zeros first, a pause for the users. Ask IT to grant the SQL Server service account \"Perform volume maintenance tasks\".", mb))
		default:
			why := ""
			if st.IFIError != "" {
				why = " (" + strings.TrimSuffix(st.IFIError, ".") + ")"
			}
			p.Warnings = append(p.Warnings, fmt.Sprintf("Could not tell whether instant file initialization is on%s (INS07); without it each %d MB growth is written with zeros first.", why, mb))
		}
	}
	if x.Volume != "" && x.VolumeTotalMB > 0 && x.VolumeFreeMB < int64(mb) {
		p.Warnings = append(p.Warnings, fmt.Sprintf("Volume %s has %d MB free, less than one growth of %d MB: free space there first (STO01).", x.Volume, x.VolumeFreeMB, mb))
	}
	if x.DatabaseID == 2 && x.isData() {
		p.Warnings = append(p.Warnings, "This is a TempDB data file: give every TempDB data file the same growth (TMP02).")
	}
	p.Warnings = append(p.Warnings, growthAtOnce, permission(ScopeDatabase, x.Database))

	if !usable {
		return p, fix, nil
	}
	setting := fmt.Sprintf("FILEGROWTH = %dMB", mb)
	back := growthSetting(x)
	p.Changes = append(p.Changes, Drift{Setting: "Autogrowth of " + x.LogicalName, Current: fix.Current, Target: fmt.Sprintf("%d MB", mb)})
	if unlimited {
		setting += ", MAXSIZE = UNLIMITED"
		back += ", MAXSIZE = " + maxSizeSetting(x.MaxSizePages)
		p.Changes = append(p.Changes, Drift{Setting: "Size limit of " + x.LogicalName, Current: pagesText(x.MaxSizePages), Target: "none"})
	}
	p.Statement = alterFile(x.Database, x.LogicalName, setting)
	p.Rollback = alterFile(x.Database, x.LogicalName, back)
	return p, fix, nil
}

// AutogrowthTarget reads the file an autogrowth preview was built for back from its options: the
// file, its database, the growth in MB and whether the size limit is lifted.
func AutogrowthTarget(options map[string]string) (file, fileDB string, growthMB int, unlimited bool, ok bool) {
	file, fileDB = strings.TrimSpace(options[OptionGrowthFile]), strings.TrimSpace(options[OptionGrowthDatabase])
	growthMB, ok = parseWholeNumber(options[OptionGrowthMB], 1, maxGrowthMB)
	_, offered := options[OptionGrowthUnlimited]
	unlimited = offered && boolOption(options, OptionGrowthUnlimited)
	return file, fileDB, growthMB, unlimited, ok && file != "" && fileDB != ""
}

// stateFile finds a file in what was read back.
func stateFile(st StorageFileState, fileDB, name string) (StorageFile, bool) {
	for _, x := range st.Files {
		if strings.EqualFold(x.Database, fileDB) && strings.EqualFold(x.LogicalName, name) {
			return x, true
		}
	}
	return StorageFile{}, false
}

// AutogrowthReadBack says what the file's growth is after the fix ran, and whether it is the growth
// the preview set (and no size limit when it lifted it).
func AutogrowthReadBack(st StorageFileState, options map[string]string) (string, bool) {
	file, fileDB, mb, unlimited, ok := AutogrowthTarget(options)
	if !ok {
		return "the preview's options are incomplete", false
	}
	x, found := stateFile(st, fileDB, file)
	if !found {
		return "the file " + file + " was not found in " + fileDB, false
	}
	after := "growth " + growthText(x)
	verified := !x.IsPercentGrowth && x.Growth*8 == int64(mb)*1024
	if unlimited {
		// MAXSIZE = UNLIMITED on a log file is stored as 2 TB.
		lifted := x.MaxSizePages == -1 || (x.isLog() && x.MaxSizePages == 268435456)
		if lifted {
			after += ", no size limit"
		} else {
			after += ", limit " + pagesText(x.MaxSizePages)
		}
		verified = verified && lifted
	}
	return after, verified
}

// AutogrowthChangeDescription is the change log line of the autogrowth fix.
func AutogrowthChangeDescription(fix Fix) string {
	file, fileDB := fix.Options[OptionGrowthFile], fix.Options[OptionGrowthDatabase]
	return fmt.Sprintf("Autogrowth of %s in %s: %s -> %s", file, fileDB, fix.Current, fix.Target)
}

// tempdbFolder is the folder of tempdb's first data file, with its trailing backslash; "" when its
// path has none.
func tempdbFolder(x StorageFile) string {
	i := strings.LastIndex(x.PhysicalName, `\`)
	if i < 0 {
		return ""
	}
	return x.PhysicalName[:i+1]
}

// tempdbNewNames are the logical names of the files to add: tempdev{n} from the number after the
// data files, skipping a name (or a file name) that any of tempdb's files already has.
func tempdbNewNames(files []StorageFile, dataFiles int, folder string, count int) []string {
	taken := map[string]bool{}
	for _, x := range files {
		taken[strings.ToLower(x.LogicalName)] = true
		taken[strings.ToLower(x.PhysicalName)] = true
	}
	out := []string{}
	for n := dataFiles + 1; len(out) < count; n++ {
		name := fmt.Sprintf("tempdev%d", n)
		if taken[strings.ToLower(name)] || taken[strings.ToLower(folder+name+".ndf")] {
			continue
		}
		out = append(out, name)
	}
	return out
}

// BuildAddTempdbFilesFix builds the preview of "Add TempDB data files" from the storage facts and the
// count option (default: the files missing to reach min(CPUs, 8)). Each new file goes in the folder
// of tempdb's first data file, with its size and growth. A count out of range, a folder that cannot
// be told or a volume without room for the files gives the warning and no changes.
// ErrFixNotApplicable when TMP01 did not fire.
func BuildAddTempdbFilesFix(f StorageFacts, options map[string]string) (FixPreview, Fix, error) {
	fired := false
	for _, x := range storageFindings(f) {
		fired = fired || x.FixID == FixAddTempdbFiles
	}
	files := f.tempdbData()
	if !fired || len(files) == 0 || f.CPUCount <= 0 {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	sort.Slice(files, func(i, j int) bool { return files[i].FileID < files[j].FileID })
	n, target := len(files), min(f.CPUCount, 8)
	gap := target - n
	d, _ := FindFixDef(FixAddTempdbFiles)
	fix := Fix{ID: d.ID, Title: d.Title, Scope: d.Scope, CheckID: d.CheckID, Setting: d.Setting,
		Current: fmt.Sprintf("%d data files", n)}
	p := FixPreview{EngineVersion: f.EngineVersion, Changes: []Drift{}, Warnings: []string{}, Options: map[string]string{}}

	usable := true
	count := gap
	in := FixInput{Name: OptionTempdbCount, Label: "Files to add", Kind: "text", Value: strconv.Itoa(gap),
		Help: fmt.Sprintf("How many data files to add. %d in all is the recommendation for %d logical CPUs (one per CPU, at most 8).", target, f.CPUCount)}
	if typed, given := options[OptionTempdbCount]; given {
		if v, ok := parseWholeNumber(typed, 1, gap); ok {
			count = v
			in.Value = strconv.Itoa(v)
		} else {
			warn := fmt.Sprintf(tempdbCountWarn, gap)
			in.Value, in.Warning = strings.TrimSpace(typed), warn
			p.Warnings = append(p.Warnings, warn)
			usable = false
		}
	}
	p.Inputs = []FixInput{in}
	p.Options[OptionTempdbCount] = in.Value
	fix.Target = fmt.Sprintf("%d data files", n+count)

	first := files[0]
	folder := tempdbFolder(first)
	if folder == "" {
		p.Warnings = append(p.Warnings, "Could not tell the folder of "+first.LogicalName+" ("+first.PhysicalName+"): add the files by hand.")
		usable = false
	}
	sizeMB := first.SizeMB
	growth := growthText(first)

	// The existing files should be equal, or the new ones are used unevenly.
	maxMB, differ := int64(0), false
	for _, x := range files {
		maxMB = max(maxMB, x.SizeMB)
		differ = differ || x.SizeMB != first.SizeMB || x.Growth != first.Growth || x.IsPercentGrowth != first.IsPercentGrowth
	}
	if differ {
		p.Warnings = append(p.Warnings, fmt.Sprintf("The existing TempDB data files are not the same size and growth: make the existing files %d MB first or the new ones are used unevenly (TMP02).", maxMB),
			tempdbSetupFirst)
	}
	need := int64(count) * sizeMB
	switch {
	case first.Volume == "" || first.VolumeTotalMB <= 0:
		p.Warnings = append(p.Warnings, fmt.Sprintf("The folder %s must have room for the new files: %d x %d MB = %d MB (kakapo could not read the volume).", folder, count, sizeMB, need))
	case first.VolumeFreeMB < need:
		p.Warnings = append(p.Warnings, fmt.Sprintf("The folder %s needs %d MB for the new files (%d x %d MB) and volume %s has %d MB free: free space first or add fewer files.",
			folder, need, count, sizeMB, first.Volume, first.VolumeFreeMB))
		usable = false
	default:
		p.Warnings = append(p.Warnings, fmt.Sprintf("The new files take %d MB (%d x %d MB) on %s, which has %d MB free.", need, count, sizeMB, first.Volume, first.VolumeFreeMB))
	}
	p.Warnings = append(p.Warnings, tempdbAtOnce, tempdbRemoveNote, permission(ScopeDatabase, "tempdb"))
	if !usable {
		return p, fix, nil
	}

	names := tempdbNewNames(f.TempdbFiles(), n, folder, count)
	p.Changes = append(p.Changes, Drift{Setting: "TempDB data files", Current: strconv.Itoa(n), Target: strconv.Itoa(n + count)})
	add, remove := []string{}, []string{}
	for _, name := range names {
		path := folder + name + ".ndf"
		add = append(add, fmt.Sprintf("ALTER DATABASE tempdb ADD FILE (NAME = %s, FILENAME = %s, SIZE = %dMB, %s);",
			nstr(name), nstr(path), sizeMB, growthSetting(first)))
		remove = append([]string{"ALTER DATABASE tempdb REMOVE FILE " + QuoteName(name) + ";"}, remove...)
		p.Changes = append(p.Changes, Drift{Setting: name, Current: "none", Target: fmt.Sprintf("%s, %d MB, growth %s", path, sizeMB, growth)})
	}
	p.Statement = strings.Join(add, "\n")
	p.Rollback = strings.Join(remove, "\n")
	return p, fix, nil
}

// AddedTempdbFiles are the logical names of the files an add-tempdb-files preview adds.
func AddedTempdbFiles(p FixPreview) []string {
	out := []string{}
	for _, c := range p.Changes {
		if c.Current == "none" && c.Setting != "TempDB data files" {
			out = append(out, c.Setting)
		}
	}
	return out
}

// TempdbFilesReadBack says how many TempDB data files there are after the fix ran, and whether
// every file it added is there.
func TempdbFilesReadBack(st StorageFileState, added []string) (string, bool) {
	n := 0
	have := map[string]bool{}
	for _, x := range st.Files {
		if x.DatabaseID == 2 && x.isData() {
			n++
			have[strings.ToLower(x.LogicalName)] = true
		}
	}
	missing := []string{}
	for _, name := range added {
		if !have[strings.ToLower(name)] {
			missing = append(missing, name)
		}
	}
	out := fmt.Sprintf("%d TempDB data files", n)
	if len(missing) > 0 {
		return out + "; not found: " + strings.Join(missing, ", "), false
	}
	return out + " (" + strings.Join(added, ", ") + " added)", true
}

// TempdbChangeDescription is the change log line of the add-tempdb-files fix.
func TempdbChangeDescription(fix Fix, added []string) string {
	return fmt.Sprintf("TempDB data files: %s -> %s (added %s)", fix.Current, fix.Target, strings.Join(added, ", "))
}

// AvailableStorageFixes are the fixes of the Storage card's findings on this read: one autogrowth
// fix per file STO02 or STO03 found, with the file in its options (the card page matches a finding
// to it by the finding's object; database is there for a TempDB file only, so that a database named
// like one of its files cannot match another file's entry), the add-TempDB-files fix when TMP01
// fired, and the TempDB file set fix once whenever the files were read (the card's TempDB row).
func AvailableStorageFixes(f StorageFacts, findings []Finding) []Fix {
	out := []Fix{}
	tempdb := false
	for _, x := range findings {
		switch x.FixID {
		case FixAutogrowth:
			s, ok := fileForFinding(f, x)
			if !ok {
				continue
			}
			d, _ := FindFixDef(FixAutogrowth)
			opts := map[string]string{OptionGrowthFile: s.LogicalName}
			if s.DatabaseID == 2 {
				opts[OptionGrowthDatabase] = "tempdb"
			}
			out = append(out, Fix{ID: d.ID, Title: d.Title, Scope: d.Scope, CheckID: x.CheckID, Setting: d.Setting,
				Current: growthText(s), Target: fmt.Sprintf("%d MB", autogrowthDefaultMB(s)), Options: opts})
		case FixAddTempdbFiles:
			if tempdb {
				continue
			}
			if _, fix, err := BuildAddTempdbFilesFix(f, nil); err == nil {
				tempdb = true
				out = append(out, fix)
			}
		}
	}
	// The TempDB file set, whatever TMP02 says: the Storage card's standing TempDB row shows the
	// files and the week's growths and pre-sizes a healthy set from the same preview.
	if _, fix, err := BuildTempdbFilesFix(f, StorageFileState{}, nil); err == nil {
		out = append(out, fix)
	}
	return out
}

// The TempDB file set fix (docs/phase4e-contract.md, part 1): every TempDB file at a starting size
// the workload has already reached and a fixed growth, in one batch.
const (
	OptionTempdbDataSizeMB   = "dataSizeMB"
	OptionTempdbDataGrowthMB = "dataGrowthMB"
	OptionTempdbLogSizeMB    = "logSizeMB"
	OptionTempdbLogGrowthMB  = "logGrowthMB"
)

// Limits and defaults of the TempDB file set fix, in MB.
const (
	tempdbSizeStepMB  = 64
	tempdbMinSizeMB   = 64
	tempdbMaxSizeMB   = 262144
	tempdbMinGrowthMB = 64
	tempdbMinLogMB    = 256
	tempdbBigFileMB   = 1024
)

// Texts of the TempDB file set fix.
const (
	tempdbDataSizeHelp   = "The size every TempDB data file starts at, now and after every restart, in MB (64 to 262144). The suggestion is the largest data file now, rounded up to 64 MB: the size the workload has already reached."
	tempdbDataGrowthHelp = "How much each data file grows when it is full, in MB (64 to 4096): 512 MB for files of 1 GB or more, else 64 MB. Every data file gets the same growth."
	tempdbLogSizeHelp    = "The size TempDB's log starts at, now and after every restart, in MB (64 to 262144). The suggestion is its size now, rounded up to 64 MB, at least 256 MB."
	tempdbLogGrowthHelp  = "How much the log grows when it is full, in MB (64 to 4096): 256 MB for a log of 1 GB or more, else 64 MB."
	tempdbDataSizeWarn   = "Data file size: type a whole number of MB from 64 to 262144."
	tempdbDataGrowthWarn = "Data file growth: type a whole number of MB from 64 to 4096."
	tempdbLogSizeWarn    = "Log size: type a whole number of MB from 64 to 262144."
	tempdbLogGrowthWarn  = "Log growth: type a whole number of MB from 64 to 4096."
	tempdbSetupAtOnce    = "Takes effect at once and online: the files grow now, nothing is moved and no restart is needed."
	tempdbSetupRestart   = "TempDB is recreated at these sizes on every restart, so from now on it starts at the size it needs instead of growing to it during the day."
	tempdbSetupPerm      = "Needs the ALTER permission on tempdb (in practice, sysadmin)."
	tempdbLogZeroed      = "The %d MB added to the log is written with zeros first (instant file initialization never applies to the log): a short pause for TempDB's users."
)

// roundUpMB rounds a size up to the next 64 MB, at least 64 MB.
func roundUpMB(mb int64) int64 {
	r := (mb + tempdbSizeStepMB - 1) / tempdbSizeStepMB * tempdbSizeStepMB
	return max(r, tempdbMinSizeMB)
}

// tempdbDefaultSize is the starting size the fix suggests for a set of files: the largest rounded up
// to 64 MB, at least floor. When a file is already exactly that size but a restart recreates it
// smaller, the suggestion is 64 MB more: SQL Server accepts only a size larger than the current one,
// and the point is the starting size.
func tempdbDefaultSize(files []StorageFile, floor int64) int64 {
	largest := int64(0)
	for _, x := range files {
		largest = max(largest, x.SizeMB)
	}
	target := max(roundUpMB(largest), floor)
	for _, x := range files {
		if x.SizeMB == target && x.StartupSizeMB > 0 && x.StartupSizeMB < target {
			target += tempdbSizeStepMB
			break
		}
	}
	return min(target, tempdbMaxSizeMB)
}

// tempdbBigGrowth is the growth the fix suggests: big when the size is 1 GB or more.
func tempdbBigGrowth(sizeMB, big int64) int64 {
	if sizeMB >= tempdbBigFileMB {
		return big
	}
	return tempdbMinGrowthMB
}

// mbInput reads one whole-MB input of the fix from the options: the default when it was not given,
// the typed value when it is in range, else the warning (ok false).
func mbInput(options map[string]string, name, label, help, warn string, def int64, lo, hi int) (int64, FixInput, bool) {
	in := FixInput{Name: name, Label: label, Help: help, Kind: "text", Value: strconv.FormatInt(def, 10)}
	typed, given := options[name]
	if !given {
		return def, in, true
	}
	if n, ok := parseWholeNumber(typed, lo, hi); ok {
		in.Value = strconv.Itoa(n)
		return int64(n), in, true
	}
	in.Value, in.Warning = strings.TrimSpace(typed), warn
	return def, in, false
}

// tempdbFileText is a TempDB file as the fix shows it: "128 MB, growth 64 MB", with its size limit.
func tempdbFileText(x StorageFile) string {
	out := fmt.Sprintf("%d MB, growth %s", x.SizeMB, growthText(x))
	if maxSizeSet(x.MaxSizePages) {
		out += ", limit " + pagesText(x.MaxSizePages)
	}
	return out
}

// sizesText is the sizes of a set of files: "128 MB", or "8 to 128 MB" when they differ.
// OptionTempdbSummary carries the TempDB row's text in the fix's options (never read by the preview).
const OptionTempdbSummary = "summary"

// growthsText is the files' growth when they share one, else "mixed".
func growthsText(files []StorageFile) string {
	if len(files) == 0 {
		return "?"
	}
	first := growthText(files[0])
	for _, x := range files[1:] {
		if growthText(x) != first {
			return "mixed"
		}
	}
	return first
}

func sizesText(files []StorageFile) string {
	if len(files) == 0 {
		return "?"
	}
	lo, hi := files[0].SizeMB, files[0].SizeMB
	for _, x := range files {
		lo, hi = min(lo, x.SizeMB), max(hi, x.SizeMB)
	}
	if lo == hi {
		return fmt.Sprintf("%d MB", lo)
	}
	return fmt.Sprintf("%d to %d MB", lo, hi)
}

// tempdbModify is one MODIFY FILE of tempdb.
func tempdbModify(name string, settings []string) string {
	return "ALTER DATABASE tempdb MODIFY FILE (NAME = " + nstr(name) + ", " + strings.Join(settings, ", ") + ");"
}

// namesAnd joins names as "a", "a and b", "a, b and c".
func namesAnd(names []string) string {
	if len(names) <= 1 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// tempdbRoom is the room a volume needs for the files that grow on it.
type tempdbRoom struct {
	volume         string
	needMB, freeMB int64
	known          bool
}

// tempdbRestart is the files that get no SIZE (they are already the size set, or larger) but that a
// restart recreates smaller: "temp1 (1024 MB)", the size set and the largest current size.
type tempdbRestart struct {
	names        []string
	size, maxNow int64
}

// note is the warning for those files; "" when there are none.
func (r tempdbRestart) note() string {
	switch len(r.names) {
	case 0:
		return ""
	case 1:
		return fmt.Sprintf("After a restart %s starts smaller than the %d MB set here: SQL Server accepts only a size larger than the file is now (%d MB), so type %d MB or more to make that its starting size.",
			r.names[0], r.size, r.maxNow, roundUpMB(r.maxNow+1))
	}
	return fmt.Sprintf("After a restart these files start smaller than the %d MB set here: %s. SQL Server accepts only a size larger than a file is now (up to %d MB), so type %d MB or more to make that their starting size.",
		r.size, namesAnd(r.names), r.maxNow, roundUpMB(r.maxNow+1))
}

// BuildTempdbFilesFix builds the preview of "Set up the TempDB files" from the storage facts (tempdb's
// files as they are now, their volumes and startup sizes), the file state (instant file
// initialization) and the options: the data file size and growth, the log size and growth. Every file
// gets its growth, a SIZE when it is smaller than its target (SQL Server refuses a smaller one; a
// larger file keeps its size and the preview says so) and MAXSIZE = UNLIMITED when it is capped. A bad
// value or a volume without room for the growth gives the warning and no changes.
// ErrFixNotApplicable when TMP02 did not fire.
func BuildTempdbFilesFix(f StorageFacts, st StorageFileState, options map[string]string) (FixPreview, Fix, error) {
	// Offered whenever the files were read, TMP02 fired or not: a TempDB that is equal but starts
	// small is pre-sized from the same preview (the Storage card's standing TempDB row).
	data, logs := []StorageFile{}, []StorageFile{}
	for _, x := range f.TempdbFiles() {
		if x.isData() {
			data = append(data, x)
		} else if x.isLog() {
			logs = append(logs, x)
		}
	}
	if len(data) == 0 {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	sort.Slice(data, func(i, j int) bool { return data[i].FileID < data[j].FileID })
	sort.Slice(logs, func(i, j int) bool { return logs[i].FileID < logs[j].FileID })
	version := f.EngineVersion
	if version == "" {
		version = st.EngineVersion
	}
	d, _ := FindFixDef(FixTempdbFiles)
	fix := Fix{ID: d.ID, Title: d.Title, Scope: d.Scope, CheckID: d.CheckID, Setting: d.Setting,
		Current: fmt.Sprintf("%d data files of %s, log %s", len(data), sizesText(data), sizesText(logs)),
		// The Storage card's standing TempDB row shows this: the files as they are, then the week's
		// growth history (docs/phase4e-contract.md). Not an input; the preview ignores it.
		Options: map[string]string{OptionTempdbSummary: fmt.Sprintf("%d data files of %s, growth %s; log %s, growth %s.%s",
			len(data), sizesText(data), growthsText(data), sizesText(logs), growthsText(logs), f.tempdbGrowthEvidence())}}
	p := FixPreview{EngineVersion: version, Changes: []Drift{}, Warnings: []string{}, Options: map[string]string{}}

	// The inputs; the growth suggestions follow the size in use.
	usable := true
	input := func(name, label, help, warn string, def int64, lo, hi int) int64 {
		v, in, ok := mbInput(options, name, label, help, warn, def, lo, hi)
		if !ok {
			p.Warnings = append(p.Warnings, warn)
			usable = false
		}
		p.Inputs = append(p.Inputs, in)
		p.Options[name] = in.Value
		return v
	}
	dataSize := input(OptionTempdbDataSizeMB, "Data file size (MB)", tempdbDataSizeHelp, tempdbDataSizeWarn,
		tempdbDefaultSize(data, tempdbMinSizeMB), tempdbMinSizeMB, tempdbMaxSizeMB)
	dataGrowth := input(OptionTempdbDataGrowthMB, "Data file growth (MB)", tempdbDataGrowthHelp, tempdbDataGrowthWarn,
		tempdbBigGrowth(dataSize, 512), tempdbMinGrowthMB, maxGrowthMB)
	logSize := input(OptionTempdbLogSizeMB, "Log size (MB)", tempdbLogSizeHelp, tempdbLogSizeWarn,
		tempdbDefaultSize(logs, tempdbMinLogMB), tempdbMinSizeMB, tempdbMaxSizeMB)
	logGrowth := input(OptionTempdbLogGrowthMB, "Log growth (MB)", tempdbLogGrowthHelp, tempdbLogGrowthWarn,
		tempdbBigGrowth(logSize, 256), tempdbMinGrowthMB, maxGrowthMB)
	fix.Target = fmt.Sprintf("%d data files of %d MB, growth %d MB; log %d MB, growth %d MB", len(data), dataSize, dataGrowth, logSize, logGrowth)

	// Each file: its statement, its rollback, its row, the room it needs.
	stmts, backs, rows := []string{}, []string{}, []Drift{}
	rooms := map[string]*tempdbRoom{}
	keptData, keptLog := []string{}, []string{}
	var restartData, restartLog tempdbRestart
	largestKept, dataInc, logInc, capped := int64(0), int64(0), int64(0), false
	for _, x := range append(append([]StorageFile{}, data...), logs...) {
		size, growth := dataSize, dataGrowth
		if x.isLog() {
			size, growth = logSize, logGrowth
		}
		settings, back := []string{}, []string{growthSetting(x)}
		target := fmt.Sprintf("%d MB, growth %d MB", size, growth)
		switch {
		case x.SizeMB < size:
			settings = append(settings, fmt.Sprintf("SIZE = %dMB", size))
			inc := size - x.SizeMB
			if x.isLog() {
				logInc += inc
			} else {
				dataInc += inc
			}
			key := x.Volume
			if x.VolumeTotalMB <= 0 {
				key = ""
			}
			r := rooms[key]
			if r == nil {
				r = &tempdbRoom{volume: key, freeMB: x.VolumeFreeMB, known: key != ""}
				rooms[key] = r
			}
			r.needMB += inc
		case x.SizeMB > size:
			target = fmt.Sprintf("kept at %d MB, growth %d MB", x.SizeMB, growth)
			if x.isLog() {
				keptLog = append(keptLog, x.LogicalName)
			} else {
				keptData = append(keptData, fmt.Sprintf("%s (%d MB)", x.LogicalName, x.SizeMB))
				largestKept = max(largestKept, x.SizeMB)
			}
		}
		// Without a SIZE, the starting size stays what it is.
		if x.SizeMB >= size && x.StartupSizeMB > 0 && x.StartupSizeMB < size {
			r := &restartData
			if x.isLog() {
				r = &restartLog
			}
			r.names = append(r.names, fmt.Sprintf("%s (at %d MB)", x.LogicalName, x.StartupSizeMB))
			r.size, r.maxNow = size, max(r.maxNow, x.SizeMB)
		}
		settings = append(settings, fmt.Sprintf("FILEGROWTH = %dMB", growth))
		if maxSizeSet(x.MaxSizePages) {
			capped = true
			settings = append(settings, "MAXSIZE = UNLIMITED")
			back = append(back, "MAXSIZE = "+maxSizeSetting(x.MaxSizePages))
			target += ", no size limit"
		}
		stmts = append(stmts, tempdbModify(x.LogicalName, settings))
		backs = append(backs, tempdbModify(x.LogicalName, back))
		rows = append(rows, Drift{Setting: x.LogicalName, Current: tempdbFileText(x), Target: target})
	}

	// Room on each volume for the increase: not enough blocks.
	keys := make([]string, 0, len(rooms))
	for k := range rooms {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		r := rooms[k]
		switch {
		case !r.known:
			rows = append(rows, Drift{Setting: "Disk room needed", Current: "", Target: fmt.Sprintf("%d MB (kakapo could not read the volume)", r.needMB)})
			p.Warnings = append(p.Warnings, fmt.Sprintf("The TempDB files need %d MB more room and kakapo could not read the volume: check the free space before applying.", r.needMB))
		case r.freeMB < r.needMB:
			p.Warnings = append(p.Warnings, fmt.Sprintf("The TempDB files need %d MB more on %s and it has %d MB free: free space there first (STO01) or type smaller sizes.", r.needMB, r.volume, r.freeMB))
			usable = false
		default:
			rows = append(rows, Drift{Setting: "Disk room needed", Current: "", Target: fmt.Sprintf("%d MB on %s (%d MB free)", r.needMB, r.volume, r.freeMB)})
		}
	}

	// A larger file is left alone.
	switch len(keptData) {
	case 0:
	case 1:
		p.Warnings = append(p.Warnings, fmt.Sprintf("Data file %s is larger than the %d MB set here and is left as it is: to make the data files equal, raise the size to %d MB, or shrink that file by hand in a restart window.",
			keptData[0], dataSize, largestKept))
	default:
		p.Warnings = append(p.Warnings, fmt.Sprintf("Data files %s are larger than the %d MB set here and are left as they are: to make the data files equal, raise the size to %d MB, or shrink them by hand in a restart window.",
			namesAnd(keptData), dataSize, largestKept))
	}
	if len(keptLog) > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("The log %s is larger than the %d MB set here and keeps its size.", namesAnd(keptLog), logSize))
	}
	for _, r := range []tempdbRestart{restartData, restartLog} {
		if w := r.note(); w != "" {
			p.Warnings = append(p.Warnings, w)
		}
	}

	// What growing costs: zeros for the data files without instant file initialization, always for the log.
	if dataInc > 0 {
		switch {
		case st.IFI != nil && *st.IFI:
			p.Warnings = append(p.Warnings, "Instant file initialization is on (INS07): the data files grow without writing zeros first.")
		case st.IFI != nil:
			p.Warnings = append(p.Warnings, fmt.Sprintf("Instant file initialization is off (INS07): the %d MB added to the data files is written with zeros first, which takes a while and loads the disk (and so does every later growth). Ask IT to grant the SQL Server service account \"Perform volume maintenance tasks\".", dataInc))
		default:
			why := ""
			if st.IFIError != "" {
				why = " (" + strings.TrimSuffix(st.IFIError, ".") + ")"
			}
			p.Warnings = append(p.Warnings, fmt.Sprintf("Could not tell whether instant file initialization is on%s (INS07); without it the %d MB added to the data files is written with zeros first, which takes a while.", why, dataInc))
		}
	}
	if logInc > 0 {
		p.Warnings = append(p.Warnings, fmt.Sprintf(tempdbLogZeroed, logInc))
	}
	limits := ""
	if capped {
		limits = " and size limits"
	}
	p.Warnings = append(p.Warnings, tempdbSetupAtOnce, tempdbSetupRestart,
		"The rollback puts the previous growths"+limits+" back. The sizes cannot be rolled back, because SQL Server accepts only a larger size: to make a file smaller, shrink it by hand (DBCC SHRINKFILE) in a quiet hour or a restart window.",
		tempdbSetupPerm)
	if !usable {
		return p, fix, nil
	}
	p.Changes = rows
	p.Statement = strings.Join(stmts, "\n")
	p.Rollback = strings.Join(backs, "\n")
	return p, fix, nil
}

// tempdbTargets reads the four values a TempDB file set preview was built with from its options.
func tempdbTargets(options map[string]string) (dataSize, dataGrowth, logSize, logGrowth int64, ok bool) {
	vals := [4]int64{}
	for i, c := range []struct {
		name   string
		lo, hi int
	}{
		{OptionTempdbDataSizeMB, tempdbMinSizeMB, tempdbMaxSizeMB}, {OptionTempdbDataGrowthMB, tempdbMinGrowthMB, maxGrowthMB},
		{OptionTempdbLogSizeMB, tempdbMinSizeMB, tempdbMaxSizeMB}, {OptionTempdbLogGrowthMB, tempdbMinGrowthMB, maxGrowthMB},
	} {
		n, good := parseWholeNumber(options[c.name], c.lo, c.hi)
		if !good {
			return 0, 0, 0, 0, false
		}
		vals[i] = int64(n)
	}
	return vals[0], vals[1], vals[2], vals[3], true
}

// TempdbSetupReadBack says what TempDB's files are after the fix ran (read back from
// tempdb.sys.database_files), and whether every file is at least its target size with the fixed
// growth the preview set and no size limit.
func TempdbSetupReadBack(st StorageFileState, options map[string]string) (string, bool) {
	dataSize, dataGrowth, logSize, logGrowth, ok := tempdbTargets(options)
	if !ok {
		return "the preview's options are incomplete", false
	}
	data, logs, differ := []StorageFile{}, []StorageFile{}, []string{}
	for _, x := range st.Files {
		if x.DatabaseID != 2 {
			continue
		}
		size, growth := dataSize, dataGrowth
		if x.isLog() {
			size, growth = logSize, logGrowth
			logs = append(logs, x)
		} else {
			data = append(data, x)
		}
		lifted := !maxSizeSet(x.MaxSizePages)
		if x.SizeMB < size || x.IsPercentGrowth || x.Growth*8 != growth*1024 || !lifted {
			differ = append(differ, x.LogicalName+" "+tempdbFileText(x))
		}
	}
	if len(data) == 0 {
		return "TempDB's files were not found", false
	}
	if len(differ) > 0 {
		return "not as set: " + strings.Join(differ, "; "), false
	}
	return fmt.Sprintf("%d data files of %s, growth %d MB; log %s, growth %d MB", len(data), sizesText(data), dataGrowth, sizesText(logs), logGrowth), true
}

// TempdbSetupChanged says whether any TempDB file read back differs from what the preview showed as
// its current value: after a failed batch, whether the statements before the failing one ran.
func TempdbSetupChanged(p FixPreview, st StorageFileState) bool {
	before := map[string]string{}
	for _, c := range p.Changes {
		before[strings.ToLower(c.Setting)] = c.Current
	}
	for _, x := range st.Files {
		if was, ok := before[strings.ToLower(x.LogicalName)]; ok && x.DatabaseID == 2 && was != tempdbFileText(x) {
			return true
		}
	}
	return false
}

// TempdbSetupChangeDescription is the change log line of the TempDB file set fix.
func TempdbSetupChangeDescription(fix Fix) string {
	return fmt.Sprintf("TempDB files: %s -> %s", fix.Current, fix.Target)
}
