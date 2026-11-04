package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

func TestStorageSamplesAndDiskTrend(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if tr, err := st.DiskTrend(ctx, 1, `D:\`); err != nil || tr != nil {
		t.Fatalf("no samples: %+v %v", tr, err)
	}

	used, pct := int64(3500), 12.5
	sample := &domain.StorageSample{DataAllocMB: new(int64), DataUsedMB: &used, LogUsedPct: &pct}
	newest := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	for _, s := range []struct {
		at   time.Time
		free int64
	}{
		{newest.Add(-40 * 24 * time.Hour), 90000}, // outside the 30 days
		{newest.Add(-20 * 24 * time.Hour), 70000}, // the oldest within them
		{newest.Add(-12 * time.Hour), 52000},
		{newest, 51200},
	} {
		vols := []domain.VolumeSample{{Volume: `D:\`, FreeMB: s.free, TotalMB: 102400}, {Volume: `L:\`, FreeMB: 1, TotalMB: 2}}
		if err := st.InsertStorageSamples(ctx, 1, "GraniteLive", s.at, vols, sample); err != nil {
			t.Fatal(err)
		}
	}
	// Another server keeps its own.
	if err := st.InsertStorageSamples(ctx, 2, "GraniteLive", newest.Add(time.Hour), []domain.VolumeSample{{Volume: `D:\`, FreeMB: 5, TotalMB: 10}}, nil); err != nil {
		t.Fatal(err)
	}

	tr, err := st.DiskTrend(ctx, 1, `D:\`)
	if err != nil || tr == nil {
		t.Fatalf("trend: %+v %v", tr, err)
	}
	want := domain.DiskTrend{Volume: `D:\`, OldestAt: newest.Add(-20 * 24 * time.Hour), OldestFreeMB: 70000, NewestAt: newest, NewestFreeMB: 51200}
	if !tr.OldestAt.Equal(want.OldestAt) || !tr.NewestAt.Equal(want.NewestAt) || tr.OldestFreeMB != want.OldestFreeMB ||
		tr.NewestFreeMB != want.NewestFreeMB || tr.Volume != want.Volume || tr.NewestAt.Location() != time.UTC {
		t.Errorf("trend %+v\nwant %+v", *tr, want)
	}

	var n, nulls int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*), count(*) - count(log_size_mb) FROM storage_sample WHERE server_id = 1`).Scan(&n, &nulls); err != nil || n != 4 || nulls != 4 {
		t.Errorf("storage samples %d, null log sizes %d, %v", n, nulls, err)
	}
	var dataUsed int64
	var logPct float64
	if err := st.db.QueryRowContext(ctx, `SELECT data_used_mb, log_used_pct FROM storage_sample LIMIT 1`).Scan(&dataUsed, &logPct); err != nil || dataUsed != 3500 || logPct != 12.5 {
		t.Errorf("storage sample %d %v %v", dataUsed, logPct, err)
	}
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM volume_sample`).Scan(&n); err != nil || n != 9 {
		t.Errorf("volume samples %d %v", n, err)
	}
	// Nothing to keep: nothing written.
	if err := st.InsertStorageSamples(ctx, 3, "X", newest, nil, nil); err != nil {
		t.Fatal(err)
	}
}

// Phase 4c: a live sample round trip; the latest of a database is returned, another database's
// and server's are kept apart.
func TestPerfSampleRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "test.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if p, err := st.LastPerfSample(ctx, 1, "GraniteLive"); p != nil || err != nil {
		t.Fatalf("none yet: %+v %v", p, err)
	}
	at := time.Date(2026, 11, 3, 8, 15, 30, 123456000, time.UTC)
	older := domain.PerfSample{Database: "GraniteLive", CapturedAt: at.Add(-time.Hour), Seconds: 30, Verdict: "old",
		Lines: []domain.SampleLine{}, Waits: []domain.SampleWait{}}
	newer := domain.PerfSample{Database: "GraniteLive", CapturedAt: at, Seconds: 15, ElapsedSeconds: 15.2, Verdict: "Session 83 has blocked 14 sessions.",
		Lines:    []domain.SampleLine{{Area: domain.SampleAreaBlocking, Text: "Session 83 has blocked 14 sessions."}, {Area: domain.SampleAreaQueries, Text: "q", QueryHash: "0xAA"}},
		Waits:    []domain.SampleWait{{Type: "LCK_M_X", Group: "LOCK", Words: "blocking", WaitMs: 60000, Tasks: 30}},
		CPU:      &domain.SampleCPU{SQL: 70, Other: 25, Records: 1, InWindow: true, Schedulers: 8},
		Queries:  []domain.SampleQuery{{QueryHash: "0xAA", ObjectName: "dbo.PickNext", CPUMs: 60000, Runs: 12}},
		Blocking: domain.SampleBlocking{HeadBlockers: []domain.SampleBlocker{{Session: 83, Blocked: 14, LongestSec: 390}}, Blocked: 14, LongestSec: 390},
		Memory:   &domain.SampleMemory{PLEStart: 4000, PLEEnd: 120}, FilesError: "denied"}
	for _, p := range []domain.PerfSample{newer, older} {
		if err := st.InsertPerfSample(ctx, 1, "GraniteLive", p); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.InsertPerfSample(ctx, 2, "GraniteLive", domain.PerfSample{CapturedAt: at.Add(time.Hour), Verdict: "other server"}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertPerfSample(ctx, 1, "Other", domain.PerfSample{CapturedAt: at.Add(time.Hour), Verdict: "other db"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.LastPerfSample(ctx, 1, "GraniteLive")
	if err != nil || got == nil {
		t.Fatalf("last: %+v %v", got, err)
	}
	if !got.CapturedAt.Equal(at) || got.CapturedAt.Location() != time.UTC || got.Verdict != newer.Verdict || got.Seconds != 15 ||
		got.ElapsedSeconds != 15.2 || len(got.Lines) != 2 || got.Lines[1].QueryHash != "0xAA" || got.CPU == nil || got.CPU.Other != 25 ||
		len(got.Queries) != 1 || got.Queries[0].ObjectName != "dbo.PickNext" || got.Blocking.HeadBlockers[0].Session != 83 ||
		got.Memory.PLEEnd != 120 || got.FilesError != "denied" {
		t.Errorf("round trip %+v", got)
	}
	var n int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM perf_sample`).Scan(&n); err != nil || n != 4 {
		t.Errorf("rows %d %v", n, err)
	}
}
