# external_gateway Setup Script
# Creates egw-svc account, ACLs, and installs the Windows service
# Run once by operator (admin), NOT by the agent

param(
    [string]$ServiceName = "egw",
    [string]$DisplayName = "External Gateway Service",
    [string]$Description = "Hardened proxy for LLM agents to call external services",
    [string]$BinaryPath = "C:\Program Files\external_gateway\egw.exe",
    [string]$ConfigPath = "C:\Program Files\external_gateway\config.yaml",
    [int]$Port = 8443
)

$ErrorActionPreference = "Stop"

function Write-Step {
    param([string]$Message)
    Write-Host "[*] $Message" -ForegroundColor Cyan
}

function Write-Success {
    param([string]$Message)
    Write-Host "[+] $Message" -ForegroundColor Green
}

function Write-Failure {
    param([string]$Message)
    Write-Host "[-] $Message" -ForegroundColor Red
}

function New-ServiceAccount {
    Write-Step "Creating service account 'egw-svc'"

    $existingUser = Get-LocalUser -Name "egw-svc" -ErrorAction SilentlyContinue
    if ($existingUser) {
        Write-Success "Service account already exists"
        return
    }

    $password = -join ((33..126) | Get-Random -Count 32 | ForEach-Object { [char]$_ })
    $securePassword = ConvertTo-SecureString $password -AsPlainText -Force

    New-LocalUser -Name "egw-svc" -Password $securePassword -Description "External Gateway Service Account" -PasswordNeverExpires
    Write-Success "Created service account 'egw-svc'"
}

function Grant-ServiceLogonRights {
    Write-Step "Granting SeServiceLogonRight to egw-svc"

    $sid = (Get-LocalUser -Name "egw-svc").SID.Value
    $existing = secedit /export /cfg "$env:TEMP\secpol.cfg" | Out-Null
    $cfg = Get-Content "$env:TEMP\secpol.cfg" -Raw

    if ($cfg -notmatch "SeServiceLogonRight.*egw-svc") {
        $cfg = $cfg -replace "(SeServiceLogonRight\s*=\s*)(.*)", "`$1`$2,`"$sid`""
        Set-Content -Path "$env:TEMP\secpol.cfg" -Value $cfg -NoNewline
        secedit /configure /db "$env:TEMP\secedit.sdb" /cfg "$env:TEMP\secpol.cfg" /areas SECURITYPOLICY | Out-Null
        Write-Success "Granted SeServiceLogonRight"
    } else {
        Write-Success "SeServiceLogonRight already granted"
    }

    Remove-Item "$env:TEMP\secpol.cfg" -Force -ErrorAction SilentlyContinue
}

function Deny-InteractiveLogon {
    Write-Step "Denying interactive and network logon to egw-svc"

    $sid = (Get-LocalUser -Name "egw-svc").SID.Value

    $denyRules = @(
        "SeDenyInteractiveLogonRight",
        "SeDenyNetworkLogonRight"
    )

    foreach ($rule in $denyRules) {
        $existing = secedit /export /cfg "$env:TEMP\secpol_$rule.cfg" | Out-Null
        $cfg = Get-Content "$env:TEMP\secpol_$rule.cfg" -Raw

        if ($cfg -notmatch "$sid") {
            $cfg = $cfg -replace "($rule\s*=\s*)(.*)", "`$1`$2,`"$sid`""
            Set-Content -Path "$env:TEMP\secpol_$rule.cfg" -Value $cfg -NoNewline
            secedit /configure /db "$env:TEMP\secedit_$rule.sdb" /cfg "$env:TEMP\secpol_$rule.cfg" /areas SECURITYPOLICY | Out-Null
            Write-Success "Set $rule"
        } else {
            Write-Success "$rule already set"
        }

        Remove-Item "$env:TEMP\secpol_$rule.cfg" -Force -ErrorAction SilentlyContinue
    }
}

function Install-Binary {
    Write-Step "Installing binary to $BinaryPath"

    $dir = Split-Path $BinaryPath -Parent
    if (-not (Test-Path $dir)) {
        New-Item -ItemType Directory -Path $dir -Force | Out-Null
    }

    if (Test-Path $BinaryPath) {
        Write-Success "Binary already installed"
        return
    }

    Write-Warning "Binary not found at $BinaryPath"
    Write-Warning "Build the binary first with: go build -o egw.exe ./cmd/egw"
}

