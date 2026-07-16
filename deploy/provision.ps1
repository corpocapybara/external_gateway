param(
    [string]$InstallDir = "C:\Program Files\external_gateway",
    [switch]$SkipBuild,
    [switch]$SkipSecrets
)

$ErrorActionPreference = "Stop"
$ScriptDir = Split-Path -Parent $PSScriptRoot
$BuildDir = $ScriptDir
$OutputFile = "$ScriptDir\agent-keys.txt"

. "$PSScriptRoot\workspaces.ps1"

function Write-Step { param([string]$Msg) Write-Host "[*] $Msg" -ForegroundColor Cyan }
function Write-Success { param([string]$Msg) Write-Host "[+] $Msg" -ForegroundColor Green }
function Write-Failure { param([string]$Msg) Write-Host "[-] $Msg" -ForegroundColor Red }

Write-Host ""
Write-Host "=== external_gateway - Full Provision ===" -ForegroundColor Magenta
Write-Host ""

if (-not ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Failure "Must run as Administrator"
    exit 1
}

# ---- 0. Discover workspaces from config.<x>.yaml (single source of truth) ----
# Deploy dir, service name, ops file, port, roles, and secret names all derive
# from the workspace token <x>. Ports are alphabetical (base 8443) unless a
# config sets server.port; duplicates abort here.
Write-Step "Discovering workspaces from config.<x>.yaml ..."
$ws = Get-EgwWorkspaces -BuildDir $BuildDir
foreach ($w in $ws) {
    Write-Success ("{0}: service {1}, port {2}, dir {3}\{0}, roles [{4}]" -f `
        $w.X, $w.Service, $w.Port, $InstallDir, ($w.Roles -join ', '))
}

# ---- 1. Build ----
if (-not $SkipBuild) {
    Write-Step "Building binaries..."
    Push-Location $BuildDir
    $env:CGO_ENABLED = 0
    go build -o egw.exe ./cmd/egw 2>&1 | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "egw build failed" }
    go build -o egw-admin.exe ./cmd/egw-admin 2>&1 | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "egw-admin build failed" }
    Pop-Location
    Write-Success "Binaries built"
}

# ---- 2. Ensure target directories ----
Write-Step "Creating install directories..."
$dirs = @($InstallDir) + ($ws | ForEach-Object { Join-Path $InstallDir $_.X })
foreach ($d in $dirs) {
    if (-not (Test-Path $d)) { New-Item -ItemType Directory -Path $d -Force | Out-Null }
}

# ---- 2b. Stop running services to unlock binaries ----
Write-Step "Stopping services for update..."
foreach ($w in $ws) {
    $s = Get-Service -Name $w.Service -ErrorAction SilentlyContinue
    if ($s -and $s.Status -eq "Running") {
        Stop-Service $w.Service -Force
        Write-Success "$($w.Service) stopped"
    }
}
Start-Sleep -Seconds 2

# ---- 3. Copy binaries and configs ----
Write-Step "Copying files..."
Copy-Item "$BuildDir\egw.exe" "$InstallDir\egw.exe" -Force
Copy-Item "$BuildDir\egw-admin.exe" "$InstallDir\egw-admin.exe" -Force
foreach ($w in $ws) {
    $d = Join-Path $InstallDir $w.X
    Copy-Item $w.ConfigFile "$d\config.yaml"     -Force
    Copy-Item $w.OpsFile    "$d\operations.yaml" -Force
}
Write-Success "Binaries and configs copied"

# ---- 4. Generate agent tokens (one per role declared in each config.<x>.yaml) ----
Write-Step "Generating fresh agent tokens..."

function New-Token {
    return -join ((65..90) + (97..122) + (48..57) | Get-Random -Count 48 | ForEach-Object { [char]$_ })
}
function Hash-Token($t) {
    $sha = [System.Security.Cryptography.SHA256]::Create()
    $h = $sha.ComputeHash([System.Text.Encoding]::UTF8.GetBytes($t))
    return [System.BitConverter]::ToString($h).Replace("-", "").ToLower()
}

$agentLinesByWs = @{}
foreach ($w in $ws) { $agentLinesByWs[$w.X] = @() }

$keyLines = @()
$keyLines += "external_gateway - Agent Keys"
$keyLines += "Generated: $(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')"
$keyLines += "Treat these as secrets - they grant access through the gateway."
$keyLines += ""
$keyLines += ("=" * 70)

foreach ($w in $ws) {
    foreach ($role in $w.Roles) {
        $raw = New-Token
        $hash = Hash-Token $raw
        $label = "$($w.X.ToUpper()) $role"

        $keyLines += "--- Key : [$label] ---"
        $keyLines += "  Token:    $raw"
        $keyLines += "  SHA256:   $hash"
        $keyLines += "  Endpoint: http://127.0.0.1:$($w.Port)"
        $keyLines += "  Header:   Authorization: Bearer $raw"
        $keyLines += ""

        $entry = @()
        $entry += "  - id: agent-$role"
        $entry += '    token_sha256: "' + $hash + '"'
        $entry += "    grants:"
        $entry += "      - workspace: $($w.X)"
        $entry += "        role: $role"
        $agentLinesByWs[$w.X] += $entry
    }
}

$keyLines += ("=" * 70)
$keyLines += "Gateway endpoints:"
foreach ($w in $ws) { $keyLines += ("  {0}: http://127.0.0.1:{1}" -f $w.X.ToUpper(), $w.Port) }
$keyLines += ""
$keyLines += "All requests need: Authorization: Bearer <token>"
$keyLines += ""

Set-Content -Path $OutputFile -Value ($keyLines -join "`r`n") -Encoding UTF8
Write-Success "Keys written to $OutputFile"

# ---- 5. Patch configs with tokens ----
Write-Step "Patching agent config sections..."
foreach ($w in $ws) {
    $agentBlock = @("agents:") + $agentLinesByWs[$w.X]
    $path = Join-Path (Join-Path $InstallDir $w.X) "config.yaml"
    $raw = Get-Content $path -Raw
    $raw = $raw -replace '(?s)agents:.*?(?=\r?\nworkspaces:)', ($agentBlock -join "`r`n")
    Set-Content -Path $path -Value $raw -Encoding UTF8
}
Write-Success "Agent configs patched"

# ---- 6. Service account ----
Write-Step "Creating service account 'egw-svc'..."
$existingUser = Get-LocalUser -Name "egw-svc" -ErrorAction SilentlyContinue
if (-not $existingUser) {
    $password = -join ((33..126) | Get-Random -Count 32 | ForEach-Object { [char]$_ })
    $securePassword = ConvertTo-SecureString $password -AsPlainText -Force
    New-LocalUser -Name "egw-svc" -Password $securePassword -Description "External Gateway Service Account" -PasswordNeverExpires
    Write-Success "Created egw-svc"
} else {
    Write-Success "egw-svc already exists"
}

# ---- 7. SeServiceLogonRight ----
Write-Step "Granting SeServiceLogonRight..."
$sid = (Get-LocalUser -Name "egw-svc").SID.Value
secedit /export /cfg "$env:TEMP\secpol.cfg" | Out-Null
$cfg = Get-Content "$env:TEMP\secpol.cfg" -Raw
if ($cfg -notmatch "egw-svc") {
    $cfg = $cfg -replace "(SeServiceLogonRight\s*=\s*)(.*)", ('$1$2,*' + $sid)
    Set-Content -Path "$env:TEMP\secpol.cfg" -Value $cfg -NoNewline
    secedit /configure /db "$env:TEMP\secedit.sdb" /cfg "$env:TEMP\secpol.cfg" /areas SECURITYPOLICY | Out-Null
}
Remove-Item "$env:TEMP\secpol.cfg" -Force -ErrorAction SilentlyContinue
Write-Success "SeServiceLogonRight granted"

# ---- 8. Deny interactive / network logon ----
Write-Step "Restricting logon rights..."
foreach ($rule in @("SeDenyInteractiveLogonRight", "SeDenyNetworkLogonRight")) {
    secedit /export /cfg "$env:TEMP\secpol_$rule.cfg" | Out-Null
    $c = Get-Content "$env:TEMP\secpol_$rule.cfg" -Raw
    if ($c -notmatch [regex]::Escape($sid)) {
        # Use simple string concatenation to avoid regex interpolation issues
        $c = $c -replace "($rule\s*=\s*)(.*)", ('$1$2,*' + $sid)
        Set-Content -Path "$env:TEMP\secpol_$rule.cfg" -Value $c -NoNewline
        secedit /configure /db "$env:TEMP\secedit_$rule.sdb" /cfg "$env:TEMP\secpol_$rule.cfg" /areas SECURITYPOLICY | Out-Null
    }
    Remove-Item "$env:TEMP\secpol_$rule.cfg" -Force -ErrorAction SilentlyContinue
}
Write-Success "Logon restrictions set"

# ---- 9. ACLs on install dir ----
Write-Step "Setting ACLs..."
$acl = Get-Acl $InstallDir
$inherit = [System.Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [System.Security.AccessControl.InheritanceFlags]::ObjectInherit
$prop = [System.Security.AccessControl.PropagationFlags]::None

# Use SID directly to avoid name-resolution race after account creation
$svcSid = [System.Security.Principal.SecurityIdentifier]::new($sid)
$acl.SetAccessRule((New-Object System.Security.AccessControl.FileSystemAccessRule($svcSid, "ReadAndExecute", $inherit, $prop, "Allow")))
$adminSid = [System.Security.Principal.SecurityIdentifier]::new("S-1-5-32-544")
$acl.SetAccessRule((New-Object System.Security.AccessControl.FileSystemAccessRule($adminSid, "FullControl", $inherit, $prop, "Allow")))
foreach ($r in $acl.Access | Where-Object { $_.IdentityReference.Value -like "*Users*" }) {
    $acl.RemoveAccessRule($r) | Out-Null
}
Set-Acl $InstallDir $acl
Write-Success "ACLs set"

# ---- 10. Audit directory ----
$auditDir = "C:\ProgramData\external_gateway\audit"
Write-Step "Creating audit directory..."
if (-not (Test-Path $auditDir)) { New-Item -ItemType Directory -Path $auditDir -Force | Out-Null }
$acl = Get-Acl $auditDir
$acl.AddAccessRule((New-Object System.Security.AccessControl.FileSystemAccessRule($svcSid, "Write,Read", $inherit, $prop, "Allow")))
$acl.SetAccessRule((New-Object System.Security.AccessControl.FileSystemAccessRule($adminSid, "FullControl", $inherit, $prop, "Allow")))
Set-Acl $auditDir $acl
Write-Success "Audit dir ready"

# ---- 11. Register services (svc-name egw-<x>, port derived per workspace) ----
foreach ($w in $ws) {
    Write-Step "Registering $($w.Service)..."
    $d = Join-Path $InstallDir $w.X
    if (Get-Service -Name $w.Service -ErrorAction SilentlyContinue) {
        cmd /c "`"$InstallDir\egw.exe`" --remove --svc-name $($w.Service) >nul 2>&1"
        Start-Sleep -Seconds 1
    }
    cmd /c "`"$InstallDir\egw.exe`" --install --config `"$d\config.yaml`" --ops `"$d\operations.yaml`" --svc-name $($w.Service) --port $($w.Port) >nul 2>&1"
    if ($LASTEXITCODE -eq 0) { Write-Success "$($w.Service) registered (port $($w.Port))" } else { Write-Failure "$($w.Service) failed (exit $LASTEXITCODE)" }
}

