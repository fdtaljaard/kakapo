package domain

import (
	"fmt"
	"math"
	"time"
)

// VolumeSample is the free space on one volume holding this database's files, kept on every
// storage read (docs/phase3b-contract.md, addendum C).
type VolumeSample struct {
	Volume  string
	FreeMB  int64
	TotalMB int64
}

// StorageSample is the database's sizes, kept on every storage read; nil where not read.
type StorageSample struct {
	DataAllocMB   *int64
	DataUsedMB    *int64
	LogSizeMB     *int64
	LogUsedPct    *float64
	TempdbAllocMB *int64
	TempdbUsedMB  *int64
}

// Samples are what a storage read keeps: one row per volume this database's files are on (none
// when the files or the volumes could not be read) and one row of the database's sizes (nil when
// nothing could be read or the database is not online).
func (f StorageFacts) Samples() ([]VolumeSample, *StorageSample) {
	if f.Error != "" || f.State != "ONLINE" {
		return nil, nil
	}
	vols := []VolumeSample{}
	if f.FilesError == "" && f.VolumeError == "" {
		for _, v := range volumesOf(f.DatabaseFiles()) {
			vols = append(vols, VolumeSample{Volume: v.Name, FreeMB: v.FreeMB, TotalMB: v.TotalMB})
		}
	}
	s := &StorageSample{LogUsedPct: f.LogUsedPct, LogSizeMB: f.LogSizeMB}
	if f.LogSpaceError != "" {
		s.LogUsedPct, s.LogSizeMB = nil, nil
	}
	if f.FilesError == "" {
		var alloc, used, logMB int64
		allUsed, hasLog := f.SpaceUsedError == "", false
		for _, x := range f.DatabaseFiles() {
			switch {
			case x.isData():
				alloc += x.SizeMB
				if x.UsedMB == nil {
					allUsed = false
				} else {
					used += *x.UsedMB
				}
			case x.isLog():
				logMB += x.SizeMB
				hasLog = true
			}
		}
		s.DataAllocMB = &alloc
		if allUsed {
			s.DataUsedMB = &used
		}
		if s.LogSizeMB == nil && hasLog {
			s.LogSizeMB = &logMB
		}
	}
	if f.TempdbAllocMB != nil && f.TempdbFreeMB != nil {
		alloc, used := *f.TempdbAllocMB, *f.TempdbAllocMB-*f.TempdbFreeMB
		s.TempdbAllocMB, s.TempdbUsedMB = &alloc, &used
	}
	return vols, s
}

// tightestVolume is the volume holding this database's files with the least free space as a
// share of its size.
func (f StorageFacts) tightestVolume() (volume, bool) {
	vs := volumesOf(f.DatabaseFiles())
	if len(vs) == 0 {
		return volume{}, false
	}
	tight := vs[0]
	for _, v := range vs[1:] {
		if v.freePct() < tight.freePct() {
			tight = v
		}
	}
	return tight, true
}

// TightestVolume names the volume STO12 forecasts: the one holding this database's files with
// the least free space as a share of its size. false when the volumes were not read.
func (f StorageFacts) TightestVolume() (string, bool) {
	if f.Error != "" || f.FilesError != "" || f.VolumeError != "" {
		return "", false
	}
	v, ok := f.tightestVolume()
	return v.Name, ok
}

// DiskTrend is the pair of stored samples of one volume STO12 forecasts from: the newest, and the
// oldest within the 30 days before it (which may be the newest itself).
type DiskTrend struct {
	Volume       string
	OldestAt     time.Time // UTC
	OldestFreeMB int64
	NewestAt     time.Time // UTC
	NewestFreeMB int64
}

// sto12: when the tightest volume will be full at the rate its free space fell between two
// samples a day or more apart.
func (b *storageBuild) sto12() {
	t := b.trend
	if t == nil {
		b.skipped["STO12"] = sto12NeedsPair + "."
		return
	}
	span := t.NewestAt.Sub(t.OldestAt)
	if span < 24*time.Hour {
		b.skipped["STO12"] = fmt.Sprintf("%s; the first was kept %s.", sto12NeedsPair, utcText(t.OldestAt))
		return
	}
	days := span.Hours() / 24
	nDays := max(1, int(math.Round(days)))
	rate := float64(t.OldestFreeMB-t.NewestFreeMB) / days // MB a day
	evidence := fmt.Sprintf("Free on %s: %s MB at %s, %s MB at %s.", t.Volume, n0int(t.OldestFreeMB), utcText(t.OldestAt),
		n0int(t.NewestFreeMB), utcText(t.NewestAt))
	if rate <= 0 {
		b.add("STO12", 5, "Planned", EffortModerate, t.Volume,
			fmt.Sprintf("%s has not lost free space over the last %d days.", t.Volume, nDays), sto12Why, sto12Do,
			fmt.Sprintf("%s freed per day over %d days. %s", rateText(-rate), nDays, evidence))
		return
	}
	left := float64(t.NewestFreeMB) / rate
	leftDays := int(math.Floor(left))
	rank := 5
	switch {
	case left < 7:
		rank = 2
	case left < 30:
		rank = 3
	}
	finding := fmt.Sprintf("At the rate of the last %d days (%s a day), %s will be full in about %d days.", nDays, rateText(rate), t.Volume, leftDays)
	if rank == 5 {
		evidence = fmt.Sprintf("At %s/day, full in about %d days. %s", rateText(rate), leftDays, evidence)
	}
	b.add("STO12", rank, "Planned", EffortModerate, t.Volume, finding, sto12Why, sto12Do, evidence)
}

// sto12Days is the days left STO12 found, when it fires.
func (b *storageBuild) sto12Days() (int, Finding, bool) {
	for _, x := range b.all("STO12") {
		if !x.Severity.Actionable() || b.trend == nil {
			continue
		}
		t := b.trend
		rate := float64(t.OldestFreeMB-t.NewestFreeMB) / (t.NewestAt.Sub(t.OldestAt).Hours() / 24)
		return int(math.Floor(float64(t.NewestFreeMB) / rate)), x, true
	}
	return 0, Finding{}, false
}

// rateText writes MB a day: "640 MB", or "1.2 GB" from 1 GB.
func rateText(mbPerDay float64) string { return mbText(int64(math.Round(mbPerDay))) }

// utcText writes a stored UTC time for a note.
func utcText(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") + " UTC" }
