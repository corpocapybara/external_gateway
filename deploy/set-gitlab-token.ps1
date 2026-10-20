# Set (or rotate) the GitLab PAT the gateway uses for a workspace.
# Run from an elevated Windows PowerShell:
#   powershell -ExecutionPolicy Bypass -File .\deploy\set-gitlab-token.ps1
# If config.<ws>.yaml has no admin_token_sha256 (the admin API is fail-closed), an admin token is
# generated, its hash added to the config, the token saved to agent-keys.txt, and the gateway redeployed.
param(
    [string]$Workspace = 'thp',
    [int]$Port = 8444,
    [string]$SecretName = 'gitlab'
)
$ErrorActionPreference = 'Stop'
$BuildDir = Split-Path -Parent $PSScriptRoot
$ConfigFile = Join-Path $BuildDir "config.$Workspace.yaml"
$KeysFile = Join-Path $BuildDir 'agent-keys.txt'
$AdminExe = Join-Path $BuildDir 'egw-admin.exe'
$Base = "http://127.0.0.1:$Port"
$AdminLabel = "[$($Workspace.ToUpper()) Admin]"

function Get-Sha256Hex([string]$text) {
    $bytes = [Security.Cryptography.SHA256]::Create().ComputeHash([Text.Encoding]::UTF8.GetBytes($text))
    return (($bytes | ForEach-Object { $_.ToString('x2') }) -join '')
}

function New-RandomToken {
    $bytes = New-Object byte[] 36
    [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    return ([Convert]::ToBase64String($bytes) -replace '[+/=]', '')
}

function Get-KeyFromFile([string]$label) {
    if (-not (Test-Path $KeysFile)) { return $null }
    $lines = [IO.File]::ReadAllLines($KeysFile)
    for ($i = 0; $i -lt $lines.Count; $i++) {
        if ($lines[$i].Contains("Key : $label")) {
            for ($j = $i + 1; $j -lt [Math]::Min($i + 5, $lines.Count); $j++) {
                if ($lines[$j] -match '^\s*Token:\s*(\S+)') { return $Matches[1] }
            }
        }
    }
    return $null
}

function Read-Secret([string]$prompt) {
    $secure = Read-Host -Prompt $prompt -AsSecureString
    $ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
    try { return [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr) }
    finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr) }
}

$principal = New-Object Security.Principal.WindowsPrincipal([Security.Principal.WindowsIdentity]::GetCurrent())
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Run this from an elevated (Run as administrator) PowerShell.'
}

$config = [IO.File]::ReadAllText($ConfigFile)
if ($config -notmatch '(?m)^admin_token_sha256:\s*"?[0-9a-f]{64}') {
    Write-Host "No admin_token_sha256 in $ConfigFile - generating an admin token" -ForegroundColor Yellow
    $adminToken = New-RandomToken
    $sha = Get-Sha256Hex $adminToken
    $config = $config.TrimEnd() + "`r`n`r`nadmin_token_sha256: `"$sha`"`r`n"
    [IO.File]::WriteAllText($ConfigFile, $config, (New-Object Text.UTF8Encoding($false)))
    $entry = "`r`n--- Key : $AdminLabel ---`r`n  Token:    $adminToken`r`n  SHA256:   $sha`r`n  Endpoint: $Base/admin`r`n  Header:   Authorization: Bearer $adminToken`r`n"
    [IO.File]::AppendAllText($KeysFile, $entry, (New-Object Text.UTF8Encoding($false)))
    Write-Host "Admin token saved to $KeysFile as $AdminLabel" -ForegroundColor Green
    & (Join-Path $PSScriptRoot 'update.ps1')
    Start-Sleep -Seconds 3
} else {
    $adminToken = Get-KeyFromFile $AdminLabel
    if (-not $adminToken) { $adminToken = Read-Secret 'Admin token (admin_token_sha256 is set but no token found in agent-keys.txt)' }
    if ($config -notmatch (Get-Sha256Hex $adminToken)) {
        throw 'That admin token does not match admin_token_sha256 in the config.'
    }
}

$pat = Read-Secret "GitLab personal access token (read_api) for $Workspace/$SecretName"
if (-not $pat) { throw 'No token entered.' }
$pat | & $AdminExe -workspace $Workspace -name $SecretName -admin "$Base/admin" -token $adminToken
if ($LASTEXITCODE -ne 0) { throw "egw-admin failed (exit $LASTEXITCODE)" }

$agentToken = $env:EGW_TOKEN
if (-not $agentToken) { $agentToken = Get-KeyFromFile "[$($Workspace.ToUpper()) Full Access]" }
if ($agentToken) {
    Start-Sleep -Seconds 1
    $r = Invoke-RestMethod -Uri "$Base/v1/workspaces/$Workspace/op/gitlab.whoami" -Method Post -Body '{}' `
           -ContentType 'application/json' -Headers @{ Authorization = "Bearer $agentToken" }
    if ($r.username) { Write-Host "OK: egw GitLab authenticates as $($r.username)" -ForegroundColor Green }
    else { Write-Host "gitlab.whoami did not return a user: $($r | ConvertTo-Json -Compress)" -ForegroundColor Red }
} else {
    Write-Host 'Secret set. No agent token found to verify with gitlab.whoami.' -ForegroundColor Yellow
}
