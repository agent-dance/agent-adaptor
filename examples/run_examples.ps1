$ErrorActionPreference = "Stop"

if (-not $env:SystemRoot) {
    $env:SystemRoot = "C:\Windows"
}
if (-not $env:windir) {
    $env:windir = $env:SystemRoot
}
if (-not $env:ComSpec) {
    $env:ComSpec = Join-Path $env:SystemRoot "System32\\cmd.exe"
}
if (-not $env:LocalAppData) {
    $env:LocalAppData = Join-Path $env:USERPROFILE "AppData\\Local"
}
if (-not $env:APPDATA) {
    $env:APPDATA = Join-Path $env:USERPROFILE "AppData\\Roaming"
}
if (-not $env:TEMP) {
    $env:TEMP = Join-Path $env:LocalAppData "Temp"
}
if (-not $env:TMP) {
    $env:TMP = $env:TEMP
}

function Run-Example {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )

    Write-Host ""
    Write-Host "==> Running $Name" -ForegroundColor Cyan
    & go run @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "Example failed: $Name"
    }
    Write-Host "PASS: $Name" -ForegroundColor Green
}

function Have-Codex {
    try {
        $null = Get-Command codex -ErrorAction Stop
        return $true
    } catch {
        return $false
    }
}

function Test-CodexHealthy {
    & codex --help *> $null
    return ($LASTEXITCODE -eq 0)
}

Run-Example -Name "mock-adapter-playground" -Arguments @("./examples/mock-adapter-playground")
Run-Example -Name "mock-skills-contract" -Arguments @("./examples/mock-skills-contract")
Run-Example -Name "codex-skills-live" -Arguments @("./examples/codex-skills-live")

if (-not (Have-Codex)) {
    Write-Host ""
    Write-Host "SKIP: real Codex examples because 'codex' is not available on PATH" -ForegroundColor Yellow
    exit 0
}

if (-not (Test-CodexHealthy)) {
    Write-Host ""
    Write-Host "SKIP: real Codex examples because 'codex --help' failed in the current shell environment" -ForegroundColor Yellow
    exit 0
}

Run-Example -Name "codex-basic" -Arguments @("./examples/codex-basic")
Run-Example -Name "codex-stream" -Arguments @("./examples/codex-stream")
Run-Example -Name "codex-sessions" -Arguments @("./examples/codex-sessions")
Run-Example -Name "codex-admin-named" -Arguments @("./examples/codex-admin-named")
