package collect

import (
	"context"
	"os"
	"testing"

	"sqlmonitor/internal/domain"
)

// TestSetupFactsLive reads the setup facts of one database on a real instance, read-only, when
// SQLMONITOR_TEST_HOST (Windows authentication) and SQLMONITOR_TEST_DB name one: INS17's reads
// (the schedulers, the affinity, the VM flag, the processor layout from 2016 SP2) come back whole.
func TestSetupFactsLive(t *testing.T) {
	host, name := os.Getenv("SQLMONITOR_TEST_HOST"), os.Getenv("SQLMONITOR_TEST_DB")
	if host == "" || name == "" {
		t.Skip("set SQLMONITOR_TEST_HOST and SQLMONITOR_TEST_DB to read a live instance")
	}
	ctx := context.Background()
	db, err := Open(ctx, domain.ServerInput{Host: host, Auth: domain.AuthWindows})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f, err := SetupFacts(ctx, db, name)
	if err != nil {
		t.Fatal(err)
	}
	if f.SysInfoError != "" || f.CPUCount == 0 || f.OnlineSchedulers == 0 || f.AffinityType == "" {
		t.Errorf("facts: %+v", f)
	}
	if f.Major() >= 14 && (f.Sockets == 0 || f.CoresPerSocket == 0) {
		t.Errorf("no processor layout on %s", f.ProductVersion)
	}
	t.Logf("%s %s: cpu %d, schedulers %d online %d offline, runnable %d, affinity %s, vm %v, %d sockets x %d cores",
		f.Edition, f.ProductVersion, f.CPUCount, f.OnlineSchedulers, f.OfflineSchedulers, f.RunnableNow, f.AffinityType,
		f.VirtualMachine, f.Sockets, f.CoresPerSocket)
}
