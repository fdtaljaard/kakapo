package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

// CheckdbRoom is the answer to STO10, "is there room to run DBCC CHECKDB on this database?", read
// on request and stored (docs/phase3b-contract.md).
type CheckdbRoom struct {
	CapturedAt      time.Time `json:"capturedAt"`
	DurationSeconds float64   `json:"durationSeconds"`
	EstimateKB      int64     `json:"estimateKB"`
	NeedMB          int64     `json:"needMB"`
	TempdbFreeMB    int64     `json:"tempdbFreeMB"`
	TempdbRoomMB    int64     `json:"tempdbRoomMB"`
	DataVolume      string    `json:"dataVolume"`
	DataFreeMB      int64     `json:"dataFreeMB"`
	DataSizeMB      int64     `json:"dataSizeMB"`
	Finding         Finding   `json:"finding"` // STO10 as a finding row (Info when passed)
}

// CheckdbRoomInputs are the numbers STO10 compares: SQL Server's estimate and what tempdb and the
// data volume have room for.
type CheckdbRoomInputs struct {
	Database     string
	CapturedAt   time.Time // UTC
	Duration     time.Duration
	EstimateKB   int64 // the sum of the estimate's rows
	TempdbFreeMB int64 // unallocated extents inside tempdb's data files
	TempdbRoomMB int64 // that plus what autogrowth can add
	DataVolume   string
	DataFreeMB   int64 // the least free space among the volumes holding the data files
	DataSizeMB   int64 // the data files' used MB
}

// Texts of STO10, from the contract.
const (
	sto10Why = "CHECKDB sorts its work in tempdb and takes an internal snapshot of the database on the data volume; if either runs out of room the check fails part-way, and on a busy server it can fill tempdb for every other database too. The snapshot grows with every change made while the check runs, so the figure is an estimate; run CHECKDB in a quiet period."
	sto10Do  = "Free space on %s or let tempdb grow to %s. Alternatives: run DBCC CHECKDB WITH PHYSICAL_ONLY (needs far less), or run the full check on a restored copy of the last backup, which also tests the backup. Run it in a quiet period."
)

// NeedMB is what CHECKDB needs in tempdb: the estimate, taken as low, doubled; whole MB, rounded up.
func NeedMB(estimateKB int64) int64 {
	return int64(math.Ceil(float64(estimateKB) * 2 / 1024))
}

// BuildCheckdbRoom compares the estimate with the room in tempdb and on the data volume and states
// the verdict as an STO10 finding.
func BuildCheckdbRoom(in CheckdbRoomInputs) CheckdbRoom {
	need := NeedMB(in.EstimateKB)
	r := CheckdbRoom{
		CapturedAt: in.CapturedAt.UTC(), DurationSeconds: math.Round(in.Duration.Seconds()*1000) / 1000,
		EstimateKB: in.EstimateKB, NeedMB: need, TempdbFreeMB: in.TempdbFreeMB, TempdbRoomMB: in.TempdbRoomMB,
		DataVolume: in.DataVolume, DataFreeMB: in.DataFreeMB, DataSizeMB: in.DataSizeMB,
	}

	rank, sentence := 5, ""
	switch {
	case need > in.TempdbRoomMB:
		rank = 2
		sentence = fmt.Sprintf("CHECKDB on %s needs about %s of tempdb, but tempdb can only reach %s.",
			in.Database, mbText(need), mbText(in.TempdbRoomMB))
	case need > in.TempdbFreeMB:
		rank = 3
		sentence = fmt.Sprintf("CHECKDB on %s needs about %s of tempdb; tempdb has %s free and would have to grow by %s.",
			in.Database, mbText(need), mbText(in.TempdbFreeMB), mbText(need-in.TempdbFreeMB))
	}
	if in.DataFreeMB*10 < in.DataSizeMB {
		dataRank := 3
		if in.DataFreeMB*50 < in.DataSizeMB {
			dataRank = 2
		}
		dataSentence := fmt.Sprintf("The data volume has %s free for a database of %s; the snapshot CHECKDB takes needs room to grow while it runs.",
			mbText(in.DataFreeMB), mbText(in.DataSizeMB))
		if sentence == "" {
			sentence = dataSentence
		} else {
			sentence += " " + dataSentence
		}
		rank = min(rank, dataRank)
	}
	if sentence == "" {
		sentence = fmt.Sprintf("CHECKDB on %s needs about %s of tempdb; tempdb has %s free and the data volume has %s free.",
			in.Database, mbText(need), mbText(in.TempdbFreeMB), mbText(in.DataFreeMB))
	}

	chk, _ := CheckByID("STO10")
	r.Finding = Finding{
		CheckID: chk.ID, Title: chk.Title, Severity: SeverityFromRank(rank), Owner: chk.Owner,
		When: WhenDecideFirst, Effort: EffortModerate, Object: in.Database,
		Finding: sentence, Why: sto10Why, DoThis: fmt.Sprintf(sto10Do, in.DataVolume, mbText(need)),
		ThenCheck: chk.ThenCheck,
		Evidence: fmt.Sprintf("Estimate: %s KB. Need: %s MB (twice the estimate). TempDB free: %s MB, room to grow to: %s MB. Data volume %s: %s MB free. Data size: %s MB. The estimate took %.1f seconds.",
			n0int(in.EstimateKB), n0int(need), n0int(in.TempdbFreeMB), n0int(in.TempdbRoomMB), in.DataVolume,
			n0int(in.DataFreeMB), n0int(in.DataSizeMB), in.Duration.Seconds()),
	}
	return r
}

