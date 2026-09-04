param(
    [string]$ClientName  = "",
    [string]$HubUrl      = "",
    # New enrollment-code workflow (Phase 1 implementation).
    # Pass an existing code, or leave blank to auto-request one from the Hub
    # using $env:WMONITOR_ADMIN_TOKEN.
    [string]$EnrollCode  = "",
    # Legacy parameter kept for backward-compat during the transition period.
    # Will be removed in the next major release — use -EnrollCode instead.
    [string]$ApiKey      = ""
)

$ErrorActionPreference = "Stop"

Write-Host "=========================================" -ForegroundColor Cyan
Write-Host "  W-Monitor Universal Release Builder"    -ForegroundColor Cyan
Write-Host "=========================================" -ForegroundColor Cyan

# ── Resolve enrollment code ──────────────────────────────────────────────────
# Priority: explicit -EnrollCode > auto-request from Hub > legacy -ApiKey
if ($EnrollCode -eq "" -and $HubUrl -ne "" -and $env:WMONITOR_ADMIN_TOKEN -ne "") {
    Write-Host "  Auto-requesting enrollment code from Hub..." -ForegroundColor Yellow
    try {
        $body = @{ client_name = if ($ClientName -ne "") { $ClientName } else { "Default" }
                   max_uses    = 25
                   ttl_hours   = 336 } | ConvertTo-Json
        $resp = Invoke-RestMethod `
            -Method  POST `
            -Uri     "$HubUrl/api/admin/enroll-codes" `
            -Headers @{ "X-API-Key" = $env:WMONITOR_ADMIN_TOKEN; "Content-Type" = "application/json" } `
            -Body    $body
        $EnrollCode = $resp.code
        Write-Host "  Enrollment Code : $EnrollCode" -ForegroundColor Green
        Write-Host "  Tenant ID       : $($resp.tenant_id)" -ForegroundColor DarkGray
        Write-Host "  Expires         : $([DateTimeOffset]::FromUnixTimeSeconds($resp.expires_at).LocalDateTime)" -ForegroundColor DarkGray
        Write-Host "  Max Uses        : $($resp.max_uses)" -ForegroundColor DarkGray
    } catch {
        Write-Warning "Could not auto-request enrollment code: $_"
        Write-Warning "Falling back to universal binary (no baked credentials)."
        $EnrollCode = ""
    }
}

# ── Build ldflags ────────────────────────────────────────────────────────────
$ldflags = "-s -w"
if ($HubUrl -ne "") {
    $ldflags += " -X main.defaultHubURL=$HubUrl"
    Write-Host "  Hub URL         : $HubUrl" -ForegroundColor Yellow
}
if ($EnrollCode -ne "") {
    $ldflags += " -X main.defaultEnrollCode=$EnrollCode"
    Write-Host "  Enroll Code     : $($EnrollCode.Substring(0, [Math]::Min(7, $EnrollCode.Length)))..." -ForegroundColor Green
}
if ($ClientName -ne "") {
    $ldflags += " -X main.defaultClientLabel=$ClientName"
    Write-Host "  Client Label    : $ClientName" -ForegroundColor Yellow
}

# Legacy path: baked API key (transition period only — emits a deprecation warning)
if ($ApiKey -ne "") {
    Write-Warning "DEPRECATED: -ApiKey bakes a plaintext secret into the binary. Use -EnrollCode instead."
    $ldflags += " -X main.defaultAPIKey=$ApiKey"
}

if ($HubUrl -eq "" -and $EnrollCode -eq "" -and $ApiKey -eq "") {
    Write-Host "  Mode            : Universal Generic Binaries (Zero Baked Credentials)" -ForegroundColor Green
}

Write-Host "-----------------------------------------"

# ── Output binary names ──────────────────────────────────────────────────────
$outDir  = "dist"
if ($ClientName -ne "") {
    $sanitized = $ClientName -replace '[^a-zA-Z0-9_\-]', '_'
    $outDir    = "dist\$sanitized"
}
if (-not (Test-Path $outDir)) {
    New-Item -ItemType Directory -Force -Path $outDir | Out-Null
}

