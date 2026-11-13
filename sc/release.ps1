# Publishes a kakapo release on GitHub: builds kakapo.exe stamped with the version, zips it with the readme and
# the third-party notices, tags the commit and creates the release with both files attached.
#
#   .\release.ps1 0.1.0 -DryRun   build and package into dist\ only: no tag, nothing published
#   .\release.ps1 0.1.0           the same, then tag v0.1.0, push the tag and publish the release
#
# Needs what build.ps1 needs, plus the GitHub CLI (gh) logged in with push rights to the repository.
param(
    [Parameter(Mandatory = $true)][string]$Version,
    [switch]$DryRun
)
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot

$Version = $Version.TrimStart('v')
if ($Version -notmatch '^\d+\.\d+\.\d+$') { throw "version '$Version' is not like 1.2.3" }
$tag = "v$Version"

function Git([string]$arguments) {
    $out = cmd /c "git $arguments 2>&1"
    if ($LASTEXITCODE -ne 0) { throw "git $arguments failed: $out" }
    return $out
}

Push-Location $root
try {
    # A release is the code everyone can see: main, committed, pushed, and a tag not used before.
    if (-not $DryRun) {
        if (-not (Get-Command gh -ErrorAction SilentlyContinue)) { throw 'the GitHub CLI (gh) is not installed' }
        cmd /c "gh auth status >nul 2>&1"; if ($LASTEXITCODE -ne 0) { throw 'gh is not logged in: run gh auth login' }
        $branch = Git 'rev-parse --abbrev-ref HEAD'
        if ($branch -ne 'main') { throw "on branch $branch, not main" }
        if (Git 'status --porcelain --untracked-files=no') { throw 'uncommitted changes: commit or stash them first' }
        Git 'fetch --quiet origin main --tags' | Out-Null
        if ((Git 'rev-parse HEAD') -ne (Git 'rev-parse origin/main')) { throw 'main is not the same as origin/main: push or pull first' }
        cmd /c "git rev-parse -q --verify refs/tags/$tag >nul 2>&1"
        if ($LASTEXITCODE -eq 0) { throw "tag $tag exists already" }
    }

    & "$root\build.ps1" -Version $Version
    if (-not $?) { throw 'build failed' }

    $dist = Join-Path $root 'dist'
    if (Test-Path $dist) { Remove-Item $dist -Recurse -Force }
    $stage = Join-Path $dist "kakapo-$tag"
    New-Item -ItemType Directory -Path $stage | Out-Null
    Copy-Item "$root\kakapo.exe", "$root\readme.md", "$root\THIRD_PARTY_NOTICES.md" $stage
    $exe = Join-Path $dist 'kakapo.exe'
    $zip = Join-Path $dist "kakapo-$tag-windows-x64.zip"
    Copy-Item "$root\kakapo.exe" $exe
    Compress-Archive -Path "$stage\*" -DestinationPath $zip
    Remove-Item $stage -Recurse -Force

    $sums = foreach ($f in $exe, $zip) { '{0}  {1}' -f (Get-FileHash $f -Algorithm SHA256).Hash.ToLower(), (Split-Path $f -Leaf) }
    $notes = Join-Path $dist 'notes.md'
    @(
        "kakapo $tag for Windows x64. Download ``kakapo.exe`` and run it, or the zip, which also has the readme and the third-party notices."
        ''
        'The first start can take a minute while antivirus scans the new exe. Windows may warn that the exe is not signed: More info, then Run anyway.'
        ''
        'SHA-256:'
        '```'
        $sums
        '```'
    ) | Set-Content -Path $notes -Encoding utf8

    Write-Host "packaged $tag in $dist"
    $sums | ForEach-Object { Write-Host "  $_" }
    if ($DryRun) { Write-Host 'dry run: no tag, nothing published'; return }

    git tag -a $tag -m "kakapo $tag"
    if ($LASTEXITCODE -ne 0) { throw "git tag $tag failed" }
    git push --quiet origin $tag
    if ($LASTEXITCODE -ne 0) { throw "git push of $tag failed; the tag is only local: git tag -d $tag to start again" }
    gh release create $tag $exe $zip --title "kakapo $tag" --notes-file $notes --generate-notes --verify-tag
    if ($LASTEXITCODE -ne 0) { throw "gh release create failed; the tag $tag is pushed, so rerun only the gh step" }
    Write-Host "published $tag"
} finally { Pop-Location }