// mbText writes a size for a sentence: "640 MB", or "4.4 GB" from 1 GB.
func mbText(mb int64) string {
	if mb >= 1024 {
		return n1(float64(mb)/1024) + " GB"
	}
	return n0int(mb) + " MB"
}

// CheckdbRoomInputsFrom takes tempdb's and the data volume's numbers from the storage facts; the
// estimate, the time it took and when are the caller's. It fails when the facts do not hold them.
func CheckdbRoomInputsFrom(f StorageFacts) (CheckdbRoomInputs, error) {
	in := CheckdbRoomInputs{Database: f.Database}
	switch {
	case f.Error != "":
		return in, errors.New(f.Error)
	case f.FilesError != "":
		return in, errors.New("could not read the database files: " + f.FilesError)
	case f.VolumeError != "":
		return in, errors.New("could not read the volumes: " + f.VolumeError)
	case f.TempdbFreeMB == nil:
		msg := "could not read the free space in tempdb"
		if f.TempdbError != "" {
			msg += ": " + f.TempdbError
		}
		return in, errors.New(msg)
	}
	in.TempdbFreeMB = *f.TempdbFreeMB
	in.TempdbRoomMB = in.TempdbFreeMB + f.TempdbGrowthMB()

	v, ok := f.dataVolume()
	if !ok {
		return in, errors.New("could not tell which volume holds the data files")
	}
	in.DataVolume, in.DataFreeMB = v.Name, v.FreeMB
	for _, x := range f.DatabaseFiles() {
		if !x.isData() {
			continue
		}
		if x.UsedMB != nil {
			in.DataSizeMB += *x.UsedMB
		} else {
			in.DataSizeMB += x.SizeMB
		}
	}
	return in, nil
}

// TempdbGrowthMB is what autogrowth can add to tempdb's data files: for each file that grows,
// its max size (when it has one) less its size, else the free space on its volume; summed per
// volume and capped by that volume's free space.
func (f StorageFacts) TempdbGrowthMB() int64 {
	room, free := map[string]int64{}, map[string]int64{}
	for _, x := range f.tempdbData() {
		if x.Growth <= 0 {
			continue
		}
		r := x.VolumeFreeMB
		if x.MaxSizePages == 0 || maxSizeSet(x.MaxSizePages) {
			r = max(0, x.MaxSizePages*8/1024-x.SizeMB)
		}
		room[x.Volume] += r
		free[x.Volume] = x.VolumeFreeMB
	}
	total := int64(0)
	for v, r := range room {
		total += min(r, free[v])
	}
	return total
}

// MarshalJSON writes checkdbRoom for the Storage card, null when it was never run, and leaves it
// out for every other card; it writes checkdbRun for the Backups and Storage cards, null when no
// CHECKDB ran from the app, and leaves it out for every other card; it writes fixes for the Setup and Upkeep cards, [] when none, and
// leaves them out for every other card; it writes deadlocks for the Performance card, null when
// the report was never run, and leaves it out for every other card; the same for blockingHistory.
func (d CardDetail) MarshalJSON() ([]byte, error) {
	type plain CardDetail
	if d.Card.ID != CardPerformance {
		d.Blocking, d.Trend = nil, nil
	}
	if d.Card.ID == CardSetup || d.Card.ID == CardUpkeep {
		// The cards with fixes always say which are available, [] when none.
		fixes := d.Fixes
		if fixes == nil {
			fixes = []Fix{}
		}
		d.Fixes = nil
		return json.Marshal(struct {
			plain
			Fixes []Fix `json:"fixes"`
		}{plain(d), fixes})
	}
	if d.Card.ID == CardPerformance {
		// The Performance card always says what the last deadlock report and the last blocking
		// history found, null before a run.
		last, blocking, trend := d.Deadlocks, d.Blocking, d.Trend
		d.Deadlocks, d.Blocking, d.Trend = nil, nil, nil
		return json.Marshal(struct {
			plain
			Deadlocks *DeadlockReport   `json:"deadlocks"`
			Blocking  *BlockingHistory  `json:"blockingHistory"`
			Trend     *PerformanceTrend `json:"trend"`
		}{plain(d), last, blocking, trend})
	}
	run := d.CheckdbRun
	d.CheckdbRun = nil
	if d.Card.ID == CardBackups {
		return json.Marshal(struct {
			plain
			CheckdbRun *CheckdbRun `json:"checkdbRun"`
		}{plain(d), run})
	}
	if d.Card.ID != CardStorage {
		return json.Marshal(plain(d))
	}
	return json.Marshal(struct {
		plain
		CheckdbRoom *CheckdbRoom `json:"checkdbRoom"`
		CheckdbRun  *CheckdbRun  `json:"checkdbRun"`
	}{plain(d), d.CheckdbRoom, run})
}