$winExe   = if ($ClientName -ne "") { "$outDir\wmonitor_$($ClientName -replace '[^a-zA-Z0-9_\-]','_').exe" } else { "$outDir\wmonitor.exe" }
$linuxExe = if ($ClientName -ne "") { "$outDir\wmonitor_$($ClientName -replace '[^a-zA-Z0-9_\-]','_')_linux" } else { "$outDir\wmonitor_linux" }

# ── Build Windows Binary ─────────────────────────────────────────────────────
Write-Host "Building Windows ($winExe)..."
$env:GOOS   = "windows"
$env:GOARCH = "amd64"
go build -ldflags "$ldflags" -o $winExe .
if ($LASTEXITCODE -ne 0) {
    Write-Error "Windows build failed."
    exit 1
}
# Always copy to root for install.ps1 convenience
Copy-Item -Path $winExe -Destination "wmonitor.exe" -Force
Write-Host "  -> $winExe (also copied to .\wmonitor.exe)" -ForegroundColor Green

# ── Build Linux Binary ───────────────────────────────────────────────────────
Write-Host "Building Linux ($linuxExe)..."
$env:GOOS   = "linux"
$env:GOARCH = "amd64"
go build -ldflags "$ldflags" -o $linuxExe .
if ($LASTEXITCODE -ne 0) {
    Write-Error "Linux build failed."
    exit 1
}
Write-Host "  -> $linuxExe" -ForegroundColor Green

# ── Reset build env vars ─────────────────────────────────────────────────────
Remove-Item Env:\GOOS
Remove-Item Env:\GOARCH

# ── Build log (fingerprint only — NO plaintext secrets) ──────────────────────
$logFile = "$outDir\build_log.csv"
$csvHeader = "ClientName,KeyPrefix,HubUrl,WinBinary,LinuxBinary,BuiltAt"
if (-not (Test-Path $logFile)) {
    Set-Content -Path $logFile -Value $csvHeader
}
$label     = if ($ClientName -ne "") { $ClientName } else { "Generic" }
$keyPrefix = if ($EnrollCode -ne "") { $EnrollCode.Substring(0, [Math]::Min(7, $EnrollCode.Length)) + "..." } else { "(none)" }
$now       = (Get-Date -Format "yyyy-MM-dd HH:mm:ss")
Add-Content -Path $logFile -Value "`"$label`",`"$keyPrefix`",`"$HubUrl`",`"$(Split-Path $winExe -Leaf)`",`"$(Split-Path $linuxExe -Leaf)`",`"$now`""
Write-Host "Build record saved to $logFile (no plaintext secrets)" -ForegroundColor DarkGray

Write-Host "`n=========================================" -ForegroundColor Cyan
Write-Host "BUILD COMPLETE!" -ForegroundColor Green
Write-Host "=========================================" -ForegroundColor Cyan
Write-Host ""
Write-Host "Deployment workflow (new enrollment model):" -ForegroundColor White
Write-Host ""
Write-Host "  STEP 1 -- Hub: create an enrollment code for a new client" -ForegroundColor Yellow
Write-Host "    .\wmonitor.exe -hub -new-enroll-code 'AcmeCorp' -ttl 72h -max-uses 25"
Write-Host ""
Write-Host "  STEP 2 -- Build a client binary with the code baked in" -ForegroundColor Yellow
Write-Host "    .\build_release.ps1 -ClientName 'AcmeCorp' -HubUrl 'https://hub.example.com' -EnrollCode 'WM-XXXX-XXXX-XXXX'"
Write-Host ""
Write-Host "  STEP 3 -- Distribute and install on client servers" -ForegroundColor Yellow
Write-Host "    Windows: .\install.ps1 -Mode agent -HubUrl 'https://hub.example.com'"
Write-Host "    Linux:   sudo ./install.sh --mode agent --hub-url https://hub.example.com"
Write-Host ""
Write-Host "  NOTE: No API key is needed in the install command." -ForegroundColor Green
Write-Host "        The binary auto-enrolls on first run using the baked enrollment code."
Write-Host "========================================="
