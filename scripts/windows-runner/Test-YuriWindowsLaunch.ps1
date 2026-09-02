[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Executable,

    [ValidateRange(5, 300)]
    [int]$TimeoutSeconds = 45,

    [ValidateSet("", "onboarding", "voice")]
    [string]$UiFlow = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$resolvedExecutable = (Resolve-Path -LiteralPath $Executable).Path
if ([System.IO.Path]::GetExtension($resolvedExecutable) -ne ".exe") {
    throw "Expected a Windows executable: $resolvedExecutable"
}

$smokeRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("yuri-windows-smoke-" + [guid]::NewGuid().ToString("N"))
$readyFile = Join-Path $smokeRoot "dom-ready.json"
$resultFile = Join-Path $smokeRoot "ui-result.json"
$stdoutFile = Join-Path $smokeRoot "stdout.log"
$stderrFile = Join-Path $smokeRoot "stderr.log"
$process = $null

$smokeEnvironment = @{
    "YURI_TEST_MODE" = "1"
    "YURI_TEST_PROFILE_ROOT" = $smokeRoot
    "YURI_TEST_READY_FILE" = $readyFile
    "YURI_TEST_AUTO_EXIT" = "1"
}
if ($UiFlow) {
    $smokeEnvironment["YURI_TEST_UI_FLOW"] = $UiFlow
    $smokeEnvironment["YURI_TEST_UI_RESULT_FILE"] = $resultFile
}

$previousEnvironment = @{}
foreach ($name in $smokeEnvironment.Keys) {
    $previousEnvironment[$name] = [System.Environment]::GetEnvironmentVariable($name, "Process")
    [System.Environment]::SetEnvironmentVariable($name, $smokeEnvironment[$name], "Process")
}

function Wait-ForFile {
    param(
        [Parameter(Mandatory = $true)] [string]$Path,
        [Parameter(Mandatory = $true)] [System.Diagnostics.Process]$Process,
        [Parameter(Mandatory = $true)] [datetime]$Deadline,
        [Parameter(Mandatory = $true)] [string]$Description
    )

    while (-not (Test-Path -LiteralPath $Path)) {
        if ($Process.HasExited) {
            throw "Yuri exited with code $($Process.ExitCode) before $Description was written."
        }
        if ([datetime]::UtcNow -ge $Deadline) {
            throw "Timed out waiting for $Description after $TimeoutSeconds seconds."
        }
        Start-Sleep -Milliseconds 250
    }
}

function Write-SmokeDiagnostics {
    foreach ($path in @($stdoutFile, $stderrFile, $readyFile, $resultFile)) {
        if (Test-Path -LiteralPath $path) {
            Write-Warning "--- $([System.IO.Path]::GetFileName($path)) ---"
            foreach ($line in (Get-Content -LiteralPath $path -ErrorAction SilentlyContinue | Select-Object -First 160)) {
                Write-Warning ([string]$line)
            }
        }
    }
}

try {
    New-Item -ItemType Directory -Path $smokeRoot | Out-Null
    $process = Start-Process -FilePath $resolvedExecutable -PassThru `
        -RedirectStandardOutput $stdoutFile -RedirectStandardError $stderrFile

    $deadline = [datetime]::UtcNow.AddSeconds($TimeoutSeconds)
    Wait-ForFile -Path $readyFile -Process $process -Deadline $deadline -Description "the DOM-ready marker"

    $ready = Get-Content -LiteralPath $readyFile -Raw | ConvertFrom-Json
    if ($ready.state -ne "ready" -or $ready.platform -notlike "windows/*") {
        throw "Unexpected readiness marker: $(Get-Content -LiteralPath $readyFile -Raw)"
    }

    if ($UiFlow) {
        Wait-ForFile -Path $resultFile -Process $process -Deadline ([datetime]::UtcNow.AddSeconds($TimeoutSeconds)) -Description "the $UiFlow UI result"
        $result = Get-Content -LiteralPath $resultFile -Raw | ConvertFrom-Json
        if ($result.flow -ne $UiFlow -or $result.state -ne "passed") {
            throw "The $UiFlow UI smoke failed: $(Get-Content -LiteralPath $resultFile -Raw)"
        }
    }

    $databaseFile = Join-Path $smokeRoot "data\yuri.sqlite3"
    if (-not (Test-Path -LiteralPath $databaseFile)) {
        throw "The isolated Yuri database was not created: $databaseFile"
    }

    if (-not $process.WaitForExit($TimeoutSeconds * 1000)) {
        throw "Yuri did not auto-exit within $TimeoutSeconds seconds."
    }
    if ($process.ExitCode -ne 0) {
        throw "Yuri launch smoke exited with code $($process.ExitCode)."
    }

    $label = if ($UiFlow) { "$UiFlow UI" } else { "launch" }
    Write-Host "Windows $label smoke passed: $resolvedExecutable"
}
catch {
    Write-SmokeDiagnostics
    throw
}
finally {
    if ($null -ne $process -and -not $process.HasExited) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
    }
    foreach ($name in $smokeEnvironment.Keys) {
        [System.Environment]::SetEnvironmentVariable($name, $previousEnvironment[$name], "Process")
    }
    if (Test-Path -LiteralPath $smokeRoot) {
        Remove-Item -LiteralPath $smokeRoot -Recurse -Force
    }
}