# ---- 12. Start services ----
Write-Step "Starting services..."
foreach ($w in $ws) { Start-Service $w.Service -ErrorAction SilentlyContinue }
Start-Sleep -Seconds 3
Write-Success "Services started"

# ---- 13. Verify ----
Write-Step "Verifying..."
foreach ($w in $ws) {
    try {
        $r = Invoke-WebRequest -Uri "http://127.0.0.1:$($w.Port)/healthz" -UseBasicParsing -TimeoutSec 5
        Write-Success "$($w.X) gateway OK (status $($r.StatusCode))"
    } catch { Write-Failure "$($w.X) gateway: $_" }
}

# ---- 14. Secrets (names derived from each config's secret://<x>/<name> refs) ----
if (-not $SkipSecrets) {
    Write-Host ""
    Write-Step "Setting up secrets. Paste values when prompted (Enter to skip each)."
    Write-Host ""

    function Prompt-Secret {
        param([string]$Workspace, [string]$Name, [int]$AdminPort)
        $val = Read-Host "  $Name [$Workspace]"
        if ($val) {
            $result = cmd /c "`"$InstallDir\egw-admin.exe`" -admin http://127.0.0.1:$AdminPort/admin -workspace $Workspace -name $Name -value `"$val`" 2>&1"
            if ($LASTEXITCODE -eq 0) { Write-Success "$Name set" } else { Write-Failure "$Name FAILED: $result" }
        }
    }

    foreach ($w in $ws) {
        if (-not $w.Secrets) { continue }
        Write-Host "--- $($w.X.ToUpper()) Secrets (port $($w.Port)) ---" -ForegroundColor Cyan
        foreach ($name in $w.Secrets) {
            Prompt-Secret -Workspace $w.X -Name $name -AdminPort $w.Port
        }
        Write-Host ""
    }
}

