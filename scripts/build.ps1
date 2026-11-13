# Build both executables for Windows and Linux. Same artifact names as build.sh.
# Usage: powershell -File scripts/build.ps1 [-Version 0.1.0] [-OutputDir dist]
param(
    [string]$Version = "",
    [string]$OutputDir = "dist"
)

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

if (-not $Version) {
    $line = Select-String -Path "internal/config/config.go" -Pattern '^var Version = "([^"]+)"'
    if (-not $line) { throw "cannot read Version from internal/config/config.go" }
    $Version = $line.Matches[0].Groups[1].Value
}

New-Item -ItemType Directory -Force -Path $OutputDir | Out-Null
$ldflags = "-s -w -X kcptun-proxy/internal/config.Version=$Version"

$targets = @(
    @{ OS = "windows"; Arch = "amd64"; Ext = ".exe" },
    @{ OS = "linux";   Arch = "amd64"; Ext = ""    }
)
$roles = @("server", "client")

foreach ($t in $targets) {
    foreach ($role in $roles) {
        $name = "kcptun-$role-$($t.OS)-$($t.Arch)$($t.Ext)"
        $out = Join-Path $OutputDir $name
        Write-Host "build -> $out"
        $env:CGO_ENABLED = "0"
        $env:GOOS = $t.OS
        $env:GOARCH = $t.Arch
        & go build -trimpath -ldflags $ldflags -o $out "./cmd/kcptun-$role"
        if ($LASTEXITCODE -ne 0) { throw "go build failed for $name" }
    }
}
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue

Write-Host "built $Version (4 artifacts):"
Get-ChildItem -Path $OutputDir -Filter "kcptun-*" | Select-Object -ExpandProperty Name
