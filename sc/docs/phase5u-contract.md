# Phase 5u contract: kakapo installs Ola Hallengren's IndexOptimize

Agreed with Francois on 2026-11-11 as drafted. Built the same day: see Built.

The maintenance plan's **Index maintenance** row stays grey on most servers. It says "kakapo offers index
maintenance only with Ola Hallengren's IndexOptimize installed in master". To install it, someone has to
download Ola's script, change `@CreateJobs` to `'N'` and run it against master. That step is rarely taken.

This phase puts it in kakapo. The Index maintenance row gets an **Install** button: one fix that creates
Ola's `CommandLog` table and his `CommandExecute` and `IndexOptimize` procedures in master, from a copy
shipped inside kakapo. The row then offers weekly index work as it does today when Ola is there. Index work
stays optional and off by default.

SCOPE rules 1, 2, 4 and 5. Nothing changes until the user applies the fix.

## What it is not

- **Not the full Maintenance Solution.** `DatabaseBackup` and `DatabaseIntegrityCheck` are not installed.
  kakapo's maintenance procedure switches to them as soon as they exist (`maintproc_sql.go`, tasks 1, 4 and
  5), which would change how backups are named and where they go, as a side effect of an index fix.
- **No Agent jobs.** Ola's own jobs are not created (`@CreateJobs = 'N'` in his terms). kakapo's plan makes
  the jobs, as now.
- **Not an upgrade of someone else's install.** When `CommandExecute` or `IndexOptimize` is already in
  master, kakapo installs nothing (decision 3).
- **No new index judgement.** Thresholds, the window and "never offline" are 5q's and d143b90's.

## Decisions to confirm

1. **Only three objects, from Ola's separate scripts.** `CommandLog.sql`, `CommandExecute.sql` and
   `IndexOptimize.sql` from the same repository and version as `MaintenanceSolution.sql`. They are what
   IndexOptimize needs: it stops with "The stored procedure CommandExecute is missing" without the second,
   and kakapo's commands log to the first.
2. **A pinned copy inside kakapo.** Version `2026-09-12 15:02:38` (the `--// Version:` line in both
   procedures), downloaded 2026-11-11 from
   `https://raw.githubusercontent.com/olahallengren/sql-server-maintenance-solution/main/`. The files are
   embedded byte for byte (`go:embed`), with Ola's MIT `LICENSE` beside them and a `SOURCE.md` giving the
   URL, the version and each file's SHA-256:
   - `CommandLog.sql` 7a508fa7ed562ec5ee09d4f587f7f2a87f7a4eb3c57450602ee99cdeb4e18b31
   - `CommandExecute.sql` 190962fc9802ce8d8f5082a23119567d27f05d2bfc36e1bdf5ce79edea5d8e76
   - `IndexOptimize.sql` ae23d8bd3d866c8c83a6ce27d982b0573e4f96a30e744b3549fa63a9548abed6
   - `LICENSE` 11a9e9cd4b1552936dcae48b1a6b1da0d083767f8b1a813cdf180b38c76ed60d

   A test checks the hashes, so an edit to the copy fails the build. A newer version arrives only with a
   kakapo release.
3. **Install only into an empty slot.** The fix is offered when neither `CommandExecute` nor
   `IndexOptimize` is in master. `CommandLog` is created only when missing (Ola's script already says
   `IF NOT EXISTS`). When `CommandExecute` is there without `IndexOptimize` (someone installed Ola's
   backups only), kakapo does not install: other jobs may rely on that `CommandExecute`, and
   IndexOptimize may refuse it as too old ("needs to be updated"). The row then says which of Ola's
   procedures it found and their version, and that whoever installed them should add IndexOptimize from
   the same version.
4. **SQL Server 2017 or later.** Ola's current script names 2017 and later, and IndexOptimize uses
   `STRING_AGG` and other 2017 functions. On 2016 and earlier the row says so and offers no fix.
5. **Where it shows: the Index maintenance row only.** An **Install** button, the same as **Create** on the
   alerts row. The statistics row does not get one: `sp_updatestats` is fine there.

## 1. The read (no new query)

`MaintenancePlanFacts.IndexOptimize` and `MaintProcFacts.OlaIndex` already say whether IndexOptimize and
CommandExecute are in master. Added to the same read:

- `OlaCommandExecute bool`: CommandExecute alone (for decision 3);
- `OlaVersion string`: the 19 characters after `--// Version: ` in `OBJECT_DEFINITION` of whichever of
  CommandExecute and IndexOptimize exists, as Ola reads his own version;
- `ProductMajor` from `ProductVersion` (already read).

## 2. The fix `install-ola-index` (server; check MNT03, beside `maintenance-job`)

`BuildInstallOlaIndexFix` (pure, `domain/ola_fix.go`):

