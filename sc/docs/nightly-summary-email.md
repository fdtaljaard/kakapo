# Requirement: the nightly summary email

Noted 2026-11-10, for planning. Nothing is designed or built yet. Phases and contracts come later, each one
small and proving one thing end to end.

## The requirement, in one line

A nightly scheduled job, part of the maintenance plan, runs after all the night's maintenance. It emails a
short summary: what ran and whether it succeeded, then how yesterday's performance compared with the short-term
usual. The reader should not have to log on to the server or open kakapo to get a recap of yesterday.

## What the email answers

1. **Did last night's maintenance run, and did it work?** One line per job of the maintenance plan (phase 5b):
   - full and log backups, integrity check, statistics, index work when on, history cleanup and archives;
   - started, duration, succeeded or failed;
   - a failed job gives its error message's first line;
   - a job that should have run and did not is listed, so a missing job is not silent.
2. **Was yesterday better or worse than usual?** The same rules as the Performance card's "better or worse"
   (phase 5k):
   - business hours;
   - sums, not averages;
   - work asked, CPU per query and waiting;
   - the verdict and its one sentence.

   The comparison is against the short-term usual. **To decide:** 7 days only, as 5k does today, or 7, 14 and
   30 days side by side.
3. **What changed, and did it help?** (to investigate; it may be left out) Changes made yesterday or recently,
   and their effect: the change log (5f) and "did my change help" (5h). The requirement may settle on
   performance alone rather than per-change impact.

The email answers first and keeps detail out, as the screens do (SCOPE 1a): a subject line that carries the
verdict, for example "OK: maintenance ran, performance as usual", then the short body. Nothing in it needs
kakapo to be open.

## What we already have to build on

- **The maintenance plan (5b):** the jobs, their kakapo names and categories, and their times:
  - check, Sunday 01:00;
  - statistics, other nights 01:00;
  - archives, 02:30;
  - index work, Sunday 03:00;
  - cleanup, Sunday 05:00;
  - full backup, 22:00.

  The summary job would run after the last of these, for example at 06:00, before the working day.
- **The operator-alerts fix (4d):** it creates the operator with an email and already checks whether Database
  Mail is configured. The summary needs the same check, and the same warning when no mail can leave the server.
- **Job history in msdb:** what ran and whether it worked. The `/* kakapo */` marker and the job names show
  which jobs are ours.
- **Query Store hourly data:** the source of the 5k performance verdict.

## The design question to settle first

kakapo is a desktop exe that runs only while someone has it open, so it is not running at night. The email
therefore has to be produced **on the SQL Server side**: an Agent job, its T-SQL, and Database Mail.

Consequences:

- **Two copies of one rule.** The performance verdict now lives in Go (`domain`). For the email it has to be
  rewritten in T-SQL, in a procedure or job step the fix installs, the way the XE session is installed (5e).
  Somehow the Go rule and the SQL rule must stay the same: shared test cases, or the same fixture checked both
  ways.
- **Changes the server cannot see.** kakapo's own change log is in its DuckDB file, not on the server. The
  email can list only what the server itself shows: the default trace or the 5f harvest's sources, and job
  history.
- **Install and remove like any helper.** The job is installed and removed through the fix pattern: preview,
  confirm, apply, read back, change row, and a remove fix. It appears on the maintenance plan row and under
  Installed helpers.
- **Recipients.** The operator's email by default, or a list entered in the fix.

The alternative is for kakapo to run as a service, or as a scheduled task with `-no-browser`, and send the mail
itself. That is a larger change to what kakapo is (one portable exe), so it is noted only to set aside.

## Open questions for the plan

- The comparison windows: 7 days only, or 7, 14 and 30.
- Per database or per server: one email per server, with one section per monitored database?
- Send on a clean night too, or only when something failed or got worse? A daily "all OK" proves the job ran.
- Plain text or HTML, for Database Mail's `@body_format`.
- Changes and their impact: in the first version, or later?
- Express edition has no Agent. What then: the script to schedule with Windows Task Scheduler, as the backup
  schedule does?

## A first cut at the phases (to agree)

1. **Maintenance results only.** An installed job runs after the plan and emails last night's job outcomes
   through Database Mail. The fix installs it, and a remove fix takes it away.
2. **Yesterday's performance.** The 5k verdict, computed in T-SQL from Query Store, with tests that hold it to
   the Go rule.
3. **Longer windows and changes.** 14 and 30 days, and changes with their effect, if wanted after 1 and 2 are
   in use.
