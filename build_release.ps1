<#
.SYNOPSIS
Builds release binaries for Windows and Linux.
#>

$ErrorActionPreference = "Stop"

Write-Host "Building W-Monitor Release Binaries..."

# Build Windows (Current OS)
Write-Host "Building Windows (wmonitor.exe)..."
$env:GOOS = "windows"
$env:GOARCH = "amd64"
go build -ldflags "-s -w" -o wmonitor.exe .
if ($LASTEXITCODE -ne 0) {
    Write-Error "Windows build failed."
    exit 1
}
Write-Host "  -> wmonitor.exe built." -ForegroundColor Green

# Build Linux
Write-Host "Building Linux (wmonitor_linux)..."
$env:GOOS = "linux"
$env:GOARCH = "amd64"
go build -ldflags "-s -w" -o wmonitor_linux .
if ($LASTEXITCODE -ne 0) {
    Write-Error "Linux build failed."
    exit 1
}
Write-Host "  -> wmonitor_linux built." -ForegroundColor Green

# Reset env vars
Remove-Item Env:\GOOS
Remove-Item Env:\GOARCH

Write-Host "`nBuild Complete!" -ForegroundColor Cyan
Write-Host "Run .\install.ps1 as Administrator to install on this machine."
Write-Host "Or copy wmonitor_linux and install.sh to a Linux server."
