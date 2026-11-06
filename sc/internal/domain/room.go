package domain

import (
	"fmt"
	"math"
	"strings"
)

// The room line (docs/phase5l-contract.md): every fix that uses disk space shows, before anything
// runs, the drive it lands on, its free space now, what the change will use and what is left after.
// A builder states what its change needs (FixPreview.Needs: a path or a volume, and a part); the
// service reads the server's volumes and the stored trend and BuildRoom judges, one line per drive.

// Room states.
const (
	RoomFits    = "fits"
	RoomTight   = "tight"
	RoomNoFit   = "nofit"
	RoomUnknown = "unknown"
)

// The two thresholds agreed in the review of 6 Nov 2026: less than 10 % of the drive free after the
// change, or the drive's trend filling it within 30 days of the change, is tight.
const (
	roomTightPct  = 10
	roomTightDays = 30
)

// RoomPart is one thing a change puts on a drive: "8 full backups", 6,344 MB, and how that was
// counted. Weak says the number is a rough estimate; Unbounded that the change has no fixed size
// (the log after a switch to FULL recovery): MB is then 0 and the line is at least tight.
type RoomPart struct {
	Label     string `json:"label"`
	MB        int64  `json:"mb"`
	Basis     string `json:"basis"`
	Weak      bool   `json:"weak,omitempty"`
	Unbounded bool   `json:"unbounded,omitempty"`
}

// RoomNeed is what a builder states: the part and where it lands, a folder or file path as the
// SQL Server service sees it (Path), or a volume already known (Volume, as sys.dm_os_volume_stats
// names it: "C:\").
type RoomNeed struct {
	Path   string
	Volume string
	Part   RoomPart
}

// Volume is one drive of the server: its mount point ("C:\"), total and free MB. TotalMB 0 means
// only the free space is known (xp_fixeddrives).
type Volume struct {
	Name    string
	TotalMB int64
	FreeMB  int64
}

// RoomFacts are what BuildRoom judges against: the server's volumes and, per volume name, the
// stored trend STO12 forecasts from (none when there are not two samples a day apart).
type RoomFacts struct {
	Volumes []Volume
	Trends  map[string]DiskTrend
}

// RoomLine is one drive's room for a change.
type RoomLine struct {
	Drive    string     `json:"drive"`   // "C:", or the path when the drive is not readable
	TotalMB  int64      `json:"totalMB"` // 0 when not known
	FreeMB   int64      `json:"freeMB"`  // 0 when not readable
	NeedMB   int64      `json:"needMB"`
	LeftMB   int64      `json:"leftMB"`
	DaysLeft *int       `json:"daysLeft,omitempty"` // after the change, at the drive's trend
	Parts    []RoomPart `json:"parts"`
	State    string     `json:"state"`
	Sentence string     `json:"sentence"`
}

// RoomHeld says the change does not fit on one of its drives: Apply is held back.
func RoomHeld(lines []RoomLine) bool {
	for _, l := range lines {
		if l.State == RoomNoFit {
			return true
		}
	}
	return false
}

// RoomHeldReason is what Apply answers when the room holds it back.
func RoomHeldReason(lines []RoomLine) string {
	for _, l := range lines {
		if l.State == RoomNoFit {
			return l.Sentence
		}
	}
	return ""
}

