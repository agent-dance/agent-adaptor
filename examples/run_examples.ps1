param(
    [string]$Agent = "",
    [string]$Command = ""
)

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

function Assert-AgentName {
    param([string]$Name)
    if ($Name -notin @("codex", "claude", "cursor", "codebuddy")) {
        throw "Agent must be codex, claude, cursor, or codebuddy; got '$Name'"
    }
}

function Default-AgentCommands {
	param([string]$Name)
	switch ($Name) {
		"claude" { return @("claude", "trpc-claudecode") }
		"cursor" { return @("agent", "cursor-agent") }
		"codebuddy" { return @("codebuddy") }
		default { return @("codex") }
	}
}

function Agent-CommandEnv {
    param([string]$Name)
    switch ($Name) {
        "claude" { return $env:CLAUDE_COMMAND }
        "cursor" { return $env:CURSOR_COMMAND }
        "codebuddy" { return $env:CODEBUDDY_COMMAND }
        default { return $env:CODEX_COMMAND }
    }
}

function Resolve-AgentCommand {
	param([string]$Name, [string]$Override)
	if ($Override) {
		return $Override
	}
	$fromEnv = Agent-CommandEnv -Name $Name
	if ($fromEnv) {
		return $fromEnv
	}
	$candidates = @(Default-AgentCommands -Name $Name)
	foreach ($candidate in $candidates) {
		if (Test-AgentHealthy -CommandPath $candidate) {
			return $candidate
		}
	}
	return $candidates[0]
}

function Test-AgentHealthy {
    param([string]$CommandPath)
    try {
        & $CommandPath --help *> $null
        return ($LASTEXITCODE -eq 0)
    } catch {
        return $false
    }
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

if (-not $Agent) {
    if ($env:AGENT_ADAPTOR_EXAMPLE_AGENT) {
        $Agent = $env:AGENT_ADAPTOR_EXAMPLE_AGENT
    } else {
        $Agent = "codex"
    }
}

Assert-AgentName -Name $Agent
# The codec example is deterministic and never starts a CLI. Run it even when
# no local provider is installed so the script always performs a useful smoke.
Run-Example -Name "threads/codec" -Arguments @("./examples/threads/codec", "-agent=$Agent")

$resolvedCommand = Resolve-AgentCommand -Name $Agent -Override $Command
if (-not (Test-AgentHealthy -CommandPath $resolvedCommand)) {
    Write-Host ""
    Write-Host "SKIP: selected local CLI '$Agent' is not healthy via command '$resolvedCommand --help'" -ForegroundColor Yellow
    exit 0
}

$common = @("-agent=$Agent")
$common += "-command=$resolvedCommand"

Run-Example -Name "quickstart" -Arguments (@("./examples/quickstart") + $common)
Run-Example -Name "structured-output" -Arguments (@("./examples/structured-output") + $common)
Run-Example -Name "streaming" -Arguments (@("./examples/streaming") + $common)
Run-Example -Name "threads" -Arguments (@("./examples/threads") + $common)
Run-Example -Name "inspect" -Arguments (@("./examples/inspect") + $common)
Run-Example -Name "skills" -Arguments (@("./examples/skills") + $common)
Run-Example -Name "profiles/resources" -Arguments (@("./examples/profiles/resources") + $common)
# -run=false keeps this one free: it materializes the profile and reports the
# on-disk evidence without ever invoking the paid model.
Run-Example -Name "profiles (sync only)" -Arguments (@("./examples/profiles", "-run=false", "-probe=false") + $common)
Run-Example -Name "a2a-server" -Arguments (@("./examples/a2a-server") + $common)