# ---- Summary ----
Write-Host ""
Write-Host ("=" * 60) -ForegroundColor Magenta
Write-Host "  PROVISION COMPLETE" -ForegroundColor Magenta
Write-Host ("=" * 60) -ForegroundColor Magenta
Write-Host ""
Write-Host "  Agent keys file:  $OutputFile" -ForegroundColor Yellow
foreach ($w in $ws) {
    Write-Host ("  {0,-6} endpoint: http://127.0.0.1:{1}   service: {2}" -f $w.X, $w.Port, $w.Service) -ForegroundColor Cyan
}
Write-Host ""

# Print tokens for immediate use
Write-Host "--- Agent Tokens ---" -ForegroundColor Cyan
$keyLines = Get-Content $OutputFile -Raw
$keyLines -split "`r`n" | ForEach-Object {
    if ($_ -match "^--- Key") {
        Write-Host "" -NoNewline
        Write-Host $_ -ForegroundColor Yellow
    } elseif ($_ -match "^  Token:") {
        Write-Host $_ -ForegroundColor White
    } elseif ($_ -match "^  Endpoint:") {
        Write-Host $_ -ForegroundColor Cyan
    } else {
        Write-Host $_
    }
}

Write-Host ""
Write-Host "  Distribute tokens above to your users." -ForegroundColor Yellow
Write-Host ""
