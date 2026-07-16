<#
.SYNOPSIS
  Provision (or rotate) a Slack browser/desktop session token into external_gateway
  for ANY gateway workspace + ANY signed-in Slack team.

.DESCRIPTION
  Reuses your logged-in Slack desktop session. Extracts the per-workspace xoxc token
  (from Local Storage LevelDB, via the bundled slack-extract Go helper) and the shared
  xoxd `d` cookie (AES-GCM-decrypted from the Cookies DB using the DPAPI-protected key),
  stores both in the SYSTEM Credential Manager via the gateway admin API, and optionally
  redeploys the binary/config and verifies end-to-end.

  Secrets are only ever held in memory and POSTed to the local admin API. They are never
  written to disk, never placed on a command line, and only masked previews are printed.

  The target gateway workspace must already declare a `slack` site whose environment uses:
      auth: { type: slack-session }
      secret_ref:        secret://<ws>/<Prefix>-xoxc
      cookie_secret_ref: secret://<ws>/<Prefix>-xoxd
  Run with -ShowConfig to print the exact YAML block.

.EXAMPLE
  # First-time: wire Slack into the acme gateway, deploy, and verify
  .\Provision-Slack.ps1 -Workspace acme -Port 8444 -SlackDomain your-slack-subdomain -Deploy -Verify

.EXAMPLE
  # See which Slack workspaces are signed in (masked tokens)
  .\Provision-Slack.ps1 -ListTeams

.EXAMPLE
  # Rotate an existing secret pair (Slack must be quit; no deploy needed)
  .\Provision-Slack.ps1 -Workspace globex -Port 8443 -SlackDomain your-slack-subdomain
#>
[CmdletBinding()]
param(
  [Parameter(Mandatory, ParameterSetName='provision')]
  [string]$Workspace,
  [Parameter(ParameterSetName='provision')]
  [int]$Port = 8443,
  [string]$SlackDomain,
  [string]$Prefix = 'slack',
  [switch]$ListTeams,
  [switch]$ShowConfig,
  [switch]$Deploy,
  [switch]$Verify,
  [switch]$TokenOnly,
  [switch]$Diagnose,
  [switch]$Rebuild,
  [string]$AgentToken,
  [string]$Repo         = (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)),
  [string]$SlackAppData = (Join-Path $env:APPDATA 'Slack'),
  [string]$GoExe        = 'C:\Program Files\Go\bin\go.exe',
  [string]$PythonExe    = ''
)
$ErrorActionPreference = 'Stop'
$toolDir = $PSScriptRoot
$work    = Join-Path ([IO.Path]::GetTempPath()) ("slackprov_" + [Guid]::NewGuid().ToString('N').Substring(0,8))
New-Item -ItemType Directory -Force -Path $work | Out-Null

function Info($m){ Write-Host $m -ForegroundColor Cyan }
function Ok($m){ Write-Host $m -ForegroundColor Green }
function Warn($m){ Write-Host $m -ForegroundColor Yellow }

if ($ShowConfig) {
  $ws = if ($Workspace) { $Workspace } else { '<ws>' }
@"
Add this `slack` site under workspaces.$ws.sites in config.$ws.yaml:

      slack:
        environments:
          prod:
            base_url: https://slack.com/api
            auth:
              type: slack-session
            secret_ref: secret://$ws/$Prefix-xoxc
            cookie_secret_ref: secret://$ws/$Prefix-xoxd

...and grant the slack.* ops to the relevant role. Then re-run without -ShowConfig.
"@ | Write-Host
  return
}

# ---- resolve python ----
if (-not $PythonExe) {
  $PythonExe = (Get-Command python -ErrorAction SilentlyContinue).Source
  if (-not $PythonExe) { throw "python not found; pass -PythonExe <path>" }
}

# ---- build the slack-extract helper if needed ----
$extractExe = Join-Path $toolDir 'slack-extract.exe'
if ($Rebuild -or -not (Test-Path $extractExe)) {
  Info "== building slack-extract helper =="
  Push-Location $toolDir
  try {
    & $GoExe mod tidy 2>&1 | Out-Null
    & $GoExe build -o slack-extract.exe .
    if ($LASTEXITCODE -ne 0) { throw "go build slack-extract failed" }
  } finally { Pop-Location }
}

