param(
    [ValidateSet("codex", "claude", "cursor")]
    [string]$Agent = "codex",
    [string]$Command = "",
    [string]$Model = "",
    [string]$Timeout = "90s",
    [switch]$Skip,
    [switch]$KeepWorkspace
)

$arguments = @(
    "run",
    "./examples/tools/live-smoke",
    "-agent=$Agent",
    "-timeout=$Timeout"
)
if ($Command) { $arguments += "-command=$Command" }
if ($Model) { $arguments += "-model=$Model" }
if ($Skip) { $arguments += "-skip" }
if ($KeepWorkspace) { $arguments += "-keep-workspace" }

& go @arguments
exit $LASTEXITCODE
