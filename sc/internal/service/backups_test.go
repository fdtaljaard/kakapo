package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"sqlmonitor/internal/domain"
)

// backupScheduleFacts is a FULL-recovery database never backed up, on an instance whose Agent runs,
// with no backup job and D:\Backup as the default folder.
func backupScheduleFacts() domain.BackupFacts {
	now := time.Date(2026, 11, 3, 9, 21, 0, 0, time.UTC)
	return domain.BackupFacts{
		Database: "Scratch", State: "ONLINE", RecoveryModel: "FULL", CreateDate: now.AddDate(0, -1, 0),
		LastGoodCheckDb: &now, ServerNow: now, ServerName: `SQL01\WMS`, Edition: "Developer Edition (64-bit)",
		HasAgent: true, AgentRunning: true, AgentStatus: "Running", JobsRead: true, BackupJobs: []domain.BackupJob{},
		KakapoJobs: []string{}, DefaultBackupDir: `E:\Backup`, CompressionOK: true, DataFiles: []string{`D:\Data\Scratch.mdf`},
	}
}

// The folder check sits between reading the facts and building the preview: a folder the server
// does not see gives a preview with the warning and no changes, so Apply is disabled.
func TestBackupSchedulePreviewBadFolder(t *testing.T) {
	f := backupScheduleFacts()
	checked := []string{}
	exists := func(ok bool) func(string) (bool, error) {
		return func(folder string) (bool, error) {
			checked = append(checked, folder)
			return ok, nil
		}
	}
	p, fix, err := buildBackupFix(domain.FixBackupSchedule, f, map[string]string{"folder": `X:\nowhere\`}, exists(false))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Changes) != 0 || p.Statement != "" || fix.ID != domain.FixBackupSchedule ||
		p.Warnings[0] != `The folder X:\nowhere does not exist on SQL01\WMS (as the SQL Server service sees it): create it, or type another.` {
		t.Errorf("bad folder preview: %+v", p)
	}
	if len(checked) != 1 || checked[0] != `X:\nowhere` {
		t.Errorf("checked %q", checked)
	}

	// The default folder, which exists: changes and the batch.
	checked = nil
	p, _, err = buildBackupFix(domain.FixBackupSchedule, f, nil, exists(true))
	if err != nil || len(p.Changes) != 6 || !strings.Contains(p.Statement, "sp_add_job") || checked[0] != `E:\Backup` {
		t.Errorf("good folder: %v %+v", err, p)
	}
	// On the data drive: still applicable, with the warning.
	p, _, _ = buildBackupFix(domain.FixBackupSchedule, f, map[string]string{"folder": `D:\Backup`}, exists(true))
	if len(p.Changes) == 0 || !strings.HasPrefix(p.Warnings[0], "This folder is on the same drive as the data files (D:)") {
		t.Errorf("same drive: %q", p.Warnings)
	}
	// A relative folder is not sent to the server.
	checked = nil
	if p, _, _ = buildBackupFix(domain.FixBackupSchedule, f, map[string]string{"folder": "Backup"}, exists(true)); len(p.Changes) != 0 || len(checked) != 0 {
		t.Errorf("relative folder: checked %q, %+v", checked, p)
	}
	// A check the server refuses names the permission.
	_, _, err = buildBackupFix(domain.FixBackupSchedule, f, nil, func(string) (bool, error) { return false, errors.New("EXECUTE permission denied") })
	var se *Error
	if !errors.As(err, &se) || se.Kind != KindUnreachable || !strings.Contains(err.Error(), "Needs sysadmin on SQL01\\WMS") {
		t.Errorf("refused check: %v", err)
	}
	// Nothing to apply: the finding did not offer the fix; nothing to remove.
	f.JobsRead = false
	if _, _, err := buildBackupFix(domain.FixBackupSchedule, f, nil, exists(true)); !errors.Is(err, domain.ErrFixNotApplicable) {
		t.Errorf("not offered: %v", err)
	}
	if _, _, err := buildBackupFix(domain.FixRemoveBackupSchedule, backupScheduleFacts(), nil, exists(true)); !errors.As(err, &se) || se.Kind != KindNotFound {
		t.Errorf("nothing to remove: %v", err)
	}
}

// A full backup time that is not HH:MM, through the preview the service builds
// (PreviewFixWithOptions calls buildBackupFix after reading the facts): the warning and no changes
// (docs/phase4b-contract.md).
func TestBackupSchedulePreviewBadTime(t *testing.T) {
	p, _, err := buildBackupFix(domain.FixBackupSchedule, backupScheduleFacts(), map[string]string{"fullTime": "25:00"},
		func(string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Changes) != 0 || p.Statement != "" || p.Warnings[0] != "The full backup time must be HH:MM, for example 22:00." {
		t.Errorf("bad time preview: %+v", p)
	}
}
