package domain

import (
	"strings"
	"testing"
	"time"
)

// The drive measured in the review of 6 Nov 2026 (docs/phase5l-contract.md): C:, 974,484 MB, 224,845 free.
var roomC = Volume{Name: `C:\`, TotalMB: 974484, FreeMB: 224845}

func TestVolumeOfPathTakesTheLongestMountPoint(t *testing.T) {
	vols := []Volume{roomC, {Name: `C:\Mounts\Data\`, TotalMB: 1000, FreeMB: 500}}
	cases := []struct {
		path, want string
		ok         bool
	}{
		{`C:\Program Files\Microsoft SQL Server\MSSQL\Backup`, `C:\`, true},
		{`c:\mounts\data\sql\x.mdf`, `C:\Mounts\Data\`, true},
		{`C:\Mounts\DataX\a`, `C:\`, true},
		{`D:\Backup`, "", false},
		{`\\nas\sql`, "", false},
		{"", "", false},
	}
	for _, c := range cases {
		v, ok := VolumeOfPath(c.path, vols)
		if ok != c.ok || v.Name != c.want {
			t.Errorf("VolumeOfPath(%q) = %q, %v; want %q, %v", c.path, v.Name, ok, c.want, c.ok)
		}
	}
}

func TestBuildRoomStates(t *testing.T) {
	part := func(mb int64) RoomPart { return RoomPart{Label: "x", MB: mb, Basis: "b"} }
	small := Volume{Name: `E:\`, TotalMB: 200 * 1024, FreeMB: 6 * 1024}
	tight := Volume{Name: `D:\`, TotalMB: 200 * 1024, FreeMB: 22 * 1024}
	f := RoomFacts{Volumes: []Volume{roomC, tight, small}}

	fits := BuildRoom([]RoomNeed{{Path: `C:\Backup`, Part: part(6344)}}, f)
	if len(fits) != 1 || fits[0].State != RoomFits || fits[0].Drive != "C:" || fits[0].LeftMB != 224845-6344 {
		t.Fatalf("fits: %+v", fits)
	}
	if want := "220 GB free now, this change uses about 6.2 GB, 213 GB left (22 % of 952 GB)."; fits[0].Sentence != want {
		t.Errorf("fits sentence = %q, want %q", fits[0].Sentence, want)
	}
	if got := BuildRoom([]RoomNeed{{Path: `D:\Backup`, Part: part(9 * 1024)}}, f); got[0].State != RoomTight {
		t.Errorf("13 GB left of 200 GB is under 10 %%: want tight, got %+v", got[0])
	}
	held := BuildRoom([]RoomNeed{{Path: `E:\Backup`, Part: part(9 * 1024)}}, f)
	if held[0].State != RoomNoFit || !RoomHeld(held) || !strings.Contains(held[0].Sentence, "3.0 GB short") {
		t.Errorf("does not fit: %+v", held[0])
	}
	share := BuildRoom([]RoomNeed{{Path: `\\nas\sql`, Part: part(6344)}}, f)
	if share[0].State != RoomUnknown || RoomHeld(share) || !strings.HasPrefix(share[0].Sentence, "SQL Server cannot report the free space of a share.") {
		t.Errorf("share: %+v", share[0])
	}
}

func TestBuildRoomOneLinePerDrive(t *testing.T) {
	f := RoomFacts{Volumes: []Volume{roomC}}
	lines := BuildRoom([]RoomNeed{
		{Path: `C:\Backup`, Part: RoomPart{Label: "backups", MB: 1000}},
		{Volume: `C:\`, Part: RoomPart{Label: "snapshot", MB: 0, Weak: true}},
		{Path: `C:\Data\x.mdf`, Part: RoomPart{Label: "index", MB: 500}},
	}, f)
	if len(lines) != 1 || len(lines[0].Parts) != 3 || lines[0].NeedMB != 1500 {
		t.Fatalf("want one C: line of 1,500 MB with three parts, got %+v", lines)
	}
}

func TestBuildRoomTrendAndUnbounded(t *testing.T) {
	now := time.Date(2026, 11, 6, 6, 0, 0, 0, time.UTC)
	// 10 GB lost in 5 days: 2 GB a day; 40 GB free after the change is 20 days.
	vol := Volume{Name: `D:\`, TotalMB: 100 * 1024, FreeMB: 41 * 1024}
	f := RoomFacts{Volumes: []Volume{vol}, Trends: map[string]DiskTrend{`D:\`: {
		Volume: `D:\`, OldestAt: now.AddDate(0, 0, -5), OldestFreeMB: 51 * 1024, NewestAt: now, NewestFreeMB: 41 * 1024}}}
	l := BuildRoom([]RoomNeed{{Path: `D:\x`, Part: RoomPart{MB: 1024}}}, f)[0]
	if l.State != RoomTight || l.DaysLeft == nil || *l.DaysLeft != 20 || !strings.Contains(l.Sentence, "about 20 days") {
		t.Errorf("trend: %+v", l)
	}
	// Samples less than a day apart give no trend (STO12's rule).
	f.Trends[`D:\`] = DiskTrend{OldestAt: now.Add(-time.Hour), OldestFreeMB: 60 * 1024, NewestAt: now, NewestFreeMB: 41 * 1024}
	if l := BuildRoom([]RoomNeed{{Path: `D:\x`, Part: RoomPart{MB: 1024}}}, f)[0]; l.DaysLeft != nil || l.State != RoomFits {
		t.Errorf("an hour apart: %+v", l)
	}
	u := BuildRoom([]RoomNeed{{Path: `C:\log.ldf`, Part: RoomPart{Label: "the log", Unbounded: true}}}, RoomFacts{Volumes: []Volume{roomC}})[0]
	if u.State != RoomTight || !strings.Contains(u.Sentence, "no fixed size") {
		t.Errorf("unbounded: %+v", u)
	}
}

func TestFullBackupMBOrder(t *testing.T) {
	// OldSchool: no backup in msdb, 3,688 MB used, the server's compression 21.5 %.
	b := BackupSizes{UsedDataMB: 3688, ServerRatio: 0.215}
	if mb, basis, weak := b.FullBackupMB(true); mb != 793 || weak || !strings.Contains(basis, "21.5 %") {
		t.Errorf("used x ratio: %d %q %v", mb, basis, weak)
	}
	if mb, _, weak := (BackupSizes{UsedDataMB: 4000}).FullBackupMB(true); mb != 1000 || !weak {
		t.Errorf("no compressed backup to learn from: %d %v", mb, weak)
	}
	if mb, _, _ := (BackupSizes{UsedDataMB: 3688}).FullBackupMB(false); mb != 3688 {
		t.Errorf("uncompressed: %d", mb)
	}
	last := BackupSizes{LastFullMB: 3693, LastFullCompressedMB: 921, UsedDataMB: 3688, ServerRatio: 0.215}
	if mb, _, _ := last.FullBackupMB(true); mb != 921 {
		t.Errorf("last full compressed: %d", mb)
	}
	if mb, _, _ := last.FullBackupMB(false); mb != 3693 {
		t.Errorf("last full uncompressed: %d", mb)
	}
}

func TestBackupScheduleNeedsOldSchool(t *testing.T) {
	s := BackupSchedule{Folder: `C:\Program Files\Microsoft SQL Server\MSSQL16.SQL2022\MSSQL\Backup`, Full: true, Compress: true, KeepDays: 7}
	needs := BackupScheduleNeeds(s, BackupSizes{UsedDataMB: 3688, ServerRatio: 0.215})
	if len(needs) != 1 || needs[0].Part.MB != 8*793 || needs[0].Part.Label != "8 full backups" {
		t.Fatalf("needs: %+v", needs)
	}
	s.Log = true
	needs = BackupScheduleNeeds(s, BackupSizes{UsedDataMB: 3688, ServerRatio: 0.215})
	if len(needs) != 2 || !needs[1].Part.Weak || needs[1].Part.MB != 0 {
		t.Errorf("log backups without history: %+v", needs)
	}
	if n := BackupScheduleNeeds(BackupSchedule{Folder: "", Full: true}, BackupSizes{}); n != nil {
		t.Errorf("no folder: %+v", n)
	}
}

func TestResolveRoles(t *testing.T) {
	paths := map[string]string{"@data:oldschool": `C:\Data\OldSchool.mdf`, "@tempdb": `T:\tempdb.mdf`, "@data:oldschool_archive": `F:\a.mdf`}
	got := ResolveRoles([]RoomNeed{{Path: RoleData}, {Path: RoleTempdb}, {Path: RoleOf(RoleData, "OldSchool_Archive")}, {Path: RoleErrorLog}, {Path: `D:\x`}}, "OldSchool", paths)
	want := []string{`C:\Data\OldSchool.mdf`, `T:\tempdb.mdf`, `F:\a.mdf`, RoleErrorLog, `D:\x`}
	for i := range want {
		if got[i].Path != want[i] {
			t.Errorf("need %d: %q, want %q", i, got[i].Path, want[i])
		}
	}
}

func TestBuildRoomUnknownSize(t *testing.T) {
	l := BuildRoom([]RoomNeed{{Path: `C:\x`, Part: RoomPart{Label: "sorts", Weak: true}}}, RoomFacts{Volumes: []Volume{roomC}})[0]
	if l.State != RoomFits || !strings.Contains(l.Sentence, "how much this change uses is not known yet") {
		t.Errorf("unknown size: %+v", l)
	}
}
