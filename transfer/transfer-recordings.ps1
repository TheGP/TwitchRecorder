param(
    [string]$Remote,
    [string]$RemoteDir,
    [string]$Destination,
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

function Get-RemoteFileType([string]$Name) {
    if ($Name -notmatch '^[a-zA-Z0-9_.-]+$') {
        throw "Invalid remote filename: $Name"
    }
    $type = @(Invoke-Remote "find $RemoteDir -maxdepth 1 -name '$Name' -printf '%y\n'") | Select-Object -First 1
    if (-not $type) { return $null }
    return $type
}

function Get-OptionalRemoteHash([string]$Name) {
    $type = Get-RemoteFileType $Name
    if (-not $type) { return $null }
    if ($type -ne 'f') {
        throw "Remote sidecar is not a regular file: $Name"
    }
    return Get-RemoteHash $Name
}

function Test-RemoteFile([string]$Name) {
    return $null -ne (Get-RemoteFileType $Name)
}

function Copy-VerifiedFile([string]$Name, [string]$RemoteHash) {
    $finalPath = Join-Path $Destination $Name
    if (Test-Path -LiteralPath $finalPath) {
        $localHash = (Get-FileHash -LiteralPath $finalPath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($localHash -ne $RemoteHash) {
            throw "Local file differs; keeping server copy: $Name"
        }
    } else {
        $temporaryPath = "$finalPath.download"
        if (Test-Path -LiteralPath $temporaryPath) {
            Remove-Item -LiteralPath $temporaryPath -Force
        }
        $remoteSource = '{0}:{1}/{2}' -f $Remote, $RemoteDir.TrimEnd('/'), $Name
        $copyOutput = & $scp -B -q -p -o BatchMode=yes -o ConnectTimeout=15 -o StrictHostKeyChecking=yes $remoteSource $temporaryPath 2>&1
        if ($LASTEXITCODE -ne 0) {
            throw "SCP failed for $Name`: $($copyOutput -join ' ')"
        }
        $localHash = (Get-FileHash -LiteralPath $temporaryPath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($localHash -ne $RemoteHash -or (Get-RemoteHash $Name) -ne $RemoteHash) {
            throw "Checksum changed during transfer; keeping server copy: $Name"
        }
        [System.IO.File]::Move($temporaryPath, $finalPath)
    }
    if ((Get-RemoteHash $Name) -ne $localHash) {
        throw "Checksum changed before removal; keeping server copy: $Name"
    }
    Invoke-Remote "bash /root/TwitchRecorder/delete-verified-recording.sh $Name $localHash" | Out-Null
    Write-TransferLog "Transferred and removed server copy: $Name"
}

try {
    $configPath = Join-Path $PSScriptRoot 'transfer-config.json'
    $config = $null
    if (Test-Path -LiteralPath $configPath -PathType Leaf) {
        $config = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
    }
    if ([string]::IsNullOrWhiteSpace($Destination)) {
        if (-not $config) {
            throw "Transfer config is missing: $configPath. Copy transfer-config.example.json to transfer-config.json and set destination."
        }
        if ($config.destination -isnot [string] -or [string]::IsNullOrWhiteSpace($config.destination)) {
            throw "Transfer config must contain a nonempty destination: $configPath"
        }
        $Destination = $config.destination
    }
    if ([string]::IsNullOrWhiteSpace($Remote)) {
        $Remote = 'root@reviewer'
        if ($config -and $config.remote) { $Remote = $config.remote }
    }
    if ([string]::IsNullOrWhiteSpace($RemoteDir)) {
        $RemoteDir = '/root/TwitchRecorder/recordings'
        if ($config -and $config.remote_dir) { $RemoteDir = $config.remote_dir }
    }
    if ($Remote -notmatch '^[a-zA-Z0-9_.@-]+$' -or
        $RemoteDir -notmatch '^/[a-zA-Z0-9_./-]+$' -or
        $RemoteDir.Contains('..')) {
        throw 'Invalid remote address or directory'
    }
    if (-not (Test-Path -LiteralPath $Destination -PathType Container)) {
        throw "Destination is unavailable: $Destination"
    }
    $ssh = (Get-Command ssh -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source
    $scp = (Get-Command scp -CommandType Application -ErrorAction Stop | Select-Object -First 1).Source
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
                $chatName = $name -replace '\.ts$', '.chat.jsonl'
                if ($DryRun) {
                    Write-TransferLog "Would transfer $name"
                    if (Get-OptionalRemoteHash $chatName) {
                        Write-TransferLog "Would transfer $chatName"
                    }
                    continue
                }

                $chatHash = Get-OptionalRemoteHash $chatName
                if ($chatHash) {
                    Copy-VerifiedFile $chatName $chatHash
                } elseif (Test-RemoteFile "$chatName.part") {
                    throw "Chat recording is still being finalized; deferring $name"
                }
                Copy-VerifiedFile $name (Get-RemoteHash $name)
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
