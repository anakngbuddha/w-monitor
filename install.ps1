<#
.SYNOPSIS
Installs Sysmon as a Windows Service.

.DESCRIPTION
This script must be run as Administrator. It will:
1. Check for administrative privileges
2. Stop the sysmon service if it already exists
3. Copy sysmon.exe to C:\Program Files\Sysmon
4. Add the installation directory to the system PATH
5. Install and start the Sysmon service

.EXAMPLE
.\install.ps1
#>

# 1. Ensure Admin privileges
$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) {
    Write-Warning "This script requires Administrator privileges to install the service."
    Write-Host "Attempting to restart script with elevated permissions..."
    Start-Process powershell -ArgumentList "-NoProfile -ExecutionPolicy Bypass -File `"$PSCommandPath`"" -Verb RunAs
    exit
}

$installDir = "$env:ProgramFiles\Sysmon"
$exeName = "sysmon.exe"
$sourceExe = Join-Path $PSScriptRoot $exeName
$targetExe = Join-Path $installDir $exeName

if (-not (Test-Path $sourceExe)) {
    Write-Error "Cannot find $sourceExe. Please run build_release.ps1 first to build the executable."
    exit 1
}

# 2. Stop and uninstall existing service (if running)
Write-Host "Checking for existing sysmon service..."
$existingService = Get-Service -Name "sysmon" -ErrorAction SilentlyContinue
if ($existingService) {
    Write-Host "Stopping sysmon service..."
    Stop-Service -Name "sysmon" -Force
    Start-Sleep -Seconds 2
    
    # We call the executable to uninstall itself if it's already there
    if (Test-Path $targetExe) {
        Write-Host "Uninstalling old service registration..."
        & $targetExe -uninstall
        Start-Sleep -Seconds 2
    }
}

# 3. Copy files
Write-Host "Creating installation directory: $installDir"
if (-not (Test-Path $installDir)) {
    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
}

Write-Host "Copying $exeName to $installDir"
Copy-Item -Path $sourceExe -Destination $targetExe -Force

# 4. Add to PATH (Machine level)
$envPath = [Environment]::GetEnvironmentVariable("PATH", [EnvironmentVariableTarget]::Machine)
if ($envPath -notmatch [regex]::Escape($installDir)) {
    Write-Host "Adding $installDir to System PATH..."
    $newPath = $envPath + (if ($envPath.EndsWith(";")) { "" } else { ";" }) + $installDir
    [Environment]::SetEnvironmentVariable("PATH", $newPath, [EnvironmentVariableTarget]::Machine)
    $env:PATH = $newPath # Update current session
}

# 5. Install and Start Service
Write-Host "Installing Sysmon service..."
& $targetExe -install

if ($LASTEXITCODE -ne 0) {
    Write-Error "Failed to install service."
    exit 1
}

Write-Host "Starting Sysmon service..."
& $targetExe -start

if ($LASTEXITCODE -ne 0) {
    Write-Error "Failed to start service."
    exit 1
}

Write-Host ""
Write-Host "Sysmon has been successfully installed and started!" -ForegroundColor Green
Write-Host "Dashboard is available at http://localhost:8080"
Write-Host "Logs are stored in the Windows Event Viewer."
Write-Host "Data is stored in %LOCALAPPDATA%\Sysmon\sysmon.db (usually C:\Windows\System32\config\systemprofile\AppData\Local\Sysmon for SYSTEM account)."
Write-Host ""
Write-Host "Press any key to exit..."
$Host.UI.RawUI.ReadKey("NoEcho,IncludeKeyDown") | Out-Null
