# Builds kakapo.exe: frontend first, then the Go binary with the frontend embedded.
# Needs: Go, Node, and a MinGW gcc (MSYS2 ucrt64) for the DuckDB driver.
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
if (Test-Path 'C:\msys64\ucrt64\bin') { $env:PATH = "C:\msys64\ucrt64\bin;$env:PATH" }
$winlibs = Get-ChildItem "$env:LOCALAPPDATA\Microsoft\WinGet\Packages" -Directory -Filter 'BrechtSanders.WinLibs*' -ErrorAction SilentlyContinue |
    ForEach-Object { Join-Path $_.FullName 'mingw64\bin' } | Where-Object { Test-Path $_ } | Select-Object -First 1
if ($winlibs) { $env:PATH = "$winlibs;$env:PATH" }
if (Test-Path 'C:\Program Files\Go\bin') { $env:PATH = "C:\Program Files\Go\bin;$env:PATH" }
$env:CGO_ENABLED = '1'

Push-Location "$root\web"
try {
    # cmd /c keeps npm's stderr warnings from being turned into PowerShell errors
    if (-not (Test-Path node_modules)) { cmd /c "npm ci" }
    cmd /c "npm run build"
    if ($LASTEXITCODE -ne 0) { throw "frontend build failed" }
} finally { Pop-Location }

Push-Location $root
try {
    # -s -w strips symbols and DWARF: a smaller exe is a shorter antivirus scan at every start.
    # The version stamp (build time, plus the git commit when there is one) shows on Home and in the log.
    $stamp = Get-Date -Format 'yyyy-MM-dd.HHmm'
    $commit = (cmd /c "git rev-parse --short HEAD 2>nul"); if ($LASTEXITCODE -eq 0 -and $commit) { $stamp = "$stamp.$commit" }
    go build -trimpath -ldflags "-s -w -X main.version=$stamp" -o kakapo.exe ./cmd/sqlmonitor
    if ($LASTEXITCODE -ne 0) { throw "go build failed" }
    Write-Host "built $root\kakapo.exe"
} finally { Pop-Location }
