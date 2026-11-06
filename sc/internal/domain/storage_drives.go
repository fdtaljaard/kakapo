package domain

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// The Storage card answers its own question first (docs/phase5l-contract.md part 5): "Will it run
// out of room?", with one bar per drive this database and tempdb use, the same bar as a fix's
// room line.

// DriveUse is one drive on the Storage card: what is on it, its free and total MB, how much of it
// this database's files take, and STO12's days left when this is the drive it forecasts.
type DriveUse struct {
	Drive      string   `json:"drive"` // "C:"
	TotalMB    int64    `json:"totalMB"`
	FreeMB     int64    `json:"freeMB"`
	DatabaseMB int64    `json:"databaseMB"` // this database's files on the drive, allocated
	Holds      []string `json:"holds"`      // "data", "log", "tempdb"
	DaysLeft   *int     `json:"daysLeft,omitempty"`
	State      string   `json:"state"` // fits (fine), tight
}

// StorageDrives is the card's answer: a sentence, then the drives, tightest first.
type StorageDrives struct {
	Sentence string     `json:"sentence"`
	Drives   []DriveUse `json:"drives"`
}

// BuildStorageDrives is part 5's answer from the storage facts and STO12's trend (nil when there
// are not two samples a day apart). nil when the volumes could not be read.
func BuildStorageDrives(f StorageFacts, trend *DiskTrend) *StorageDrives {
	if f.Error != "" || f.FilesError != "" || f.VolumeError != "" {
		return nil
	}
	byName := map[string]*DriveUse{}
	var order []string
	add := func(x StorageFile, what string, mine bool) {
		if x.Volume == "" || x.VolumeTotalMB <= 0 {
			return
		}
		d := byName[x.Volume]
		if d == nil {
			d = &DriveUse{Drive: driveName(x.Volume), TotalMB: x.VolumeTotalMB, FreeMB: x.VolumeFreeMB, State: RoomFits}
			byName[x.Volume] = d
			order = append(order, x.Volume)
		}
		if mine {
			d.DatabaseMB += x.SizeMB
		}
		for _, h := range d.Holds {
			if h == what {
				return
			}
		}
		d.Holds = append(d.Holds, what)
	}
	for _, x := range f.DatabaseFiles() {
		what := "data"
		if x.isLog() {
			what = "log"
		}
		add(x, what, true)
	}
	for _, x := range f.TempdbFiles() {
		add(x, "tempdb", false)
	}
	if len(order) == 0 {
		return nil
	}
	out := &StorageDrives{}
	for _, name := range order {
		d := byName[name]
		if trend != nil && strings.EqualFold(trend.Volume, name) {
			if rate := trendMBPerDay(*trend); rate > 0 {
				days := int(math.Floor(float64(d.FreeMB) / rate))
				d.DaysLeft = &days
			}
		}
		if d.FreeMB*100 < d.TotalMB*roomTightPct || (d.DaysLeft != nil && *d.DaysLeft < roomTightDays) {
			d.State = RoomTight
		}
		out.Drives = append(out.Drives, *d)
	}
	sort.SliceStable(out.Drives, func(i, j int) bool {
		return out.Drives[i].FreeMB*out.Drives[j].TotalMB < out.Drives[j].FreeMB*out.Drives[i].TotalMB
	})
	t := out.Drives[0]
	pct := int(math.Round(float64(t.FreeMB) * 100 / float64(t.TotalMB)))
	var mine int64
	for _, d := range out.Drives {
		mine += d.DatabaseMB
	}
	switch {
	case t.State == RoomTight && t.DaysLeft != nil && *t.DaysLeft < roomTightDays:
		out.Sentence = fmt.Sprintf("Soon. %s has %s free (%d %%) and at its current rate fills in about %d days.", t.Drive, roomSize(t.FreeMB), pct, *t.DaysLeft)
	case t.State == RoomTight:
		out.Sentence = fmt.Sprintf("It is getting tight. %s has %s free, %d %% of %s.", t.Drive, roomSize(t.FreeMB), pct, roomSize(t.TotalMB))
	case len(out.Drives) == 1:
		out.Sentence = fmt.Sprintf("No. %s has %s free (%d %%), and this database uses %s of it.", t.Drive, roomSize(t.FreeMB), pct, roomSize(mine))
	default:
		out.Sentence = fmt.Sprintf("No. The fullest drive, %s, has %s free (%d %%).", t.Drive, roomSize(t.FreeMB), pct)
	}
	return out
}