- **Not applicable** when IndexOptimize is there.
- **Blocked**, with the reason as a warning and no statement, when:
  - the major version is under 14 (2017): "Ola Hallengren's current IndexOptimize needs SQL Server 2017 or
    later; this server is {version}.";
  - CommandExecute is there without IndexOptimize: "Ola Hallengren's CommandExecute (version {v}) is
    already in master, without IndexOptimize. kakapo does not change someone else's install: add
    IndexOptimize.sql from the same version (https://ola.hallengren.com)."
- **Otherwise** the preview:
  - Changes: `CommandLog table: missing -> created` (or `exists -> kept as it is`),
    `CommandExecute: missing -> version 2026-09-12 15:02:38`, the same for IndexOptimize.
  - Statement: `USE master;` then the three scripts as Ola wrote them, in that order, split on `GO` as
    every multi-batch fix is, with one `/* kakapo: Ola Hallengren's SQL Server Maintenance Solution,
    version ..., MIT licence */` line in front. Each file's UTF-8 byte order mark is dropped (the embedded
    files keep theirs, so the hashes hold). The preview shows the whole text in the usual scroll box; it is
    long (about 200 KB). Copy gives all of it.
  - Warnings: "Index work stays off until you turn it on in the maintenance plan." and, when kakapo's
    maintenance procedure is installed, "kakapo's procedure then updates statistics with IndexOptimize
    (only statistics with enough changed rows) instead of sp_updatestats." That switch is already in the
    procedure (task 2); this phase only makes it visible.
  - Rollback: `DROP PROCEDURE dbo.IndexOptimize; DROP PROCEDURE dbo.CommandExecute;` and
    `DROP TABLE dbo.CommandLog` only when this fix created it, with the warning "A job that calls
    IndexOptimize fails after this."
- **Permission:** sysadmin, as the maintenance procedure's install. Without it, the existing refusal
  wording.

## 3. Apply, read-back, change row (`service/ola_fix.go`)

Apply runs the batches in master. The read-back says it worked when IndexOptimize and CommandExecute exist
and both report version `2026-09-12 15:02:38`, and CommandLog exists. The change row: "Ola Hallengren's
IndexOptimize on {server}: missing -> version 2026-09-12 15:02:38".

After a refresh, the Index maintenance row shows `optional` with the usual fix: the weekly index job, or the
procedure's "Weekly index work" option.

## 4. Web

No new component. The plan row already renders a fix button when `fixId` is set. Its label: **Install**.

## Sources

- Ola Hallengren, "SQL Server Index and Statistics Maintenance"
  (https://ola.hallengren.com/sql-server-index-and-statistics-maintenance.html): MaintenanceSolution.sql
  is for SQL Server 2017 to 2025 and "creates all the objects and jobs that you need"; the objects are also
  available as separate scripts on GitHub.
- The repository's `LICENSE`: MIT, "The above copyright notice and this permission notice shall be included
  in all copies or substantial portions of the Software."
- `IndexOptimize.sql` itself: the CommandExecute checks (missing, "needs to be updated").

## Tests

- The embedded files match the four SHA-256 hashes; each procedure's version line is the pinned one.
- Build: not applicable with IndexOptimize; blocked under 2017 and with CommandExecute alone (warnings as
  above, no statement); otherwise the changes, `USE master;` first, the three scripts in order, the
  procedure-statistics warning only when the procedure is installed, the rollback without `DROP TABLE` when
  CommandLog was there.
- The plan: the Index row carries `install-ola-index` when Ola is missing on 2017+, and its usual fix after.
- Live (`SQLMONITOR_TEST_HOST` + `SQLMONITOR_TEST_5U=1`, on a test instance only): install, read the
  versions back, run `IndexOptimize @Databases = <scratch database>, @Execute = 'N'`, roll back.

## Done when

On a 2017+ test instance without Ola, the Index maintenance row shows **Install**; applying it creates the
three objects at the pinned version; the row then offers weekly index work; the rollback removes them. On
a server with Ola's backups only, the row explains instead of installing.

## Not in this phase

- Updating a kakapo-installed copy when a newer kakapo ships a newer version.
- DatabaseBackup and DatabaseIntegrityCheck.
- The scheduled version of the Indexes page (5t's follow-up).

## Built (2026-11-11)

- `internal/domain/ola/`: Ola's `CommandLog.sql`, `CommandExecute.sql`, `IndexOptimize.sql` and `LICENSE`,
  byte for byte, with `SOURCE.md`. `sc/.gitattributes` marks them `-text` so git keeps their CRLF and byte
  order mark and the hashes hold.
- `domain/ola_fix.go`: `OlaInstallBlock`, `OlaInstallStatement` (12 batches: master, the table, then each
  procedure's stub and ALTER), `BuildInstallOlaIndexFix`, `OlaInstallReadBack`, `OlaInstallDescription`.
  `FixDefs` has `install-ola-index` (check MNT03, setting "IndexOptimize").
- The plan: `MaintenancePlanFacts` gains `OlaCommandExecute` and `OlaVersion` (read in
  `collect.MaintenancePlan`). The Index maintenance row carries `install-ola-index` when IndexOptimize is
  missing and nothing holds kakapo back, on the Agent path and on Express; otherwise its note says why.
  The composite (Set up the maintenance plan) never carries it.
- `collect/ola.go` (`ReadOlaIndex`) and `service/ola_fix.go`: the prepare, and the apply through
  `ExecFixBatches` with the read-back; a batch that fails after earlier ones logs what is there.
- Web: the row's button says **Install** and the dialog is titled "Install Ola Hallengren's IndexOptimize".
  The maintenance mock offers it on every database but WarehouseLive, until applied.
- Tests: `ola_fix_test.go` (hashes, version lines, statement, build, read-back, the plan row and the
  composite); `TestPhase5uLive` (`SQLMONITOR_TEST_HOST` + `SQLMONITOR_TEST_5U=1`) skips an instance that has
  any of Ola's objects.

Not proven live yet: no local instance accepted a connection on 2026-11-11. Run `TestPhase5uLive` on a test
instance without Ola before relying on it at a client.
