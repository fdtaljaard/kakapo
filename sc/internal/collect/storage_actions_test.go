package collect

import (
	"strings"
	"testing"
)

// The storage fixes' read (docs/phase4d-contract.md, part 2): instant file initialization and the
// files, each in its own TRY with its error column; the dynamic SQL carries the tag. That it is
// tagged and only reads is checked with the Backups reads (TestStorageAndBackupActionReads).
func TestStorageFileStateQuery(t *testing.T) {
	if n := strings.Count(storageFileStateQuery, "BEGIN TRY"); n != 2 || strings.Count(storageFileStateQuery, "BEGIN CATCH") != 2 {
		t.Errorf("file state: %d TRY blocks", n)
	}
	for _, want := range []string{"instant_file_initialization_enabled", "N'/* kakapo */ SELECT TOP (1) @ifi", "sys.master_files AS mf",
		"tempdb.sys.database_files AS df", "@ifiError", "@filesError",
		"LEFT JOIN sys.master_files AS smf ON smf.database_id = 2 AND smf.file_id = df.file_id", "max_size, start_mb"} {
		if !strings.Contains(storageFileStateQuery, want) {
			t.Errorf("file state lacks %q", want)
		}
	}
}
