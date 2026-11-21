# Helpers for deploy\update.ps1. Dot-source this file. They take no real service or path of their
# own, so deploy\deploylib_windows_test.go can run them without stopping any gateway.

# Names of the services whose Status is Running, in input order. $Services holds objects with Name
# and Status (Get-Service output); $null entries (a service that is not installed) are skipped.
function Select-EgwRunningServices {
    param([object[]]$Services)
    @($Services | Where-Object { $_ -and "$($_.Status)" -eq 'Running' } | ForEach-Object { $_.Name })
}

# Copy-Item that waits out a locked destination: the SCM can report a service Stopped before its
# process has released egw.exe. Retries only an IOException that is not a missing file or folder,
# printing one line per retry, until $TimeoutSeconds have passed; then it throws, destination unchanged.
function Copy-EgwFileWithRetry {
    param(
        [Parameter(Mandatory)] [string]$Source,
        [Parameter(Mandatory)] [string]$Destination,
        [int]$TimeoutSeconds = 30,
        [int]$IntervalMilliseconds = 1000
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ($true) {
        try {
            Copy-Item -LiteralPath $Source -Destination $Destination -Force -ErrorAction Stop
            return
        } catch [System.IO.FileNotFoundException], [System.IO.DirectoryNotFoundException] {
            throw
        } catch [System.IO.IOException] {
            if ((Get-Date) -ge $deadline) {
                throw "copy to $Destination still failing after $TimeoutSeconds s: $($_.Exception.Message)"
            }
            Write-Host "  $Destination locked, retrying..." -ForegroundColor Yellow
            Start-Sleep -Milliseconds $IntervalMilliseconds
        }
    }
}

# Runs $Action; when it throws, starts each of $Services again (the gateways that were running before
# the deploy stopped them) and rethrows the original error. A start that fails is a warning, so the
# rest still start. $StartService is injectable for tests.
function Invoke-EgwWithServiceRestore {
    param(
        [string[]]$Services,
        [Parameter(Mandatory)] [scriptblock]$Action,
        [scriptblock]$StartService = { param($Name) Start-Service -Name $Name -ErrorAction Stop }
    )
    try {
        & $Action
    } catch {
        $failure = $_
        Write-Host "Deploy failed: $($failure.Exception.Message)" -ForegroundColor Red
        foreach ($name in $Services) {
            try {
                & $StartService $name
                Write-Host "  $name restarted on the previous files" -ForegroundColor Yellow
            } catch {
                Write-Warning "$name did not start again: $($_.Exception.Message)"
            }
        }
        throw $failure
    }
}
