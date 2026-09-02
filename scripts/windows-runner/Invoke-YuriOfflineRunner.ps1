[CmdletBinding()]
param(
    [string]$ConfigPath = (Join-Path $PSScriptRoot "runner.config.json"),
    [switch]$Once,
    [switch]$Force
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
$env:GIT_TERMINAL_PROMPT = "0"
$env:GCM_INTERACTIVE = "never"

function Read-RunnerConfig {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) {
        throw "Runner config not found: $Path. Copy runner.config.example.json or run Install-YuriOfflineRunner.ps1."
    }
    $config = Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json
    foreach ($required in @("repositoryUrl", "branch", "workRoot")) {
        if ([string]::IsNullOrWhiteSpace([string]$config.$required)) {
            throw "Runner config property '$required' is required."
        }
    }
    if (-not [System.IO.Path]::IsPathRooted([string]$config.workRoot)) {
        throw "workRoot must be an absolute Windows path."
    }
    return $config
}

function Get-ConfigValue {
    param([object]$Config, [string]$Name, [object]$Default)
    $property = $Config.PSObject.Properties[$Name]
    if ($null -eq $property) { return $Default }
    return $property.Value
}

function Invoke-Git {
    param([string[]]$Arguments, [string]$WorkingDirectory)
    if ($WorkingDirectory) { Push-Location $WorkingDirectory }
    try {
        & $script:git @Arguments
        if ($LASTEXITCODE -ne 0) {
            throw "git $($Arguments -join ' ') failed with exit code $LASTEXITCODE."
        }
    }
    finally {
        if ($WorkingDirectory) { Pop-Location }
    }
}

function Write-JsonAtomically {
    param([object]$Value, [string]$Path)
    $temporary = "$Path.tmp"
    $Value | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $temporary -Encoding UTF8
    Move-Item -LiteralPath $temporary -Destination $Path -Force
}

function Read-State {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) {
        return [pscustomobject]@{
            lastCommit = ""
            result = "never"
            buildResult = "never"
            attemptedAtUtc = ""
            buildAttemptedAtUtc = ""
        }
    }
    try { return Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json }
    catch { throw "Runner state is not valid JSON: $Path ($($_.Exception.Message))" }
}

function Remove-OldItems {
    param([string]$Path, [int]$Keep, [switch]$Directories)
    if ($Keep -lt 1 -or -not (Test-Path -LiteralPath $Path)) { return }
    $items = if ($Directories) {
        Get-ChildItem -LiteralPath $Path -Directory
    } else {
        Get-ChildItem -LiteralPath $Path -File
    }
    $items | Sort-Object LastWriteTimeUtc -Descending | Select-Object -Skip $Keep | ForEach-Object {
        Remove-Item -LiteralPath $_.FullName -Recurse:$Directories -Force
    }
}

$resolvedConfigPath = (Resolve-Path -LiteralPath $ConfigPath).Path
$config = Read-RunnerConfig $resolvedConfigPath
$workRoot = [System.IO.Path]::GetFullPath([string]$config.workRoot)
$checkout = Join-Path $workRoot "checkout"
$logs = Join-Path $workRoot "logs"
$artifacts = Join-Path $workRoot "artifacts"
$toolsDirectory = Join-Path $workRoot "tools"
$statePath = Join-Path $workRoot "status.json"
$latestPath = Join-Path $workRoot "latest-success.json"
$pollSeconds = [int](Get-ConfigValue -Config $config -Name "pollSeconds" -Default 60)
$retryMinutes = [int](Get-ConfigValue -Config $config -Name "retryFailedAfterMinutes" -Default 30)
$artifactRetention = [int](Get-ConfigValue -Config $config -Name "artifactRetention" -Default 5)
$logRetention = [int](Get-ConfigValue -Config $config -Name "logRetention" -Default 20)
$launchSmokeConfig = Get-ConfigValue -Config $config -Name "launchSmokeFlows" -Default @("onboarding", "voice")
$launchSmokeFlows = @($launchSmokeConfig | ForEach-Object { [string]$_ })

if ($pollSeconds -lt 15) { throw "pollSeconds must be at least 15." }
if ($retryMinutes -lt 1) { throw "retryFailedAfterMinutes must be at least 1." }
if ($artifactRetention -lt 1 -or $logRetention -lt 1) { throw "Retention values must be at least 1." }
if ($Force -and -not $Once) { throw "-Force is only supported together with -Once." }

$script:git = (Get-Command git.exe -ErrorAction Stop).Source
& $script:git check-ref-format --branch ([string]$config.branch) | Out-Null
if ($LASTEXITCODE -ne 0) { throw "Invalid Git branch name: '$($config.branch)'." }
New-Item -ItemType Directory -Path $workRoot, $logs, $artifacts, $toolsDirectory -Force | Out-Null

