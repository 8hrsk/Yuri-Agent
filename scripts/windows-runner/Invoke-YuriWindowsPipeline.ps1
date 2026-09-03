[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$RepositoryRoot,

    [Parameter(Mandatory = $true)]
    [string]$ArtifactDirectory,

    [Parameter(Mandatory = $true)]
    [string]$ToolsDirectory,

    [ValidateSet("amd64", "arm64")]
    [string]$Architecture = "amd64",

    [string]$ArtifactBaseName = "yuri",

    [ValidatePattern('^[0-9]+\.[0-9]+\.[0-9]+$')]
    [string]$Version = "0.7.0",

    [string]$Commit = "unknown",

    [string]$BuildDate = ([datetime]::UtcNow.ToString("o")),

    [switch]$SkipChecks,

    [string[]]$LaunchSmokeFlows = @("onboarding", "voice")
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue"
$env:CI = "true"

$repository = (Resolve-Path -LiteralPath $RepositoryRoot).Path
$wailsVersion = "v2.15.0"
$goPackages = @("./cmd/...", "./internal/...", "./sdk/...", "./plugins/...")

function Require-Command {
    param([Parameter(Mandatory = $true)] [string]$Name)
    $command = Get-Command $Name -ErrorAction SilentlyContinue
    if ($null -eq $command) {
        throw "Required command '$Name' was not found in PATH."
    }
    return $command.Source
}

function Invoke-Native {
    param(
        [Parameter(Mandatory = $true)] [string]$FilePath,
        [Parameter(Mandatory = $true)] [string[]]$Arguments,
        [Parameter(Mandatory = $true)] [string]$Description
    )
    Write-Host "`n==> $Description"
    & $FilePath @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Description failed with exit code $LASTEXITCODE."
    }
}

function Invoke-InDirectory {
    param(
        [Parameter(Mandatory = $true)] [string]$Path,
        [Parameter(Mandatory = $true)] [scriptblock]$Action
    )
    Push-Location $Path
    try { & $Action } finally { Pop-Location }
}