function Set-ACLs {
    Write-Step "Setting ACLs on program directory"

    $dir = Split-Path $BinaryPath -Parent

    $acl = Get-Acl $dir

    $egwUser = "egw-svc"
    $adminGroup = "Administrators"

    $inherit = [System.Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [System.Security.AccessControl.InheritanceFlags]::ObjectInherit
    $propagation = [System.Security.AccessControl.PropagationFlags]::None
    $access = [System.Security.AccessControl.AccessControlType]::Allow

    $egwRights = [System.Security.AccessControl.FileSystemRights]::ReadAndExecute
    $rule1 = New-Object System.Security.AccessControl.FileSystemAccessRule($egwUser, $egwRights, $inherit, $propagation, $access)
    $acl.SetAccessRule($rule1)

    $adminRights = [System.Security.AccessControl.FileSystemRights]::FullControl
    $rule2 = New-Object System.Security.AccessControl.FileSystemAccessRule($adminGroup, $adminRights, $inherit, $propagation, $access)
    $acl.SetAccessRule($rule2)

    try {
        $usersRule = $acl.Access | Where-Object { $_.IdentityReference.Value -like "*Users*" }
        if ($usersRule) {
            foreach ($r in $usersRule) {
                $acl.RemoveAccessRule($r) | Out-Null
            }
        }
    } catch {}

    Set-Acl $dir $acl
    Write-Success "ACLs set on $dir"
}

function Register-WindowsService {
    Write-Step "Registering Windows service"

    $existing = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($existing) {
        Write-Success "Service already registered"
        return
    }

    $binDir = Split-Path $BinaryPath -Parent

    & "$BinaryPath" --install --config "$ConfigPath" --ops "$binDir\operations.yaml" 2>&1 | Out-Null
    if ($LASTEXITCODE -ne 0) {
        Write-Failure "Failed to install service"
        return
    }

    Write-Success "Service registered as '$ServiceName'"
}

function New-AuditDirectory {
    Write-Step "Creating audit log directory"

    $auditDir = "C:\ProgramData\external_gateway\audit"
    if (-not (Test-Path $auditDir)) {
        New-Item -ItemType Directory -Path $auditDir -Force | Out-Null
    }

    $acl = Get-Acl $auditDir
    $egwUser = "egw-svc"
    $adminGroup = "Administrators"

    $inherit = [System.Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [System.Security.AccessControl.InheritanceFlags]::ObjectInherit
    $propagation = [System.Security.AccessControl.PropagationFlags]::None
    $access = [System.Security.AccessControl.AccessControlType]::Allow

    $egwRights = [System.Security.AccessControl.FileSystemRights]::Write -bor [System.Security.AccessControl.FileSystemRights]::Read
    $rule1 = New-Object System.Security.AccessControl.FileSystemAccessRule($egwUser, $egwRights, $inherit, $propagation, $access)
    $acl.AddAccessRule($rule1)

    $adminRights = [System.Security.AccessControl.FileSystemRights]::FullControl
    $rule2 = New-Object System.Security.AccessControl.FileSystemAccessRule($adminGroup, $adminRights, $inherit, $propagation, $access)
    $acl.AddAccessRule($rule2)

    try {
        $usersRule = $acl.Access | Where-Object { $_.IdentityReference.Value -like "*Users*" }
        if ($usersRule) {
            foreach ($r in $usersRule) {
                $acl.RemoveAccessRule($r) | Out-Null
            }
        }
    } catch {}

    Set-Acl $auditDir $acl
    Write-Success "Audit directory created at $auditDir"
}

function Start-ServiceIfNotRunning {
    Write-Step "Starting service"

    $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if (-not $svc) {
        Write-Failure "Service not found, cannot start"
        return
    }

    if ($svc.Status -eq 'Running') {
        Write-Success "Service already running"
        return
    }

    Start-Service -Name $ServiceName
    Start-Sleep -Seconds 2

    $svc = Get-Service -Name $ServiceName
    if ($svc.Status -eq 'Running') {
        Write-Success "Service started"
    } else {
        Write-Failure "Service failed to start"
    }
}

Write-Host ""
Write-Host "=== external_gateway Setup ===" -ForegroundColor Magenta
Write-Host ""

if (-not ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    Write-Failure "This script must be run as Administrator"
    Write-Host "Right-click PowerShell -> Run as Administrator"
    exit 1
}

Write-Step "This script will:"
Write-Host "  1. Create service account 'egw-svc'"
Write-Host "  2. Configure logon rights and restrictions"
Write-Host "  3. Install the binary and set ACLs"
Write-Host "  4. Register the Windows service"
Write-Host "  5. Create audit log directory"
Write-Host ""

$confirm = Read-Host "Continue? (y/N)"
if ($confirm -ne 'y' -and $confirm -ne 'Y') {
    Write-Host "Aborted"
    exit 0
}

Write-Host ""

try {
    New-ServiceAccount
    Grant-ServiceLogonRights
    Deny-InteractiveLogon
    Install-Binary
    Set-ACLs
    New-AuditDirectory
    Register-WindowsService
    Start-ServiceIfNotRunning

    Write-Host ""
    Write-Success "Setup complete!"
    Write-Host ""
    Write-Host "Next steps:"
    Write-Host "  1. Build the binary: go build -o egw.exe ./cmd/egw"
    Write-Host "  2. Copy config: cp config.example/config.yaml C:\Program Files\external_gateway\"
    Write-Host "  3. Set up secrets: .\egw-admin.exe -workspace <name> -name jira -value '...'"
    Write-Host "  4. Generate agent tokens and configure your agent harness"
    Write-Host ""

} catch {
    Write-Failure "Setup failed: $_"
    exit 1
}
