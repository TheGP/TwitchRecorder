param(
    [string]$Remote = 'root@reviewer',
    [string]$RemoteDir = '/root/TwitchRecorder/recordings',
    [string]$Destination = 'F:\DJ',
    [switch]$DryRun
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$logPath = Join-Path $PSScriptRoot 'transfer.log'

function Write-TransferLog([string]$Message) {
    $line = '{0} {1}' -f (Get-Date -Format o), $Message
    Add-Content -LiteralPath $logPath -Value $line
    Write-Output $line
}

function Invoke-Remote([string]$Command) {
    $output = & $ssh -o BatchMode=yes -o ConnectTimeout=15 -o StrictHostKeyChecking=yes $Remote $Command 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "SSH failed: $($output -join ' ')"
    }
    return $output
}

function Get-RemoteHash([string]$Name) {
    $line = @(Invoke-Remote "sha256sum -- $RemoteDir/$Name") | Select-Object -First 1
    if (-not $line -or $line -notmatch '^([a-fA-F0-9]{64})\s') {
        throw "No valid SHA-256 checksum for $Name"
    }
    return $Matches[1].ToLowerInvariant()
}

try {
    if ($Remote -notmatch '^[a-zA-Z0-9_.@-]+$' -or
        $RemoteDir -notmatch '^/[a-zA-Z0-9_./-]+$' -or
        $RemoteDir.Contains('..')) {
        throw 'Invalid remote address or directory'
    }
    if (-not (Test-Path -LiteralPath $Destination -PathType Container)) {
        throw "Destination is unavailable: $Destination"
    }
    $ssh = (Get-Command ssh.exe -ErrorAction Stop).Source
    $scp = (Get-Command scp.exe -ErrorAction Stop).Source
    $lock = [System.IO.File]::Open((Join-Path $PSScriptRoot 'transfer.lock'), 'OpenOrCreate', 'ReadWrite', 'None')
    try {
        $names = @(Invoke-Remote "find $RemoteDir -maxdepth 1 -type f -name '*.ts' -printf '%f\n'")
        $failed = $false
        foreach ($rawName in $names) {
            $name = $rawName.Trim()
            if (-not $name) { continue }
            try {
                if ($name -notmatch '^[a-z0-9_]+-[0-9]{4}-[0-9]{2}-[0-9]{2}(-[0-9]+)?\.ts$') {
                    throw "Unexpected recording filename: $name"
                }
                if ($DryRun) {
                    Write-TransferLog "Would transfer $name"
                    continue
                }

                $remoteHash = Get-RemoteHash $name
                $finalPath = Join-Path $Destination $name
                if (Test-Path -LiteralPath $finalPath) {
                    $localHash = (Get-FileHash -LiteralPath $finalPath -Algorithm SHA256).Hash.ToLowerInvariant()
                    if ($localHash -ne $remoteHash) {
                        throw "Local file differs; keeping server copy: $name"
                    }
                } else {
                    $temporaryPath = "$finalPath.download"
                    if (Test-Path -LiteralPath $temporaryPath) {
                        Remove-Item -LiteralPath $temporaryPath -Force
                    }
                    $remoteSource = '{0}:{1}/{2}' -f $Remote, $RemoteDir.TrimEnd('/'), $name
                    $copyOutput = & $scp -B -q -p -o BatchMode=yes -o ConnectTimeout=15 -o StrictHostKeyChecking=yes $remoteSource $temporaryPath 2>&1
                    if ($LASTEXITCODE -ne 0) {
                        throw "SCP failed for $name`: $($copyOutput -join ' ')"
                    }
                    $localHash = (Get-FileHash -LiteralPath $temporaryPath -Algorithm SHA256).Hash.ToLowerInvariant()
                    if ($localHash -ne $remoteHash -or (Get-RemoteHash $name) -ne $remoteHash) {
                        throw "Checksum changed during transfer; keeping server copy: $name"
                    }
                    [System.IO.File]::Move($temporaryPath, $finalPath)
                }

                if ((Get-RemoteHash $name) -ne $localHash) {
                    throw "Checksum changed before removal; keeping server copy: $name"
                }
                Invoke-Remote "bash /root/TwitchRecorder/delete-verified-recording.sh $name $localHash" | Out-Null
                Write-TransferLog "Transferred and removed server copy: $name"
            } catch {
                $failed = $true
                Write-TransferLog "ERROR: $($_.Exception.Message)"
            }
        }
        if (-not $names.Count) {
            Write-TransferLog 'No completed recordings to transfer'
        }
        if ($failed) { exit 1 }
    } finally {
        $lock.Dispose()
    }
} catch {
    Write-TransferLog "ERROR: $($_.Exception.Message)"
    exit 1
}
