<#
.SYNOPSIS
Builds release binaries for Windows and Linux.
#>

$ErrorActionPreference = "Stop"

Write-Host "Building Sysmon Release Binaries..."

# Build Windows (Current OS)
Write-Host "Building Windows (sysmon.exe)..."
$env:GOOS = "windows"
$env:GOARCH = "amd64"
go build -ldflags "-s -w" -o sysmon.exe .
if ($LASTEXITCODE -ne 0) {
    Write-Error "Windows build failed."
    exit 1
}
Write-Host "  -> sysmon.exe built." -ForegroundColor Green

# Build Linux
Write-Host "Building Linux (sysmon_linux)..."
$env:GOOS = "linux"
$env:GOARCH = "amd64"
go build -ldflags "-s -w" -o sysmon_linux .
if ($LASTEXITCODE -ne 0) {
    Write-Error "Linux build failed."
    exit 1
}
Write-Host "  -> sysmon_linux built." -ForegroundColor Green

# Reset env vars
Remove-Item Env:\GOOS
Remove-Item Env:\GOARCH

Write-Host "`nBuild Complete!" -ForegroundColor Cyan
Write-Host "Run .\install.ps1 as Administrator to install on this machine."
Write-Host "Or copy sysmon_linux and install.sh to a Linux server."
