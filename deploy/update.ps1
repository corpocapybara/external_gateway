# Quick deploy: copy egw.exe + each workspace's config/ops and restart services.
param(
    [string]$InstallDir = "C:\Program Files\external_gateway"
)
$ErrorActionPreference = "Stop"
$BuildDir = Split-Path -Parent $PSScriptRoot
. "$PSScriptRoot\workspaces.ps1"

$ws = Get-EgwWorkspaces -BuildDir $BuildDir
$names = ($ws | ForEach-Object { $_.X }) -join ', '

foreach ($w in $ws) {
    Stop-Service $w.Service -ErrorAction SilentlyContinue
    $svc = Get-Service $w.Service -ErrorAction SilentlyContinue
    if ($svc -and $svc.Status -ne 'Stopped') {
        Write-Host "Waiting for $($w.Service) to stop..."
        $svc.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(15))
    }
}
Start-Sleep -Seconds 2

Copy-Item "$BuildDir\egw.exe" "$InstallDir\egw.exe" -Force
foreach ($w in $ws) {
    $dir = Join-Path $InstallDir $w.X
    if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
    Copy-Item $w.ConfigFile "$dir\config.yaml"     -Force
    Copy-Item $w.OpsFile    "$dir\operations.yaml" -Force
    # Trim trailing whitespace (Go's yaml.v3 parser rejects it)
    foreach ($f in @("$dir\config.yaml", "$dir\operations.yaml")) {
        $txt = Get-Content $f -Raw -Encoding UTF8
        $txt = $txt -replace '(?m)[\t ]+\r?$', ''
        [IO.File]::WriteAllText($f, $txt, [Text.UTF8Encoding]::new($false))
    }
}

foreach ($w in $ws) {
    $svc = Get-Service $w.Service -ErrorAction SilentlyContinue
    if ($svc) {
        Start-Service $w.Service -ErrorAction SilentlyContinue
        Start-Sleep -Seconds 1
        if ((Get-Service $w.Service).Status -eq 'Running') {
            Write-Host "  $($w.Service) started" -ForegroundColor Green
        } else {
            Write-Host "  $($w.Service) failed to start" -ForegroundColor Red
        }
    } else {
        Write-Host "  $($w.Service) not installed, skipping" -ForegroundColor Yellow
    }
}

Write-Host "Deploy done: $names" -ForegroundColor Green
