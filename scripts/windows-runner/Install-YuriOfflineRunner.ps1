[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$RepositoryUrl,

    [string]$Branch = "dev",

    [string]$InstallRoot = "C:\YuriOfflineRunner",

    [ValidateRange(15, 3600)]
    [int]$PollSeconds = 60,

    [switch]$RegisterScheduledTask,

    [string]$TaskName = "Yuri Offline Windows CI"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if (-not [System.IO.Path]::IsPathRooted($InstallRoot)) {
    throw "InstallRoot must be an absolute Windows path."
}

$runnerDirectory = Join-Path $InstallRoot "runner"
$configPath = Join-Path $InstallRoot "runner.config.json"
New-Item -ItemType Directory -Path $runnerDirectory -Force | Out-Null

foreach ($scriptName in @(
    "Invoke-YuriOfflineRunner.ps1",
    "Invoke-YuriWindowsPipeline.ps1",
    "Test-YuriWindowsLaunch.ps1"
)) {
    $sourceScript = Join-Path $PSScriptRoot $scriptName
    $tokens = $null
    $parseErrors = $null
    [System.Management.Automation.Language.Parser]::ParseFile(
        $sourceScript,
        [ref]$tokens,
        [ref]$parseErrors
    ) | Out-Null
    if ($parseErrors.Count -gt 0) {
        throw "PowerShell parser rejected $sourceScript`: $($parseErrors -join '; ')"
    }
    Copy-Item -LiteralPath $sourceScript -Destination (Join-Path $runnerDirectory $scriptName) -Force
}

$config = [ordered]@{
    repositoryUrl = $RepositoryUrl
    branch = $Branch
    pollSeconds = $PollSeconds
    retryFailedAfterMinutes = 30
    workRoot = $InstallRoot
    artifactRetention = 5
    logRetention = 20
    launchSmokeFlows = @("onboarding", "voice")
}
$config | ConvertTo-Json | Set-Content -LiteralPath $configPath -Encoding UTF8

if ($RegisterScheduledTask) {
    $powershell = (Get-Command powershell.exe -ErrorAction Stop).Source
    $runnerScript = Join-Path $runnerDirectory "Invoke-YuriOfflineRunner.ps1"
    $arguments = "-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$runnerScript`" -ConfigPath `"$configPath`""
    $identity = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
    $action = New-ScheduledTaskAction -Execute $powershell -Argument $arguments
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User $identity
    $principal = New-ScheduledTaskPrincipal -UserId $identity -LogonType Interactive -RunLevel Limited
    $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -ExecutionTimeLimit ([timespan]::Zero) -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1) `
        -MultipleInstances IgnoreNew
    Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger `
        -Principal $principal -Settings $settings -Description "Polls Yuri dev and runs the native Windows pipeline." -Force | Out-Null
    Write-Host "Scheduled task registered: $TaskName"
}

Write-Host "Yuri offline runner installed in $InstallRoot"
Write-Host "Config: $configPath"
Write-Host "Test it now:"
Write-Host "  & '$runnerDirectory\Invoke-YuriOfflineRunner.ps1' -ConfigPath '$configPath' -Once"