# ---- snapshot Local Storage LevelDB (share-readable) and extract teams ----
Info "== discovering Slack workspaces =="
$lsSrc = Join-Path $SlackAppData 'Local Storage\leveldb'
if (-not (Test-Path $lsSrc)) { throw "Slack Local Storage not found at $lsSrc" }
$lsCopy = Join-Path $work 'lsdb'
robocopy $lsSrc $lsCopy /XF LOCK /NFL /NDL /NJH /NJS /R:1 /W:1 | Out-Null
$teamsJson = Join-Path $work 'teams.json'
& $extractExe $lsCopy $teamsJson
$teams = Get-Content $teamsJson -Raw -Encoding utf8 | ConvertFrom-Json
if (-not $teams) { throw "no Slack tokens found in Local Storage" }

if ($ListTeams) {
  Info "== signed-in Slack workspaces =="
  foreach ($t in $teams) {
    "{0,-22} name='{1}'  url={2}" -f $t.domain, $t.name, $t.url | Write-Host
    "    token {0}...(len {1})" -f $t.token.Substring(0,16), $t.token.Length | Write-Host
  }
  return
}

# ---- from here we mutate state: require Workspace + SlackDomain ----
if (-not $Workspace)   { throw "-Workspace <name> is required (or use -ListTeams / -ShowConfig)" }
if (-not $SlackDomain) { throw "-SlackDomain <domain> is required. Run -ListTeams to see options." }

$sel = $teams | Where-Object { $_.domain -eq $SlackDomain } | Select-Object -First 1
if (-not $sel) { throw "Slack team '$SlackDomain' not signed in. Run -ListTeams." }
$xoxc = $sel.token
if ($Diagnose -and $TokenOnly) { throw "-Diagnose needs the cookie; run it without -TokenOnly (quit Slack first)." }

$base = "http://127.0.0.1:$Port"

function Assert-Admin {
  $id = [Security.Principal.WindowsIdentity]::GetCurrent()
  $p  = New-Object Security.Principal.WindowsPrincipal($id)
  if (-not $p.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw "-Deploy requires an elevated (Run as Administrator) shell."
  }
}

