# Workspace discovery for external_gateway deploy tooling.
#
# The workspace token `x` is the single source of truth: it is the `<x>` in
# `config.<x>.yaml`. Everything else is derived from it —
#   deploy dir     = <InstallDir>\<x>
#   service name   = egw-<x>
#   ops file       = operations.<x>.yaml
#   CredMgr / API  = egw/<x>/<name>  and  /v1/workspaces/<x>/...
#   port           = <BasePort> + (alphabetical index), unless the config sets
#                    an explicit `server.port` (override). Duplicate ports fail.
#
# Dot-source this file, then call Get-EgwWorkspaces.

function Get-EgwWorkspaces {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)] [string]$BuildDir,
        [int]$BasePort = 8443
    )

    $configs = Get-ChildItem -Path $BuildDir -Filter 'config.*.yaml' -File |
        Where-Object { $_.Name -ne 'config.yaml' } |
        Sort-Object Name    # alphabetical by config.<x>.yaml == alphabetical by x

    if (-not $configs) { throw "no config.<x>.yaml files found in $BuildDir" }

    $list = @()
    $i = 0
    foreach ($f in $configs) {
        if ($f.Name -notmatch '^config\.(.+)\.yaml$') { continue }
        $x   = $Matches[1]
        $raw = Get-Content $f.FullName -Raw

        # Grab a top-level YAML block's body: everything after `<key>:` up to the next
        # line that starts at column 0 (the next top-level key), tolerating blank lines.
        function Get-Block([string]$text, [string]$key) {
            if ($text -match "(?ms)^$([regex]::Escape($key)):\r?\n(.*?)(?=^\S|\z)") { return $Matches[1] }
            return ''
        }

        # optional port override: an *uncommented* `port:` inside the top-level server block
        $override = $null
        $serverBlock = Get-Block $raw 'server'
        if ($serverBlock -match '(?m)^[ \t]+port:\s*(\d+)') { $override = [int]$Matches[1] }

        # roles: 2-space-indented keys under the top-level roles: block
        $roles = @()
        foreach ($line in ((Get-Block $raw 'roles') -split "\r?\n")) {
            if ($line -match '^\s{2}([A-Za-z0-9_-]+):\s*$') { $roles += $Matches[1] }
        }

        # secret names this workspace references: secret://<x>/<name>
        $secrets = @()
        foreach ($m in [regex]::Matches($raw, "secret://$([regex]::Escape($x))/([A-Za-z0-9_.-]+)")) {
            if ($secrets -notcontains $m.Groups[1].Value) { $secrets += $m.Groups[1].Value }
        }

        $list += [pscustomobject]@{
            X            = $x
            ConfigFile   = $f.FullName
            OpsFile      = Join-Path $BuildDir "operations.$x.yaml"
            Service      = "egw-$x"
            PortOverride = $override
            AlphaIndex   = $i
            Roles        = $roles
            Secrets      = $secrets
            Port         = 0
        }
        $i++
    }

    # resolve ports: override wins, else base + alphabetical index
    foreach ($w in $list) {
        $w.Port = if ($null -ne $w.PortOverride) { $w.PortOverride } else { $BasePort + $w.AlphaIndex }
    }

    # conflict check
    $conflicts = $list | Group-Object Port | Where-Object { $_.Count -gt 1 }
    if ($conflicts) {
        $detail = ($conflicts | ForEach-Object {
            "port $($_.Name) <- " + (($_.Group | ForEach-Object { $_.X }) -join ', ')
        }) -join '; '
        throw "egw workspace port conflict: $detail. Set a unique 'server.port' in the affected config.<x>.yaml."
    }

    return $list
}
