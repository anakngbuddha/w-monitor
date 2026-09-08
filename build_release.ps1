param(
    [string]$ClientName = "",
    [string]$HubUrl = "",
    [string]$EnrollCode = "",
    [string]$ApiKey = ""
)
$ErrorActionPreference = "Stop"

# Compatibility parameters reject unsafe old invocations rather than silently
# packaging a different credential or creating enrollment codes during a build.
if ($EnrollCode -ne "" -or $ApiKey -ne "") {
    throw "Credentials cannot be embedded. Provision a protected machine config after installation."
}
if ($HubUrl -ne "" -or $ClientName -ne "") {
    throw "Release binaries are universal. Configure the destination and tenant at enrollment, not at build time."
}

$oldGoOS = $env:GOOS
$oldGoArch = $env:GOARCH
$oldIsolation = $env:WMONITOR_TEST_ISOLATION
try {
    $env:WMONITOR_TEST_ISOLATION = "1"
    & go version
    if ($LASTEXITCODE -ne 0) { throw "Go toolchain unavailable" }
    & go mod verify
    if ($LASTEXITCODE -ne 0) { throw "Module verification failed" }
    & go test ./agent -run '^TestNoProductionPathCallsInTests$' -count=1
    if ($LASTEXITCODE -ne 0) { throw "Test isolation preflight failed" }
    $unformatted = & gofmt -l .
    if ($LASTEXITCODE -ne 0 -or $unformatted) { throw "Formatting gate failed; run gofmt and review the diff before building" }
    & go vet ./...
    if ($LASTEXITCODE -ne 0) { throw "Vet failed" }
    & go test -race ./... -count=1 -timeout=10m
    if ($LASTEXITCODE -ne 0) { throw "Race-enabled tests failed" }
    $commit = (& git rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0 -or $commit -notmatch '^[0-9a-f]{40}$') { throw "Build requires a recorded source commit" }
    $dirty = & git status --porcelain
    if ($dirty) { throw "Build requires a clean checkout" }
    New-Item -ItemType Directory -Force -Path dist | Out-Null
    $env:GOARCH = "amd64"
    foreach ($target in @(@{os='windows'; name='wmonitor.exe'}, @{os='linux'; name='wmonitor_linux'})) {
        $env:GOOS = $target.os
        & go build -trimpath -ldflags "-s -w -X main.buildCommit=$commit" -o (Join-Path dist $target.name) .
        if ($LASTEXITCODE -ne 0) { throw "Platform build failed" }
    }
    Get-FileHash dist/wmonitor.exe, dist/wmonitor_linux -Algorithm SHA256 | Format-Table
    Write-Host "Universal candidate binaries built. Native service/ACL, live PostgreSQL, browser and security review gates remain mandatory."
} finally {
    $env:GOOS = $oldGoOS
    $env:GOARCH = $oldGoArch
    $env:WMONITOR_TEST_ISOLATION = $oldIsolation
}
