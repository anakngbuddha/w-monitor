<#
.SYNOPSIS
Installs Sysmon for the current user without requiring Administrator privileges.

.DESCRIPTION
This script will:
1. Stop any currently running sysmon process for the user
2. Copy sysmon.exe to %LOCALAPPDATA%\Sysmon
3. Add the installation directory to the user's PATH
4. Create a hidden startup script in the user's Startup folder
5. Start Sysmon immediately in the background

.EXAMPLE
.\install_user.ps1
#>

$installDir = "$env:LOCALAPPDATA\Sysmon"
$exeName = "sysmon.exe"
$sourceExe = Join-Path $PSScriptRoot $exeName
$targetExe = Join-Path $installDir $exeName

if (-not (Test-Path $sourceExe)) {
    Write-Error "Cannot find $sourceExe. Please run build_release.ps1 first to build the executable."
    exit 1
}

# 1. Stop existing process if running
$existingProcess = Get-Process -Name "sysmon" -ErrorAction SilentlyContinue
if ($existingProcess) {
    Write-Host "Stopping existing sysmon process..."
    Stop-Process -Name "sysmon" -Force
    Start-Sleep -Seconds 2
}

# 2. Copy files
Write-Host "Creating installation directory: $installDir"
if (-not (Test-Path $installDir)) {
    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
}

Write-Host "Copying $exeName to $installDir"
Copy-Item -Path $sourceExe -Destination $targetExe -Force

# 3. Add to PATH (User level)
$envPath = [Environment]::GetEnvironmentVariable("PATH", [EnvironmentVariableTarget]::User)
if (-not $envPath) { $envPath = "" }
if ($envPath -notmatch [regex]::Escape($installDir)) {
    Write-Host "Adding $installDir to User PATH..."
    $separator = if ($envPath -and -not $envPath.EndsWith(";")) { ";" } else { "" }
    $newPath = $envPath + $separator + $installDir
    [Environment]::SetEnvironmentVariable("PATH", $newPath, [EnvironmentVariableTarget]::User)
    $env:PATH = $env:PATH + $separator + $installDir # Update current session
}

# 4. Create Startup Script (VBS to run hidden without console window)
$startupFolder = [Environment]::GetFolderPath("Startup")
$vbsPath = Join-Path $startupFolder "Sysmon.vbs"

Write-Host "Creating background startup script at $vbsPath"
$vbsContent = @"
Set WshShell = CreateObject("WScript.Shell")
WshShell.Run """$targetExe""", 0, False
Set WshShell = Nothing
"@
Set-Content -Path $vbsPath -Value $vbsContent

# 5. Start Sysmon now
Write-Host "Starting Sysmon in the background..."
& wscript.exe $vbsPath

Write-Host ""
Write-Host "Sysmon has been successfully installed for the current user!" -ForegroundColor Green
Write-Host "It is running in the background and will start automatically when you log in."
Write-Host "Dashboard is available at http://localhost:8080"
Write-Host "Data is stored in $installDir\sysmon.db"
Write-Host ""
Write-Host "Press any key to exit..."
$Host.UI.RawUI.ReadKey("NoEcho,IncludeKeyDown") | Out-Null