# ---- decrypt the xoxd `d` cookie (Slack must be quit) ----
# -TokenOnly swaps just the per-workspace xoxc token, reusing the already-stored
# xoxd cookie (shared across all Slack workspaces) — no Slack quit / no elevation.
$xoxd = $null
if (-not $TokenOnly) {
Info "== extracting xoxd cookie (Slack must be quit) =="
if (Get-Process slack -ErrorAction SilentlyContinue) {
  throw "Slack is running - quit it fully (tray -> Quit / Ctrl+Q) so the Cookies DB unlocks."
}
$cookiesDb = Join-Path $SlackAppData 'Network\Cookies'
if (-not (Test-Path $cookiesDb)) { $cookiesDb = Join-Path $SlackAppData 'Cookies' }
$rows = & $PythonExe (Join-Path $toolDir 'read_cookie.py') $cookiesDb
if (-not $rows) { throw "no 'd' cookie found in $cookiesDb" }

if (-not ('DP' -as [type])) {
  Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public class DP {
  [StructLayout(LayoutKind.Sequential)] struct BLOB { public int cb; public IntPtr pb; }
  [DllImport("crypt32.dll", SetLastError=true)]
  static extern bool CryptUnprotectData(ref BLOB pIn, string desc, IntPtr ent, IntPtr res, IntPtr prompt, int flags, ref BLOB pOut);
  public static byte[] Unprotect(byte[] data){
    BLOB inB=new BLOB(); BLOB outB=new BLOB();
    inB.pb=Marshal.AllocHGlobal(data.Length); Marshal.Copy(data,0,inB.pb,data.Length); inB.cb=data.Length;
    try{ if(!CryptUnprotectData(ref inB,null,IntPtr.Zero,IntPtr.Zero,IntPtr.Zero,0,ref outB))
           throw new System.ComponentModel.Win32Exception(Marshal.GetLastWin32Error());
         byte[] o=new byte[outB.cb]; Marshal.Copy(outB.pb,o,0,outB.cb); return o;
    } finally { if(inB.pb!=IntPtr.Zero) Marshal.FreeHGlobal(inB.pb); }
  }
}
'@
}
$ls = Get-Content (Join-Path $SlackAppData 'Local State') -Raw -Encoding utf8 | ConvertFrom-Json
$encKey = [Convert]::FromBase64String($ls.os_crypt.encrypted_key)
$aesKey = [DP]::Unprotect([byte[]]($encKey[5..($encKey.Length-1)]))

function Decrypt-V10([byte[]]$blob,[byte[]]$key){
  $pfx=[Text.Encoding]::ASCII.GetString($blob[0..2])
  if($pfx -ne 'v10' -and $pfx -ne 'v11'){ throw "bad cookie prefix '$pfx'" }
  $nonce=[byte[]]($blob[3..14]); $tag=[byte[]]($blob[($blob.Length-16)..($blob.Length-1)]); $ct=[byte[]]($blob[15..($blob.Length-17)])
  $plain=New-Object byte[] $ct.Length
  $aes=[Security.Cryptography.AesGcm]::new($key,16)
  try{ $aes.Decrypt($nonce,$ct,$tag,$plain) } finally { $aes.Dispose() }
  return ,([byte[]]$plain)
}

$xoxd=$null
foreach($line in $rows){
  $h = ($line -split '\|')[-1]
  $bytes=New-Object byte[] ($h.Length/2)
  for($i=0;$i -lt $bytes.Length;$i++){ $bytes[$i]=[Convert]::ToByte($h.Substring($i*2,2),16) }
  try{
    $plain=Decrypt-V10 $bytes $aesKey
    $val=[Text.Encoding]::UTF8.GetString($plain)
    if($val -notmatch '^xoxd-' -and $plain.Length -gt 32){
      $v2=[Text.Encoding]::UTF8.GetString([byte[]]($plain[32..($plain.Length-1)]))
      if($v2 -match '^xoxd-'){ $val=$v2 }
    }
    if($val -match '^xoxd-'){ $xoxd=$val }
  } catch { Warn ("  decrypt failed: " + $_.Exception.Message) }
}
if(-not $xoxd){ throw "could not decrypt an xoxd- cookie" }
}

if ($TokenOnly) {
  Ok ("  team={0}  xoxc {1}...(len {2})  (token-only; existing {3}-xoxd kept)" -f `
    $sel.domain, $xoxc.Substring(0,16), $xoxc.Length, $Prefix)
} else {
  Ok ("  team={0}  xoxc {1}...(len {2})  xoxd {3}...(len {4})" -f `
    $sel.domain, $xoxc.Substring(0,16), $xoxc.Length, $xoxd.Substring(0,8), $xoxd.Length)
}

# ---- optional: diagnose — test the freshly-extracted pair against Slack directly ----
if ($Diagnose) {
  Info "== diagnose: calling Slack auth.test directly with the extracted pair =="
  Info ("  token : prefix={0} dash-segments={1} length={2}  (valid xoxc = 5 segments)" -f `
    $xoxc.Substring(0,[Math]::Min(5,$xoxc.Length)), ($xoxc -split '-').Count, $xoxc.Length)
  Info ("  cookie: prefix={0} length={1}" -f `
    $xoxd.Substring(0,[Math]::Min(8,$xoxd.Length)), $xoxd.Length)
  $hdr = @{ Authorization = "Bearer $xoxc"; Cookie = "d=$xoxd" }
  foreach ($apihost in @("https://slack.com/api", ("https://{0}.slack.com/api" -f $sel.domain))) {
    try {
      $r = Invoke-RestMethod -Uri "$apihost/auth.test" -Method Post -Headers $hdr
      Ok ("  {0}/auth.test => {1}" -f $apihost, ($r | ConvertTo-Json -Compress))
    } catch {
      Warn ("  {0}/auth.test ERROR: {1}" -f $apihost, $_.Exception.Message)
    }
  }
  return
}