$mutexNameBytes = [System.Text.Encoding]::UTF8.GetBytes($resolvedConfigPath.ToLowerInvariant())
$mutexHash = [System.BitConverter]::ToString([System.Security.Cryptography.SHA256]::Create().ComputeHash($mutexNameBytes)).Replace("-", "")
$mutex = [System.Threading.Mutex]::new($false, "Local\YuriOfflineRunner-$($mutexHash.Substring(0, 20))")
$hasMutex = $false
try {
    try { $hasMutex = $mutex.WaitOne(0) } catch [System.Threading.AbandonedMutexException] { $hasMutex = $true }
    if (-not $hasMutex) { throw "Another Yuri offline runner is already using this configuration." }

    Write-Host "Yuri offline runner watches '$($config.branch)' every $pollSeconds seconds."
    Write-Host "Status: $statePath"

    while ($true) {
        $cycleStarted = [datetime]::UtcNow
        try {
            if (-not (Test-Path -LiteralPath (Join-Path $checkout ".git"))) {
                if (Test-Path -LiteralPath $checkout) {
                    throw "Checkout path exists but is not a Git repository: $checkout"
                }
                Invoke-Git @("clone", "--no-checkout", "--", [string]$config.repositoryUrl, $checkout) ""
            }

            Invoke-Git @("remote", "set-url", "origin", [string]$config.repositoryUrl) $checkout
            Invoke-Git @("fetch", "--prune", "origin", "+refs/heads/$($config.branch):refs/remotes/origin/$($config.branch)") $checkout
            $commit = (& $script:git -C $checkout rev-parse "refs/remotes/origin/$($config.branch)").Trim()
            if ($LASTEXITCODE -ne 0 -or $commit -notmatch '^[0-9a-f]{40}$') {
                throw "Could not resolve origin/$($config.branch). Has the branch been pushed?"
            }

            $state = Read-State $statePath
            $retryDue = $false
            if ($state.buildResult -eq "failed" -and $state.buildAttemptedAtUtc) {
                $retryDue = [datetime]::Parse([string]$state.buildAttemptedAtUtc).ToUniversalTime().AddMinutes($retryMinutes) -le [datetime]::UtcNow
            }
            $shouldBuild = $Force -or $state.lastCommit -ne $commit -or $retryDue
            if (-not $shouldBuild) {
                Write-Host "[$([datetime]::Now.ToString('HH:mm:ss'))] No new commit; $($commit.Substring(0, 12)) build is $($state.buildResult)."
            }
            else {
                $shortCommit = $commit.Substring(0, 12)
                $attemptedAt = [datetime]::UtcNow
                $logPath = Join-Path $logs ("{0}-{1}.log" -f $attemptedAt.ToString("yyyyMMdd-HHmmss"), $shortCommit)
                $artifactDirectory = Join-Path $artifacts $commit
                Write-Host "[$([datetime]::Now.ToString('HH:mm:ss'))] Testing $shortCommit from origin/$($config.branch)."

                Write-JsonAtomically ([ordered]@{
                    branch = [string]$config.branch
                    lastCommit = $commit
                    result = "running"
                    buildResult = "running"
                    attemptedAtUtc = $attemptedAt.ToString("o")
                    buildAttemptedAtUtc = $attemptedAt.ToString("o")
                    finishedAtUtc = $null
                    logPath = $logPath
                    artifactPath = $null
                    message = "Pipeline is running"
                }) $statePath

                $succeeded = $false
                $failureMessage = ""
                Start-Transcript -LiteralPath $logPath -Force | Out-Null
                try {
                    Invoke-Git @("reset", "--hard", $commit) $checkout
                    Invoke-Git @("clean", "-ffdx") $checkout
                    $pipeline = Join-Path $checkout "scripts\windows-runner\Invoke-YuriWindowsPipeline.ps1"
                    $pipelineArguments = @{
                        RepositoryRoot = $checkout
                        ArtifactDirectory = $artifactDirectory
                        ToolsDirectory = $toolsDirectory
                        LaunchSmokeFlows = $launchSmokeFlows
                    }
                    & $pipeline @pipelineArguments
                    $succeeded = $true
                }
                catch {
                    $failureMessage = $_.Exception.Message
                    Write-Warning $failureMessage
                }
                finally {
                    Stop-Transcript | Out-Null
                }

                $finishedAt = [datetime]::UtcNow
                if ($succeeded) {
                    $artifactPath = Join-Path $artifactDirectory "yuri.exe"
                    $status = [ordered]@{
                        branch = [string]$config.branch
                        lastCommit = $commit
                        result = "passed"
                        buildResult = "passed"
                        attemptedAtUtc = $attemptedAt.ToString("o")
                        buildAttemptedAtUtc = $attemptedAt.ToString("o")
                        finishedAtUtc = $finishedAt.ToString("o")
                        logPath = $logPath
                        artifactPath = $artifactPath
                        message = "All Windows checks and configured launch smokes passed"
                    }
                    Write-JsonAtomically $status $statePath
                    Write-JsonAtomically $status $latestPath
                    Write-Host "PASS $shortCommit -> $artifactPath"
                }
                else {
                    Write-JsonAtomically ([ordered]@{
                        branch = [string]$config.branch
                        lastCommit = $commit
                        result = "failed"
                        buildResult = "failed"
                        attemptedAtUtc = $attemptedAt.ToString("o")
                        buildAttemptedAtUtc = $attemptedAt.ToString("o")
                        finishedAtUtc = $finishedAt.ToString("o")
                        logPath = $logPath
                        artifactPath = $null
                        message = $failureMessage
                    }) $statePath
                    Write-Warning "FAIL $shortCommit. See $logPath"
                }
                Remove-OldItems $logs $logRetention
                Remove-OldItems $artifacts $artifactRetention -Directories
            }
        }
        catch {
            $message = $_.Exception.Message
            Write-Warning "Polling cycle failed: $message"
            $previousState = Read-State $statePath
            Write-JsonAtomically ([ordered]@{
                branch = [string]$config.branch
                lastCommit = [string]$previousState.lastCommit
                result = "poll-error"
                buildResult = [string]$previousState.buildResult
                attemptedAtUtc = $cycleStarted.ToString("o")
                buildAttemptedAtUtc = [string]$previousState.buildAttemptedAtUtc
                finishedAtUtc = [datetime]::UtcNow.ToString("o")
                logPath = $null
                artifactPath = $null
                message = $message
            }) $statePath
        }

        if ($Once) { break }
        Start-Sleep -Seconds $pollSeconds
    }
}
finally {
    if ($hasMutex) { $mutex.ReleaseMutex() }
    $mutex.Dispose()
}
