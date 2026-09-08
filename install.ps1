param(
    [Parameter(Mandatory=$true)][string]$ConfigPath,
    [Parameter(Mandatory=$true)][string]$BinaryPath,
    [Parameter(Mandatory=$true)][ValidatePattern('^[0-9a-fA-F]{64}$')][string]$ExpectedSHA256
)
$ErrorActionPreference = 'Stop'
$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) { throw 'Open an authorized Administrator shell. Automatic elevation with secret arguments is disabled.' }
foreach ($path in @($ConfigPath, $BinaryPath)) {
    $item = Get-Item -LiteralPath $path
    if ($item.PSIsContainer -or ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Regular, non-reparse source files are required.' }
}
if ((Get-FileHash -LiteralPath $BinaryPath -Algorithm SHA256).Hash -ne $ExpectedSHA256) { throw 'Binary checksum mismatch. Nothing installed.' }
$programData = $env:PROGRAMDATA
if (-not $programData) { $programData = 'C:\ProgramData' }
$configDir = Join-Path $programData 'wmonitor'
$configFile = Join-Path $configDir 'config.env'
$installDir = Join-Path $env:ProgramFiles 'W-Monitor'
$target = Join-Path $installDir 'wmonitor.exe'
if ((Get-Service -Name wmonitor -ErrorAction SilentlyContinue) -or (Test-Path -LiteralPath $target) -or (Test-Path -LiteralPath $configFile)) {
    throw 'Existing installation found. Use a reviewed maintenance-window upgrade. No existing service was stopped or replaced.'
}
function Protect-Directory([string]$Path) {
    if (Test-Path -LiteralPath $Path) {
        if ((Get-Item -LiteralPath $Path).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Reparse installation directory refused.' }
    } else { New-Item -ItemType Directory -Path $Path | Out-Null }
    $acl = New-Object System.Security.AccessControl.DirectorySecurity
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($sid in @('S-1-5-18', 'S-1-5-32-544')) {
        $identity = New-Object System.Security.Principal.SecurityIdentifier($sid)
        $rule = New-Object System.Security.AccessControl.FileSystemAccessRule($identity, 'FullControl', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
        $acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $Path -AclObject $acl
}
Protect-Directory $configDir
Protect-Directory $installDir
Protect-Directory (Join-Path $configDir 'data')
# Create secrets only inside a directory already restricted to SYSTEM/Admins.
[IO.File]::WriteAllBytes($configFile, [IO.File]::ReadAllBytes((Resolve-Path -LiteralPath $ConfigPath)))
$fileAcl = New-Object System.Security.AccessControl.FileSecurity
$fileAcl.SetAccessRuleProtection($true, $false)
foreach ($sid in @('S-1-5-18', 'S-1-5-32-544')) {
    $identity = New-Object System.Security.Principal.SecurityIdentifier($sid)
    $fileAcl.AddAccessRule((New-Object System.Security.AccessControl.FileSystemAccessRule($identity, 'FullControl', 'Allow')))
}
Set-Acl -LiteralPath $configFile -AclObject $fileAcl
Copy-Item -LiteralPath $BinaryPath -Destination $target
& $target -config $configFile -print-config
if ($LASTEXITCODE -ne 0) { throw 'Configuration validation failed; service was not installed.' }
& $target -config $configFile -install
if ($LASTEXITCODE -ne 0) { throw 'Service installation failed.' }
& $target -start
if ($LASTEXITCODE -ne 0) { throw 'Service start failed.' }
Write-Host 'Candidate service installed. Native reboot, standard-user ACL, stop and uninstall gates still require verification.'
Write-Host 'No secret was embedded in the binary or placed in service arguments. A checksum is not a publisher signature.'