function Test-GoFormatting {
    param([string]$Gofmt)
    Write-Host "`n==> Check Go formatting"
    $goFiles = Get-ChildItem -Path @(
        (Join-Path $repository "cmd"),
        (Join-Path $repository "internal"),
        (Join-Path $repository "sdk"),
        (Join-Path $repository "plugins")
    ) -Filter "*.go" -File -Recurse | ForEach-Object { $_.FullName }

    $unformatted = [System.Collections.Generic.List[string]]::new()
    for ($offset = 0; $offset -lt $goFiles.Count; $offset += 50) {
        $last = [Math]::Min($offset + 49, $goFiles.Count - 1)
        $batch = $goFiles[$offset..$last]
        $result = & $Gofmt -l @batch
        if ($LASTEXITCODE -ne 0) { throw "gofmt failed with exit code $LASTEXITCODE." }
        foreach ($path in $result) { $unformatted.Add($path) }
    }
    if ($unformatted.Count -gt 0) {
        throw "Go files require formatting:`n$($unformatted -join "`n")"
    }
}

$git = Require-Command "git.exe"
$go = Require-Command "go.exe"
$gofmt = Require-Command "gofmt.exe"
$node = Require-Command "node.exe"
$npm = Require-Command "npm.cmd"

$goVersion = (& $go env GOVERSION).Trim()
if ($LASTEXITCODE -ne 0 -or $goVersion -notmatch '^go1\.25(\.|$)') {
    throw "Yuri requires Go 1.25.x; found '$goVersion'."
}
$nodeVersion = (& $node -p "process.versions.node").Trim()
if ($LASTEXITCODE -ne 0 -or $nodeVersion -notmatch '^22\.') {
    throw "Yuri Windows CI requires Node.js 22.x; found '$nodeVersion'."
}

Invoke-InDirectory $repository {
    if (-not $SkipChecks) {
        Invoke-Native $git @("show", "--check", "--format=", "HEAD") "Check commit whitespace"
        Invoke-Native $go @("mod", "download") "Download Go modules"
        Test-GoFormatting $gofmt
        Invoke-Native $go (@("vet") + $goPackages) "Go vet"
        Invoke-Native $go (@("test", "-count=1", "-timeout=15m") + $goPackages) "Go tests"

        Invoke-Native $npm @("--prefix", "frontend", "ci", "--no-audit", "--no-fund") "Install frontend dependencies"
        Invoke-Native $npm @("--prefix", "frontend", "run", "lint") "Frontend ESLint"
        Invoke-Native $npm @("--prefix", "frontend", "run", "lint:css") "Frontend stylesheet lint"
        Invoke-Native $npm @("--prefix", "frontend", "run", "lint:contract") "Go/TypeScript bridge contract"
        Invoke-Native $npm @("--prefix", "frontend", "run", "typecheck") "Frontend typecheck"
        Invoke-Native $npm @("--prefix", "frontend", "test", "--", "--run") "Frontend tests"
        Invoke-Native $npm @("--prefix", "frontend", "run", "build") "Frontend production build"
    }

    New-Item -ItemType Directory -Path $ToolsDirectory -Force | Out-Null
    $wails = Join-Path $ToolsDirectory "wails.exe"
    $needsWailsInstall = -not (Test-Path -LiteralPath $wails)
    if (-not $needsWailsInstall) {
        $installedVersion = (& $wails version 2>&1 | Out-String)
        $needsWailsInstall = $LASTEXITCODE -ne 0 -or $installedVersion -notlike "*$wailsVersion*"
    }
    if ($needsWailsInstall) {
        $previousGoBin = $env:GOBIN
        try {
            $env:GOBIN = $ToolsDirectory
            Invoke-Native $go @("install", "github.com/wailsapp/wails/v2/cmd/wails@$wailsVersion") "Install Wails $wailsVersion"
        }
        finally {
            $env:GOBIN = $previousGoBin
        }
    }
    if (-not $SkipChecks) {
        Invoke-Native $wails @("doctor") "Wails environment diagnostics"
    }

    $desktopRoot = Join-Path $repository "cmd\yuri"
    $wailsConfig = Join-Path $desktopRoot "wails.json"
    $originalWailsConfig = [System.IO.File]::ReadAllText($wailsConfig)
    try {
        $config = $originalWailsConfig | ConvertFrom-Json
        $config.info.productVersion = $Version
        $updatedConfig = $config | ConvertTo-Json -Depth 20
        [System.IO.File]::WriteAllText($wailsConfig, "$updatedConfig`n", [System.Text.UTF8Encoding]::new($false))
        $ldflags = "-s -w -X github.com/OrdoAI/yuri-agent/internal/buildinfo.Version=$Version " +
            "-X github.com/OrdoAI/yuri-agent/internal/buildinfo.Commit=$Commit " +
            "-X github.com/OrdoAI/yuri-agent/internal/buildinfo.Date=$BuildDate"
        Invoke-InDirectory $desktopRoot {
            Invoke-Native $wails @(
                "build", "-clean", "-platform", "windows/$Architecture", "-trimpath",
                "-ldflags", $ldflags, "-nosyncgomod", "-s"
            ) "Build Windows Wails application"
        }
    }
    finally {
        [System.IO.File]::WriteAllText($wailsConfig, $originalWailsConfig, [System.Text.UTF8Encoding]::new($false))
    }
}

$builtExecutable = Join-Path $repository "cmd\yuri\build\bin\yuri.exe"
if (-not (Test-Path -LiteralPath $builtExecutable)) {
    throw "Wails reported success but did not create $builtExecutable."
}

$launchScript = Join-Path $repository "scripts\windows-runner\Test-YuriWindowsLaunch.ps1"
foreach ($flow in $LaunchSmokeFlows) {
    if ($flow -notin @("onboarding", "voice")) {
        throw "Unsupported launch smoke flow in configuration: '$flow'."
    }
    & $launchScript -Executable $builtExecutable -UiFlow $flow
}

New-Item -ItemType Directory -Path $ArtifactDirectory -Force | Out-Null
$artifact = Join-Path $ArtifactDirectory "$ArtifactBaseName.exe"
Copy-Item -LiteralPath $builtExecutable -Destination $artifact -Force
$checksum = (Get-FileHash -LiteralPath $artifact -Algorithm SHA256).Hash.ToLowerInvariant()
$checksumPath = Join-Path $ArtifactDirectory "$ArtifactBaseName.exe.sha256"
[System.IO.File]::WriteAllText($checksumPath, "$checksum  $ArtifactBaseName.exe`n", [System.Text.Encoding]::ASCII)

$metadataName = if ($ArtifactBaseName -eq "yuri") { "build.json" } else { "$ArtifactBaseName.json" }

$metadata = [ordered]@{
    version = $Version
    commit = $Commit
    builtAtUtc = $BuildDate
    platform = "windows/$Architecture"
    goVersion = $goVersion
    nodeVersion = $nodeVersion
    wailsVersion = $wailsVersion
    artifact = "$ArtifactBaseName.exe"
    sha256 = $checksum
}
$metadata | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $ArtifactDirectory $metadataName) -Encoding UTF8
Write-Host "`nWindows pipeline passed. Artifact: $artifact"