// VolumeOfPath is the volume a path is on: the longest mount point it starts with, case ignored.
// A share (\\server\share) or a path on no known volume is not found.
func VolumeOfPath(path string, vols []Volume) (Volume, bool) {
	p := strings.ToUpper(strings.TrimSpace(path))
	if p == "" || strings.HasPrefix(p, `\\`) {
		return Volume{}, false
	}
	if !strings.HasSuffix(p, `\`) {
		p += `\`
	}
	best, found := Volume{}, false
	for _, v := range vols {
		m := strings.ToUpper(v.Name)
		if !strings.HasSuffix(m, `\`) {
			m += `\`
		}
		if strings.HasPrefix(p, m) && (!found || len(m) > len(best.Name)) {
			best, found = v, true
		}
	}
	return best, found
}

// driveName is how a line names a volume: "C:" for "C:\", the mount point otherwise.
func driveName(mount string) string {
	if len(mount) == 3 && mount[1] == ':' && mount[2] == '\\' {
		return mount[:2]
	}
	return strings.TrimSuffix(mount, `\`)
}

// BuildRoom groups the needs by drive and judges each drive: one line per drive, in the order the
// needs first name them. A path on no readable volume is its own line, Not readable.
func BuildRoom(needs []RoomNeed, f RoomFacts) []RoomLine {
	type group struct {
		key      string
		vol      Volume
		readable bool
		parts    []RoomPart
	}
	var order []*group
	byKey := map[string]*group{}
	for _, n := range needs {
		var v Volume
		ok := false
		if n.Volume != "" {
			for _, x := range f.Volumes {
				if strings.EqualFold(x.Name, n.Volume) {
					v, ok = x, true
				}
			}
			if !ok {
				v, ok = VolumeOfPath(n.Volume, f.Volumes)
			}
		} else {
			v, ok = VolumeOfPath(n.Path, f.Volumes)
		}
		key := strings.ToUpper(v.Name)
		if !ok {
			key = "?" + strings.ToUpper(firstNonEmpty(n.Path, n.Volume))
		}
		g := byKey[key]
		if g == nil {
			g = &group{key: key, vol: v, readable: ok}
			if !ok {
				g.vol = Volume{Name: firstNonEmpty(n.Path, n.Volume)}
			}
			byKey[key] = g
			order = append(order, g)
		}
		g.parts = append(g.parts, n.Part)
	}
	out := make([]RoomLine, 0, len(order))
	for _, g := range order {
		var trend *DiskTrend
		if t, ok := f.Trends[g.vol.Name]; ok && g.readable {
			trend = &t
		}
		out = append(out, judgeRoom(g.vol, g.readable, g.parts, trend))
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// judgeRoom is one drive's line.
func judgeRoom(v Volume, readable bool, parts []RoomPart, trend *DiskTrend) RoomLine {
	l := RoomLine{Parts: parts, Drive: v.Name}
	unbounded := false
	for _, p := range parts {
		l.NeedMB += p.MB
		unbounded = unbounded || p.Unbounded
	}
	uses := "this change uses about " + roomSize(l.NeedMB)
	if unbounded && l.NeedMB == 0 {
		uses = "this change has no fixed size"
	} else if l.NeedMB == 0 {
		uses = "how much this change uses is not known yet"
	}
	if !readable {
		l.State = RoomUnknown
		where := "the free space of " + v.Name
		if strings.HasPrefix(v.Name, `\\`) {
			l.Sentence = "SQL Server cannot report the free space of a share. "
		} else {
			l.Sentence = "kakapo could not read " + where + ". "
		}
		if l.NeedMB > 0 {
			l.Sentence += "This change needs about " + roomSize(l.NeedMB) + " there: check it before you apply."
		} else {
			l.Sentence += "Check there is room before you apply."
		}
		return l
	}
	l.Drive = driveName(v.Name)
	l.TotalMB, l.FreeMB = v.TotalMB, v.FreeMB
	l.LeftMB = v.FreeMB - l.NeedMB
	if l.LeftMB < 0 {
		l.State = RoomNoFit
		l.Sentence = fmt.Sprintf("%s has %s free and this change needs about %s: %s short. Free space first, or change the options.",
			l.Drive, roomSize(v.FreeMB), roomSize(l.NeedMB), roomSize(-l.LeftMB))
		return l
	}
	if trend != nil {
		if rate := trendMBPerDay(*trend); rate > 0 {
			d := int(math.Floor(float64(l.LeftMB) / rate))
			l.DaysLeft = &d
		}
	}
	l.State = RoomFits
	if unbounded || (l.TotalMB > 0 && l.LeftMB*100 < l.TotalMB*roomTightPct) || (l.DaysLeft != nil && *l.DaysLeft < roomTightDays) {
		l.State = RoomTight
	}
	left := roomSize(l.LeftMB) + " left"
	if l.TotalMB > 0 {
		left += fmt.Sprintf(" (%d %% of %s)", int(math.Round(float64(l.LeftMB)*100/float64(l.TotalMB))), roomSize(l.TotalMB))
	}
	l.Sentence = fmt.Sprintf("%s free now, %s, %s.", roomSize(v.FreeMB), uses, left)
	if unbounded && l.NeedMB == 0 {
		l.Sentence = fmt.Sprintf("%s has %s free; %s.", l.Drive, roomSize(v.FreeMB), uses)
		if l.TotalMB > 0 {
			l.Sentence = fmt.Sprintf("%s has %s free of %s; %s.", l.Drive, roomSize(v.FreeMB), roomSize(l.TotalMB), uses)
		}
	}
	if l.DaysLeft != nil && *l.DaysLeft < roomTightDays {
		l.Sentence += fmt.Sprintf(" At the drive's current rate that is about %d days (STO12).", *l.DaysLeft)
	}
	return l
}

// trendMBPerDay is how fast the volume lost free space between the two samples; 0 when it did not.
func trendMBPerDay(t DiskTrend) float64 {
	days := t.NewestAt.Sub(t.OldestAt).Hours() / 24
	if days < 1 {
		return 0 // STO12 needs samples a day or more apart
	}
	return float64(t.OldestFreeMB-t.NewestFreeMB) / days
}

// roomSize writes a size for a room sentence: "640 MB", "6.2 GB", "213 GB" from 100 GB.
func roomSize(mb int64) string {
	switch {
	case mb < 1024:
		return n0int(mb) + " MB"
	case mb < 100*1024:
		return n1(float64(mb)/1024) + " GB"
	default:
		return n0int(int64(math.Round(float64(mb)/1024))) + " GB"
	}
}

// BackupSizes are what msdb says about backup sizes (docs/phase5l-contract.md part 3), and the
// database's used data, to estimate one full backup.
type BackupSizes struct {
	LastFullMB           int64   // this database's last full backup, uncompressed; 0 when none
	LastFullCompressedMB int64   // its compressed size (equal when not compressed)
	ServerRatio          float64 // compressed / size over the server's compressed fulls of 30 days; 0 when none
	LogMBPerDay          float64 // this database's log backups a day over 7 days; 0 when none
	LogDays              int     // the days of those 7 that had a log backup
	UsedDataMB           int64   // used pages of the data files; 0 when not read
	Error                string
}

// The compression assumed when the server has no compressed backup to learn from.
const defaultCompressRatio = 0.25

// FullBackupMB is one full backup of this database, in order: its last full in msdb, used data x
// the server's compression ratio, used data x 25 %. basis says which; weak for the last one.
func (b BackupSizes) FullBackupMB(compress bool) (mb int64, basis string, weak bool) {
	switch {
	case b.LastFullMB > 0 && compress && b.LastFullCompressedMB < b.LastFullMB:
		return b.LastFullCompressedMB, "the last full backup in msdb, compressed", false
	case b.LastFullMB > 0 && !compress && b.LastFullCompressedMB < b.LastFullMB:
		return b.LastFullMB, "the last full backup in msdb, uncompressed", false
	case b.LastFullMB > 0 && !compress:
		return b.LastFullMB, "the last full backup in msdb", false
	case b.UsedDataMB > 0 && !compress:
		return b.UsedDataMB, "used data " + roomSize(b.UsedDataMB) + ", no backup in msdb yet", false
	case b.UsedDataMB > 0 && b.ServerRatio > 0:
		return int64(math.Ceil(float64(b.UsedDataMB) * b.ServerRatio)),
			fmt.Sprintf("used data %s x %s, this server's compression in msdb; an estimate", roomSize(b.UsedDataMB), ratioPct(b.ServerRatio)), false
	case b.UsedDataMB > 0:
		return int64(math.Ceil(float64(b.UsedDataMB) * defaultCompressRatio)),
			"used data " + roomSize(b.UsedDataMB) + " x 25 %, no compressed backup on this server to learn from", true
	case b.LastFullMB > 0:
		// Compression asked, the last full was not compressed.
		return int64(math.Ceil(float64(b.LastFullMB) * firstPositive(b.ServerRatio, defaultCompressRatio))),
			"the last full backup in msdb, as it would compress", b.ServerRatio == 0
	}
	return 0, "", true
}

func firstPositive(a, b float64) float64 {
	if a > 0 {
		return a
	}
	return b
}

func ratioPct(r float64) string {
	return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.1f", r*100), "0"), ".") + " %"
}

// BackupScheduleNeeds is what the backup schedule puts in its folder: days kept + 1 full backups
// (the job backs up first and deletes after) and, in FULL recovery, days kept of log backups.
func BackupScheduleNeeds(s BackupSchedule, b BackupSizes) []RoomNeed {
	if s.Folder == "" || !ValidBackupFolder(s.Folder) {
		return nil
	}
	var needs []RoomNeed
	if s.Full {
		one, basis, weak := b.FullBackupMB(s.Compress)
		if one > 0 {
			n := s.KeepDays + 1
			needs = append(needs, RoomNeed{Path: s.Folder, Part: RoomPart{
				Label: fmt.Sprintf("%d full backups", n), MB: int64(n) * one, Weak: weak,
				Basis: fmt.Sprintf("%d days kept plus tonight's, before the oldest is deleted, x %s (%s)", s.KeepDays, roomSize(one), basis),
			}})
		} else {
			needs = append(needs, RoomNeed{Path: s.Folder, Part: RoomPart{Label: "full backups", Weak: true,
				Basis: "the size of a backup could not be read"}})
		}
	}
	if s.Log {
		if b.LogDays >= 1 && b.LogMBPerDay > 0 {
			mb := int64(math.Ceil(b.LogMBPerDay * float64(s.KeepDays)))
			needs = append(needs, RoomNeed{Path: s.Folder, Part: RoomPart{
				Label: fmt.Sprintf("%d days of log backups", s.KeepDays), MB: mb, Weak: b.LogDays < 7,
				Basis: fmt.Sprintf("%s a day, the log backups of the last %d days in msdb", roomSize(int64(math.Ceil(b.LogMBPerDay))), b.LogDays),
			}})
		} else {
			needs = append(needs, RoomNeed{Path: s.Folder, Part: RoomPart{Label: "log backups", Weak: true,
				Basis: "not known until a week of log backups has run"}})
		}
	}
	return needs
}

// Roles a builder may name instead of a path; the service reads where they are
// (collect.RolePaths). "@data" and "@log" are the database's own first data and log file,
// "@data:Other" another database's, "@tempdb" tempdb's first data file, "@errorlog" the error log.
const (
	RoleData     = "@data"
	RoleLog      = "@log"
	RoleTempdb   = "@tempdb"
	RoleErrorLog = "@errorlog"
)

// RoleOf is a role for another database: RoleOf(RoleData, "Sales_Archive") is "@data:Sales_Archive".
func RoleOf(role, database string) string { return role + ":" + database }

// ResolveRoles replaces each role in the needs by its path, a role for this database by the
// database's own; a role with no path stays as it is (its line is Not readable).
func ResolveRoles(needs []RoomNeed, database string, paths map[string]string) []RoomNeed {
	out := make([]RoomNeed, len(needs))
	for i, n := range needs {
		key := n.Path
		if (key == RoleData || key == RoleLog) && database != "" {
			key = RoleOf(key, database)
		}
		if strings.HasPrefix(key, "@") {
			if p, ok := paths[strings.ToLower(key)]; ok && p != "" {
				n.Path = p
			}
		}
		out[i] = n
	}
	return out
}

// NeedsRoles says whether any need names a role.
func NeedsRoles(needs []RoomNeed) bool {
	for _, n := range needs {
		if strings.HasPrefix(n.Path, "@") {
			return true
		}
	}
	return false
}