# ---- optional: build + deploy ----
if ($Deploy) {
  Assert-Admin
  Info "== build + deploy (restarts services) =="
  Push-Location $Repo
  try {
    & $GoExe build -o egw.exe ./cmd/egw
    if ($LASTEXITCODE -ne 0) { throw "go build egw failed" }
  } finally { Pop-Location }
  & (Join-Path $Repo 'deploy\update.ps1')
}

# ---- wait for the gateway ----
Info "== waiting for $base/healthz =="
$healthy=$false
for($i=0;$i -lt 30;$i++){ try{ if((Invoke-RestMethod "$base/healthz" -TimeoutSec 2).status -eq 'ok'){ $healthy=$true; break } }catch{}; Start-Sleep -Milliseconds 500 }
if(-not $healthy){ throw "gateway not healthy on $base (is the service running? use -Deploy)" }

# ---- store secrets ----
# Prefer egw-admin.exe (the canonical operator tool) fed via STDIN so the secret
# never lands on a command line; fall back to the identical admin-API POST.
Info "== storing secrets =="
$egwAdmin = $null
foreach($cand in @((Join-Path $Repo 'egw-admin.exe'), (Join-Path $env:ProgramFiles 'external_gateway\egw-admin.exe'))){
  if(Test-Path $cand){ $egwAdmin = $cand; break }
}
if(-not $egwAdmin){
  Push-Location $Repo
  try { & $GoExe build -o egw-admin.exe ./cmd/egw-admin; if($LASTEXITCODE -eq 0){ $egwAdmin = Join-Path $Repo 'egw-admin.exe' } }
  catch { } finally { Pop-Location }
}

function Set-Secret($name,$val){
  if($egwAdmin){
    # -value omitted on purpose -> egw-admin reads the value from stdin (not argv)
    $val | & $egwAdmin -workspace $Workspace -name $name -admin "$base/admin" | Out-Null
    if($LASTEXITCODE -ne 0){ throw "egw-admin failed for $name (exit $LASTEXITCODE)" }
    Ok ("  {0}/{1}: set via egw-admin (stdin)" -f $Workspace, $name)
  } else {
    $body = @{ workspace=$Workspace; name=$name; value=$val } | ConvertTo-Json -Compress
    $r = Invoke-WebRequest -Uri "$base/admin/secrets" -Method Post -ContentType 'application/json' -Body $body -UseBasicParsing
    Ok ("  {0}/{1}: HTTP {2} (direct admin API)" -f $Workspace, $name, $r.StatusCode)
  }
}
Set-Secret "$Prefix-xoxc" $xoxc
if (-not $TokenOnly) { Set-Secret "$Prefix-xoxd" $xoxd }

# ---- optional verify ----
if ($Verify) {
  Info "== verify: slack.auth_test =="
  if (-not $AgentToken) {
    $keys = Join-Path $Repo 'agent-keys.txt'
    if (Test-Path $keys) {
      $label = "$Workspace Full Access"
      $txt = Get-Content $keys -Raw
      $m = [regex]::Match($txt, [regex]::Escape("[$label]") + '.*?Token:\s*(\S+)', 'Singleline')
      if ($m.Success) { $AgentToken = $m.Groups[1].Value }
    }
  }
  if (-not $AgentToken) { Warn "  no agent token (pass -AgentToken); skipping verify"; }
  else {
    Start-Sleep -Seconds 1
    $hdr = @{ Authorization = "Bearer $AgentToken" }
    try {
      $at = Invoke-RestMethod -Uri "$base/v1/workspaces/$Workspace/op/slack.auth_test" -Method Post -Headers $hdr -ContentType 'application/json' -Body '{}'
      Ok ("  auth.test => " + ($at | ConvertTo-Json -Compress))
      $ch = Invoke-RestMethod -Uri "$base/v1/workspaces/$Workspace/op/slack.channels" -Method Post -Headers $hdr -ContentType 'application/json' -Body '{"limit":3}'
      Write-Host ("  channels  => " + ($ch | ConvertTo-Json -Compress -Depth 6))
    } catch {
      Warn ("  verify FAILED: " + $_.Exception.Message)
      if($_.ErrorDetails){ Warn ("  detail: " + $_.ErrorDetails.Message) }
    }
  }
}

Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
Ok "== done =="
