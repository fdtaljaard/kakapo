# Third-party notices

kakapo embeds no third-party SQL. Where a rule, a decode table or a list of names was taken closely from a
source, the Go file says so in a comment (`// Source: ...`) and the source is listed here with its licence.

## DarlingData (Erik Darling, Darling Data LLC)

Source: https://github.com/erikdarlingdata/DarlingData, licensed under the MIT License.
Copyright (c) 2021 Darling Data, LLC.

Fragments taken (each marked in the code): the wait-category and benign-wait lists, the Query Store noise
patterns, the trace-flag one-line texts, the readonly_reason decode, the database scoped configuration
defaults, the system_health component XPaths, the memory grant and spill thresholds. Files:
sp_PerfCheck.sql, sp_PressureDetector.sql, sp_QuickieStore.sql, sp_QuickieCache.sql, sp_QueryStoreCleanup.sql,
sp_HealthParser.sql, sp_HumanEventsBlockViewer.sql, TestBackupPerformance.sql, Alerts.sql, the Ola Stats Only Job (the IndexOptimize call of the statistics job), sp_IndexCleanup.sql.

MIT License

Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated
documentation files (the "Software"), to deal in the Software without restriction, including without
limitation the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the
Software, and to permit persons to whom the Software is furnished to do so, subject to the following
conditions:

The above copyright notice and this permission notice shall be included in all copies or substantial portions
of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED
TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL
THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF
CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER
DEALINGS IN THE SOFTWARE.
