param(
    [string]$ClientName = "",
    [string]$HubUrl = "",
    [string]$ApiKey = ""
)

$ErrorActionPreference = "Stop"

Write-Host "=========================================" -ForegroundColor Cyan
Write-Host "  W-Monitor Universal Release Builder" -ForegroundColor Cyan
Write-Host "=========================================" -ForegroundColor Cyan

$ldflags = "-s -w"
if ($HubUrl -ne "") {
    $ldflags += " -X main.defaultHubURL=$HubUrl"
    Write-Host "  Default Hub URL : $HubUrl" -ForegroundColor Yellow
}
if ($ApiKey -ne "") {
    $ldflags += " -X main.defaultAPIKey=$ApiKey"
    Write-Host "  Default API Key : $ApiKey" -ForegroundColor Green
}

if ($HubUrl -eq "" -and $ApiKey -eq "") {
    Write-Host "  Mode            : Universal Generic Binaries (Zero Baked Credentials)" -ForegroundColor Green
}

Write-Host "-----------------------------------------"

# Output binary names
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

# Also copy to root wmonitor.exe if a custom client name was specified
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

# Reset build env vars
Remove-Item Env:\GOOS
Remove-Item Env:\GOARCH

# If building for a specific client with baked key, record it
if ($ApiKey -ne "") {
    $registryFile = "clients_registry.csv"
    $csvHeader = "ClientName,APIKey,HubUrl,OutputBinary,CreatedAt"
    if (-not (Test-Path $registryFile)) {
        Set-Content -Path $registryFile -Value $csvHeader
    }
    $clientLabel = if ($ClientName -ne "") { $ClientName } else { "Default" }
    $now = (Get-Date).ToString("yyyy-MM-dd HH:mm:ss")
    $csvLine = "`"$clientLabel`",`"$ApiKey`",`"$HubUrl`",`"$winExe`",`"$now`""
    Add-Content -Path $registryFile -Value $csvLine
    Write-Host "Saved build record to $registryFile" -ForegroundColor DarkGray
}

Write-Host "`n=========================================" -ForegroundColor Cyan
Write-Host "BUILD COMPLETE!" -ForegroundColor Green
Write-Host "=========================================" -ForegroundColor Cyan
Write-Host "Standard Multi-Server Deployment Workflow:"
Write-Host "1. Distribute universal binary ($winExe / $linuxExe) to all client servers."
Write-Host "2. On the Hub, generate an Organization API Key for the client:"
Write-Host "   .\wmonitor.exe -db postgres -add-client `"AcmeCorp`"" -ForegroundColor Yellow
Write-Host "3. Install on client servers using the generated Organization API Key:"
Write-Host "   Windows: .\install.ps1 -Mode agent -HubUrl <HubUrl> -ApiKey <OrgApiKey>" -ForegroundColor Yellow
Write-Host "   Linux:   sudo ./install.sh --mode agent --hub-url <HubUrl> --api-key <OrgApiKey>" -ForegroundColor Yellow
Write-Host "=========================================`n"
