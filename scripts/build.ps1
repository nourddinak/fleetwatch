# Builds the UI, the Linux agents (embedded into the server), then ops-server for this
# machine and for Linux (amd64 + arm64) into bin/.
# Usage: ./scripts/build.ps1 [-Version 1.0.0] [-SkipWeb] [-Release]
# -Release needs a real version and also writes bin/SHA256SUMS for the Linux servers.
param([string]$Version = "dev", [switch]$SkipWeb, [switch]$Release)
$ErrorActionPreference = "Stop"
if ($Release -and $Version -eq "dev") { Write-Error "-Release needs -Version x.y.z" }
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

if (-not $SkipWeb) {
    Push-Location web
    # npm prints warnings on stderr; Windows PowerShell would treat them as fatal under "Stop".
    $ErrorActionPreference = "Continue"
    if (-not (Test-Path node_modules)) { npm ci; if ($LASTEXITCODE) { exit 1 } }
    npm run build; if ($LASTEXITCODE) { exit 1 }
    $ErrorActionPreference = "Stop"
    Pop-Location
}

$ldflags = "-s -w -X main.version=$Version"
$env:CGO_ENABLED = "0"
function Build($os, $arch, $out, $pkg) {
    $env:GOOS = $os; $env:GOARCH = $arch
    go build -trimpath -ldflags $ldflags -o $out $pkg
    if ($LASTEXITCODE) { exit 1 }
    Write-Host "built $out"
}

# Agents first: ops-server embeds them from internal/server/agentbin/bin.
Build "linux" "amd64" "internal/server/agentbin/bin/ops-agent-linux-amd64" "./cmd/agent"
Build "linux" "arm64" "internal/server/agentbin/bin/ops-agent-linux-arm64" "./cmd/agent"

Build "" "" "bin/ops-server.exe" "./cmd/server"
Build "linux" "amd64" "bin/ops-server-linux-amd64" "./cmd/server"
Build "linux" "arm64" "bin/ops-server-linux-arm64" "./cmd/server"
Remove-Item Env:GOOS, Env:GOARCH

if ($Release) {
    $lines = Get-ChildItem bin/ops-server-linux-* | ForEach-Object { "$((Get-FileHash $_.FullName -Algorithm SHA256).Hash.ToLower())  $($_.Name)" }
    # LF line endings so `sha256sum --check SHA256SUMS` works on Linux.
    [IO.File]::WriteAllText("$root/bin/SHA256SUMS", ($lines -join "`n") + "`n")
    Write-Host "wrote bin/SHA256SUMS"
}
