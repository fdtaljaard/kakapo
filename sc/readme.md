# kakapo

*You don't need to fly to keep a database running.* The product is **kakapo**; the code, the exe and the
data file keep the working name `sqlmonitor` so nothing registered or stored is orphaned by the rename.

A portable SQL Server monitor: one `sqlmonitor.exe`, one `sqlmonitor.duckdb` beside it, nothing installed on the
server. Scope and architecture are in [SCOPE.md](SCOPE.md).

## Run

Double-click `sqlmonitor.exe`. It serves the UI on `http://127.0.0.1:7331` and opens it as an app window in
Edge. Closing the window stops the app. Flags:

```
sqlmonitor.exe -port 7331 -data <folder> -no-browser
```

On first run the database is empty, so the app asks for a SQL Server. **Test connection** connects and shows the
version and edition. **Save** tests again and stores the server in DuckDB; a SQL login's password goes to
Windows Credential Manager, never into the file.

The server card then lists the user databases with their Query Store state (green: recommended settings; amber:
differs; red: off). **Apply recommended settings** shows the exact `ALTER DATABASE` it will run and its rollback,
runs it on confirmation, reads the options back, and writes a row to the `change` table in DuckDB. That is the only
write the app makes to a monitored server; everything else is a SELECT. The target settings and rules are in
`SCOPE.md` section 3a; the API shapes in `docs/phase1-contract.md`.

**Slow first start.** Antivirus scans the exe the first time a new build runs, which can take over a minute
before the window appears (measured: 72 s cold, under 3 s warm). Later starts are fast. If a machine scans
every start, add the folder to the antivirus exclusions. `sqlmonitor.log` beside the exe shows when the app
actually started; a second double-click while it is running just opens its window again.

## Build

Needs Go 1.25+, Node 22+, and a MinGW-w64 gcc on `PATH` (the DuckDB driver is CGO; the gcc is a build-time
dependency only, the exe is static).

```powershell
.\build.ps1
```

Frontend only, with live reload against a running backend (`sqlmonitor.exe -no-browser`):

```powershell
cd web; npm run dev
```

## Layout

```
cmd/sqlmonitor/   main: flags, DuckDB path, HTTP listener, Edge app window
internal/api/     HTTP JSON routes and the embedded single-page UI
internal/service/ use cases; the only entry point for api (and later MCP)
internal/collect/ SQL Server side: connection strings, connection test, later scripts and samplers
internal/store/   DuckDB: schema migrations, single writer, queries
internal/cred/    Windows Credential Manager for SQL login passwords
internal/domain/  model and validation, no I/O
web/              React + TypeScript + Tailwind + shadcn/ui; web/dist is embedded by web/embed.go
```

Every batch kakapo sends to a server starts with `/* kakapo */`, so its own reads never show up as costly
queries. Rules taken closely from Erik Darling's scripts are marked in the code and listed in
`THIRD_PARTY_NOTICES.md` (MIT).

Every finding ends in a button (SCOPE section 3 item 10): **Apply** runs a fix through preview, confirm, apply,
read-back and the change log, with inputs where there is a choice; **Investigate** opens the next read (a query's
page, the blocking chain, the live sample); **Copy** puts the finding as a hand-over block on the clipboard for
whoever owns it. Every preview also offers Copy script. Fixes register themselves in `internal/service/fixreg.go`
from their own files, so adding one never edits the dispatch.
