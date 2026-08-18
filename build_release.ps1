param(
    [string]$ClientName = "",
    [string]$HubUrl = "https://wmonitor-hub.onrender.com",
    [string]$ApiKey = ""
)

$ErrorActionPreference = "Stop"

# Auto-generate a secure 32-byte API key if not provided
if ($ApiKey -eq "") {
    $bytes = New-Object byte[] 32
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    $rng.GetBytes($bytes)
    $ApiKey = [Convert]::ToBase64String($bytes)
    Write-Host "Auto-generated secure API key." -ForegroundColor Cyan
}

Write-Host "=========================================" -ForegroundColor Cyan
Write-Host "  W-Monitor Release Builder (Multi-Tenant)" -ForegroundColor Cyan
Write-Host "=========================================" -ForegroundColor Cyan
if ($ClientName -ne "") {
    Write-Host "  Client Name   : $ClientName" -ForegroundColor Yellow
}
Write-Host "  Hub URL       : $HubUrl" -ForegroundColor Yellow
Write-Host "  API Key       : $ApiKey" -ForegroundColor Green
Write-Host "-----------------------------------------"

$ldflags = "-s -w"
if ($HubUrl -ne "") {
    $ldflags += " -X main.defaultHubURL=$HubUrl"
}
if ($ApiKey -ne "") {
    $ldflags += " -X main.defaultAPIKey=$ApiKey"
}

# Determine output binary names
$winExe = "wmonitor.exe"
$linuxExe = "wmonitor_linux"
if ($ClientName -ne "") {
    $sanitized = $ClientName -replace '[^a-zA-Z0-9_\-]', '_'
    $winExe = "wmonitor_${sanitized}.exe"
    $linuxExe = "wmonitor_${sanitized}_linux"
}

# Build Windows Binary
Write-Host "Building Windows ($winExe)..."
$env:GOOS = "windows"
$env:GOARCH = "amd64"
go build -ldflags "$ldflags" -o $winExe .
if ($LASTEXITCODE -ne 0) {
    Write-Error "Windows build failed."
    exit 1
}
Write-Host "  -> $winExe built successfully." -ForegroundColor Green

# Also update root wmonitor.exe if building with a client name
if ($winExe -ne "wmonitor.exe") {
    Copy-Item -Path $winExe -Destination "wmonitor.exe" -Force
}

# Build Linux Binary
Write-Host "Building Linux ($linuxExe)..."
$env:GOOS = "linux"
$env:GOARCH = "amd64"
go build -ldflags "$ldflags" -o $linuxExe .
if ($LASTEXITCODE -ne 0) {
    Write-Error "Linux build failed."
    exit 1
}
Write-Host "  -> $linuxExe built successfully." -ForegroundColor Green

# Reset env vars
Remove-Item Env:\GOOS
Remove-Item Env:\GOARCH

# Save to clients_registry.csv (Key Recovery Layer 2)
$registryFile = "clients_registry.csv"
$csvHeader = "ClientName,APIKey,HubUrl,OutputBinary,CreatedAt"
if (-not (Test-Path $registryFile)) {
    Set-Content -Path $registryFile -Value $csvHeader
}
$clientLabel = if ($ClientName -ne "") { $ClientName } else { "Default" }
$now = (Get-Date).ToString("yyyy-MM-dd HH:mm:ss")
$csvLine = "`"$clientLabel`",`"$ApiKey`",`"$HubUrl`",`"$winExe`",`"$now`""
Add-Content -Path $registryFile -Value $csvLine
Write-Host "Saved build record to $registryFile (Key Recovery Registry)" -ForegroundColor DarkGray

Write-Host "`n=========================================" -ForegroundColor Cyan
Write-Host "BUILD COMPLETE!" -ForegroundColor Green
Write-Host "=========================================" -ForegroundColor Cyan
Write-Host "1. Give $winExe to client (they just double-click on all servers)."
Write-Host "2. Give client their API Key for the dashboard:"
Write-Host "   API KEY : $ApiKey" -ForegroundColor Yellow
Write-Host "   HUB URL : $HubUrl" -ForegroundColor Yellow
Write-Host "3. Lost key recovery: client can run `"$winExe -show-key`" on any server."
Write-Host "=========================================`n"
