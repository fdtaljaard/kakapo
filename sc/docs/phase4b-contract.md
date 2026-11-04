# Phase 4b contract: the backup schedule with editable settings

Builds on `docs/phase3g-contract.md` (revision 2). The preview of `backup-schedule` becomes the whole decision:
when, how often, how long to keep, compressed or not. SCOPE section 3 item 10 (Apply with inputs where there is
a choice).

## Inputs

`FixInput` gains `Options []string` (`json:"options,omitempty"`) for `Kind: "select"`. The fix's inputs, in this
order:

| Name | Kind | Default | Rule |
|---|---|---|---|
| `folder` | text | as today | as today |
| `fullTime` | text | `22:00` | 24-hour `HH:MM`; invalid -> warning "The full backup time must be HH:MM, for example 22:00." and no Changes (like a bad folder). Help: "Daily, server time. Pick a quiet hour after the day's work and before any overnight job that reads the database." |
| `logMinutes` | select, options `5`, `10`, `15`, `30`, `60` | `15` | Only when the log job would be created. Help: "How much work you could lose. 15 minutes is the usual answer; 5 for a busy site." |
| `keepDays` | text | `7` | Integer 1 to 60; invalid -> warning "Keep between 1 and 60 days." and no Changes. Help: "Files older than this are deleted by the job. Copies kept elsewhere are not affected." |
| `compress` | bool | `CompressionOK` | Absent when the edition cannot compress. Help: "About half the size and faster to write; costs CPU while the backup runs." |
| `runNow` | bool | `true` | as today |

The UI renders a `select` for `Kind: "select"` (the dialog already renders text and bool); a select change
rebuilds the preview at once, like a checkbox.

## What changes with the values

- Full job schedule: `active_start_time = HHMM00` from `fullTime`; the schedule name reads "daily {HH:MM}".
- Log job: `freq_subday_interval = logMinutes`; the schedule name reads "every {n} minutes".
- Cleanup: `DATEADD(DAY, -{keepDays}, GETDATE())` in both jobs; Ola: `@CleanupTime = {keepDays * 24}`.
- Compression: `COMPRESSION` only when `compress` is true (and the edition allows); Ola: `@Compress = 'Y'`
  or `'N'`.
- The preview Changes rows show the values ("daily 22:00 to …", "every 15 minutes to …", "Keep: 7 days of
  files", "Compressed: yes"). The change-log description names the time, the interval and the days.
- The job description line gains the settings: "Created by kakapo on {date} for {db}: daily {HH:MM},
  log every {n} min, keep {d} days".

## The remove fix

The read of kakapo's jobs (`KakapoJobs`) gains each job's schedule (`sysschedules.active_start_time`,
`freq_subday_interval`) so the remove fix's rollback rebuilds the creation batch with the same time and
interval; keep days and compression are read from the step command as the folder is today. The Changes rows of
the remove preview say what each job does ("Full backup job | daily 22:00, keep 7 days | deleted").

## Tests

Domain: the batch with `fullTime 01:30`, `logMinutes 5`, `keepDays 14`, `compress false`; the three invalid
inputs each giving the warning and no Changes; the remove fix's rollback round trip with non-default values;
the inputs list in order with the right defaults, and `logMinutes` absent in SIMPLE recovery. Service: a bad
`fullTime` through `PreviewFixWithOptions` returns no Changes. The web build.

## Done when

`go test ./...` and `npm run build` pass; live on a throwaway database the preview shows the five inputs, a
changed time and interval appear in the Changes rows and in the created schedules (`sysschedules`), and the
remove preview names them.
