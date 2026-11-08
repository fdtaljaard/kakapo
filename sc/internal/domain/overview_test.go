package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var asOf = time.Date(2026, 11, 3, 9, 21, 0, 0, time.UTC)

// readDB is an online database whose Query Store was read with the given state.
func readDB(name string, q QueryStoreStatus) DatabaseStatus {
	return DatabaseStatus{Name: name, State: "ONLINE", QueryStore: &q}
}

func offQS(canFix bool) QueryStoreStatus {
	return QueryStoreStatus{ActualState: "OFF", DesiredState: "OFF", Verdict: VerdictOff, CanFix: canFix, Summary: "Off",
		MaxStorageMb: 100, CaptureMode: "AUTO", IntervalLengthMinutes: 60, StaleQueryThresholdDays: 30,
		FlushIntervalSeconds: 900, MaxPlansPerQuery: 200, SizeBasedCleanupMode: "AUTO", WaitStatsCaptureMode: "ON",
		Drift: []Drift{{"OPERATION_MODE", "OFF", "READ_WRITE"}, {"QUERY_CAPTURE_MODE", "AUTO", "AUTO"}}}
}

func driftQS(canFix bool, drift ...Drift) QueryStoreStatus {
	return QueryStoreStatus{ActualState: "READ_WRITE", DesiredState: "READ_WRITE", Verdict: VerdictDrift, CanFix: canFix,
		Summary: "On, 4% full, 60-min intervals, 30 days", CurrentStorageMb: 40, MaxStorageMb: 1000, CaptureMode: "AUTO",
		IntervalLengthMinutes: 60, StaleQueryThresholdDays: 30, FlushIntervalSeconds: 900, MaxPlansPerQuery: 200,
		SizeBasedCleanupMode: "AUTO", WaitStatsCaptureMode: "ON", Drift: drift}
}

func okQS() QueryStoreStatus {
	return QueryStoreStatus{ActualState: "READ_WRITE", DesiredState: "READ_WRITE", Verdict: VerdictOK, CanFix: true,
		Summary: "On, 3% full, 15-min intervals, 30 days", CurrentStorageMb: 61, MaxStorageMb: 2048, CaptureMode: "AUTO",
		IntervalLengthMinutes: 15, StaleQueryThresholdDays: 30, FlushIntervalSeconds: 900, MaxPlansPerQuery: 200,
		SizeBasedCleanupMode: "AUTO", WaitStatsCaptureMode: "ON", Drift: []Drift{}}
}

func wantFacts(t *testing.T, c Card, want ...string) {
	t.Helper()
	got := []string{}
	for _, f := range c.Facts {
		got = append(got, f.Label+"="+f.Value+"/"+f.Tone)
	}
	if strings.Join(got, "; ") != strings.Join(want, "; ") {
		t.Errorf("facts\n got %q\nwant %q", got, want)
	}
}

func TestBuildCardsOrderAndGreyCards(t *testing.T) {
	cards := BuildCards(nil, nil, nil, nil, nil, nil, nil)
	wantIDs := []CardID{CardBackups, CardSetup, CardStorage, CardUpkeep, CardPerformance, CardDesign}
	for i, c := range cards {
		if c.ID != wantIDs[i] || c.Title == "" || c.Question == "" {
			t.Errorf("card %d: %+v", i, c)
		}
		if c.ID == CardSetup {
			if c.Status != StatusGrey || c.StatusText != "Not checked yet" {
				t.Errorf("setup without facts: %+v", c)
			}
			continue
		}
		if c.Status != StatusGrey || c.StatusText != "Not checked yet" || c.Verdict != "" || c.AsOf != nil ||
			c.ToDo+c.ForClientIT+c.Fixable != 0 || c.Facts == nil || len(c.Facts) != 0 || c.Note == "" {
			t.Errorf("grey card %s: %+v", c.ID, c)
		}
	}
	// Facts must be [] and asOf null in JSON, not missing.
	b, _ := json.Marshal(cards[0])
	if !strings.Contains(string(b), `"facts":[]`) || !strings.Contains(string(b), `"asOf":null`) {
		t.Errorf("grey card JSON: %s", b)
	}
}

func TestOverviewJSONCarriesDatabase(t *testing.T) {
	o := Overview{Instance: InstanceInfo{ProductName: "SQL Server 2019"}, Database: readDB("WarehouseLive", okQS()), Cards: []Card{}, RecentChanges: []ChangeEntry{}}
	b, _ := json.Marshal(o)
	if !strings.Contains(string(b), `"database":{"name":"WarehouseLive","state":"ONLINE"`) {
		t.Errorf("overview JSON: %s", b)
	}
}

func TestUptimeDays(t *testing.T) {
	start := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	if got := UptimeDays(start, start.Add(5*24*time.Hour+3*time.Hour)); got != 5.1 {
		t.Errorf("uptime %v", got)
	}
	if got := UptimeDays(start, start.Add(-time.Hour)); got != 0 {
		t.Errorf("clock skew: %v", got)
	}
}
