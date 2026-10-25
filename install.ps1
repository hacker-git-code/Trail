# Install or update trail on Windows. Run this in PowerShell:
#
#   irm https://raw.githubusercontent.com/hacker-git-code/trail/main/install.ps1 | iex
#
# It downloads the latest release, checks that it runs, and puts it on your PATH.
# If trail is already installed, it updates that copy in place. Your history in
# ~/.trail is never touched.

$ErrorActionPreference = 'Stop'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$url = "https://github.com/hacker-git-code/trail/releases/latest/download/trail-windows-$arch.exe"

$existing = Get-Command trail.exe -ErrorAction SilentlyContinue
if ($existing) {
    $exe = $existing.Source
    $dir = Split-Path $exe
} else {
    $dir = Join-Path $env:LOCALAPPDATA 'trail'
    $exe = Join-Path $dir 'trail.exe'
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
}

Write-Host "Downloading trail..."
$tmp = Join-Path $dir 'trail.download.exe'
Invoke-WebRequest -Uri $url -OutFile $tmp -UseBasicParsing

$ver = & $tmp version 2>$null
if ($LASTEXITCODE -ne 0 -or -not $ver) {
    Remove-Item -Force $tmp -ErrorAction SilentlyContinue
    throw "The downloaded file didn't run, so nothing was changed."
}

# A running trail.exe (like a background watcher) can't be overwritten, but it can be renamed.
$old = Join-Path $dir 'trail.old.exe'
Remove-Item -Force $old -ErrorAction SilentlyContinue
if (Test-Path $exe) { Rename-Item -Force $exe 'trail.old.exe' }
Rename-Item -Force $tmp 'trail.exe'

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not $userPath) { $userPath = '' }
if (($userPath -split ';') -notcontains $dir) {
    [Environment]::SetEnvironmentVariable('Path', ($userPath.TrimEnd(';') + ';' + $dir).TrimStart(';'), 'User')
    $env:Path += ";$dir"
}

Write-Host ""
Write-Host "Installed $ver at $exe"
Write-Host ""
if ($existing) {
    Write-Host "Updated in place. Your history is untouched."
} else {
    Write-Host "Get started (open a new terminal first):"
    Write-Host "  trail watch add ~/Documents ~/Desktop   # bring back anything deleted, from any app"
    Write-Host "  trail watch autostart on                # keep watching in the background"
    Write-Host "  trail install claude                    # Claude Code: who and why for every change"
}
