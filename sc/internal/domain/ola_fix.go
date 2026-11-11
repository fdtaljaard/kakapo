package domain

import (
	"embed"
	"fmt"
	"strings"
)

// Phase 5u (docs/phase5u-contract.md): kakapo installs Ola Hallengren's IndexOptimize. install-ola-index
// creates his CommandLog table and his CommandExecute and IndexOptimize procedures in master, from the
// copy in ola/ (his separate scripts, unchanged, MIT licence; ola/SOURCE.md). Not his DatabaseBackup or
// DatabaseIntegrityCheck: kakapo's maintenance procedure switches to them as soon as they exist. No jobs.
// Only into an empty slot: never over a CommandExecute someone else installed.

//go:embed ola/CommandLog.sql ola/CommandExecute.sql ola/IndexOptimize.sql ola/LICENSE
var olaFiles embed.FS

const (
	// FixInstallOlaIndex installs Ola Hallengren's IndexOptimize (server scope; the Index maintenance row).
	FixInstallOlaIndex = "install-ola-index"
	// OlaVersion is the version of the copy in ola/: the "--// Version: " line of both procedures.
	OlaVersion = "2026-09-12 15:02:38"
	// olaMinMajor is SQL Server 2017: Ola's current script names 2017 and later, and IndexOptimize uses
	// STRING_AGG and other 2017 functions.
	olaMinMajor = 14
)

// olaScripts are the files in the order they run: the table, then CommandExecute, which IndexOptimize
// checks for.
var olaScripts = []string{"CommandLog.sql", "CommandExecute.sql", "IndexOptimize.sql"}

// OlaIndexFacts is what install-ola-index is built from, and its read-back.
type OlaIndexFacts struct {
	ServerName     string
	ProductVersion string
	IndexOptimize  bool   // master.dbo.IndexOptimize exists
	CommandExecute bool   // master.dbo.CommandExecute exists
	CommandLog     bool   // master.dbo.CommandLog exists
	Version        string // Ola's version line of IndexOptimize, else CommandExecute; "" when neither
	MaintProc      bool   // kakapo's maintenance procedure (master.dbo.kakapo_Maintenance) is installed
}

// OlaInstallBlock is why kakapo does not install Ola's IndexOptimize on a server where it is missing:
// "" when it can. Said on the Index maintenance row and as the preview's warning.
func OlaInstallBlock(productVersion string, commandExecute bool, version string) string {
	if m := EngineMajor(productVersion); m > 0 && m < olaMinMajor {
		return fmt.Sprintf("Ola Hallengren's current IndexOptimize needs SQL Server 2017 or later; this server is %s.", ProductName(m))
	}
	if commandExecute {
		v := ""
		if version != "" {
			v = " (version " + version + ")"
		}
		return "Ola Hallengren's CommandExecute" + v + " is already in master, without IndexOptimize. kakapo does not change " +
			"someone else's install: add IndexOptimize.sql from the same version (https://ola.hallengren.com)."
	}
	return ""
}

// olaScript is one of the files as it runs: without its byte order mark, with LF line ends.
func olaScript(name string) string {
	b, err := olaFiles.ReadFile("ola/" + name)
	if err != nil {
		panic("ola: " + err.Error()) // embedded: a missing file is a build error
	}
	s := strings.TrimPrefix(string(b), string(rune(0xFEFF))) // the byte order mark
	return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
}

// OlaInstallStatement is install-ola-index's statement: master, then the three scripts as Ola wrote
// them, in batches separated by GO (each of his files ends with GO).
func OlaInstallStatement() string {
	var b strings.Builder
	b.WriteString("/* kakapo: Ola Hallengren's SQL Server Maintenance Solution (https://ola.hallengren.com), version " + OlaVersion +
		", MIT licence: CommandLog, CommandExecute and IndexOptimize only, no jobs. */\nUSE master;\nGO\n")
	for _, name := range olaScripts {
		b.WriteString(olaScript(name) + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// BuildInstallOlaIndexFix builds install-ola-index's preview. ErrFixNotApplicable when IndexOptimize is
// there; a preview with only a warning (no changes, nothing to apply) when kakapo does not install.
func BuildInstallOlaIndexFix(f OlaIndexFacts) (FixPreview, Fix, error) {
	if f.IndexOptimize {
		return FixPreview{}, Fix{}, ErrFixNotApplicable
	}
	fix := suFix(FixInstallOlaIndex)
	fix.Current, fix.Target = "missing", "version "+OlaVersion
	p := FixPreview{EngineVersion: f.ProductVersion, Changes: []Drift{}, Warnings: []string{}}
	if why := OlaInstallBlock(f.ProductVersion, f.CommandExecute, f.Version); why != "" {
		p.Warnings = append(p.Warnings, why)
		return p, fix, nil
	}
	logChange := Drift{Setting: "Table master.dbo.CommandLog", Current: "missing", Target: "created"}
	if f.CommandLog {
		logChange.Current, logChange.Target = "exists", "kept as it is"
	}
	p.Changes = append(p.Changes, logChange,
		Drift{Setting: "Procedure master.dbo.CommandExecute", Current: "missing", Target: "version " + OlaVersion},
		Drift{Setting: "Procedure master.dbo.IndexOptimize", Current: "missing", Target: "version " + OlaVersion})
	p.Lead = "Installs Ola Hallengren's IndexOptimize in master, from the copy inside kakapo, so the maintenance plan can offer weekly index work."
	p.Statement = OlaInstallStatement()
	p.Rollback = "USE master;\nGO\nDROP PROCEDURE dbo.IndexOptimize;\nDROP PROCEDURE dbo.CommandExecute;"
	if !f.CommandLog {
		p.Rollback += "\nDROP TABLE dbo.CommandLog;"
	}
	p.Warnings = append(p.Warnings, "Index work stays off until you turn it on in the maintenance plan.")
	if f.MaintProc {
		p.Warnings = append(p.Warnings, "kakapo's maintenance procedure then updates statistics with IndexOptimize (only the statistics "+
			"with enough changed rows) instead of sp_updatestats.")
	}
	p.Warnings = append(p.Warnings, "The rollback removes them again: a job that calls IndexOptimize fails after it.")
	return p, fix, nil
}

// OlaInstallDescription is install-ola-index's change log line.
func OlaInstallDescription(server string) string {
	return "Ola Hallengren's IndexOptimize on " + server + ": missing -> version " + OlaVersion
}

// OlaInstallReadBack says what is in master after the install, and whether it is what was installed:
// both procedures at kakapo's version and the table.
func OlaInstallReadBack(f OlaIndexFacts) (string, bool) {
	if !f.IndexOptimize || !f.CommandExecute || !f.CommandLog {
		missing := []string{}
		for _, x := range []struct {
			ok   bool
			name string
		}{{f.CommandLog, "CommandLog"}, {f.CommandExecute, "CommandExecute"}, {f.IndexOptimize, "IndexOptimize"}} {
			if !x.ok {
				missing = append(missing, x.name)
			}
		}
		return "missing in master: " + strings.Join(missing, ", "), false
	}
	if f.Version != OlaVersion {
		return "IndexOptimize version " + orQ(f.Version) + ", not " + OlaVersion, false
	}
	return "version " + OlaVersion + " (CommandLog, CommandExecute, IndexOptimize)", true
}
