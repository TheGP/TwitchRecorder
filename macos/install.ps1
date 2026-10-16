#Requires -Version 7.0
param(
    [string]$Destination,
    [string]$Remote,
    [string]$RemoteDir,
    [string]$ListenAddr
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
if (-not $IsMacOS) { throw 'Run this installer on macOS. Windows uses listener/install.ps1 and transfer/register-transfer-task.ps1.' }
if ((& /usr/bin/id -u) -eq '0') { throw 'Run as your normal logged-in user, without sudo.' }
. (Join-Path $PSScriptRoot 'launch-agent.ps1')
$repoRoot = Split-Path -Parent $PSScriptRoot
$listenerDir = Join-Path $repoRoot 'listener'
$transferDir = Join-Path $repoRoot 'transfer'
$userProfile = [Environment]::GetFolderPath('UserProfile')
$agentDir = Join-Path $userProfile 'Library/LaunchAgents'
$uid = (& /usr/bin/id -u).Trim()
$domain = "gui/$uid"
$listenerLabel = 'com.twitchrecorder.listener'
$transferLabel = 'com.twitchrecorder.transfer'
$listenerConfigPath = Join-Path $listenerDir 'config.json'
$transferConfigPath = Join-Path $transferDir 'transfer-config.json'
$listenerConfig = @{}
$transferConfig = @{}
if (Test-Path -LiteralPath $listenerConfigPath) {
    $listenerConfig = Get-Content -LiteralPath $listenerConfigPath -Raw | ConvertFrom-Json -AsHashtable
}
if (Test-Path -LiteralPath $transferConfigPath) {
    $transferConfig = Get-Content -LiteralPath $transferConfigPath -Raw | ConvertFrom-Json -AsHashtable
}
if (-not $Destination) {
    if ($transferConfig.destination -and $listenerConfig.media_dir -and $transferConfig.destination -ne $listenerConfig.media_dir) {
        throw 'The existing sync and player folders differ. Pass -Destination to select a shared folder.'
    }
    $Destination = $transferConfig.destination
    if (-not $Destination) { $Destination = $listenerConfig.media_dir }
    if (-not $Destination) { $Destination = Join-Path $userProfile 'Music/Twitch' }
}
if ($Destination.StartsWith('~/')) { $Destination = Join-Path $userProfile $Destination.Substring(2) }
if (-not [IO.Path]::IsPathRooted($Destination)) { throw 'Destination must be an absolute macOS path.' }
$Destination = [IO.Path]::GetFullPath($Destination)
if ($Destination.StartsWith('/Volumes/')) {
    $volumePath = '/Volumes/' + $Destination.Split('/')[2]
    if (-not (Test-Path -LiteralPath $volumePath -PathType Container)) {
        throw "Backup disk is not mounted: $volumePath"
    }
}
foreach ($command in @('go', 'ffmpeg', 'ffprobe', 'ssh', 'scp', 'pwsh')) {
    Get-Command $command -CommandType Application -ErrorAction Stop | Out-Null
}
$pwshPath = (Get-Command pwsh -CommandType Application | Select-Object -First 1).Source
$executablePath = $env:PATH
$listenerBinary = Join-Path $listenerDir 'twitch-listener'
$newBinary = Join-Path $listenerDir 'twitch-listener.new'

# Never interrupt a sync while reinstalling its launch agent.
$transferState = & /bin/launchctl print "$domain/$transferLabel" 2>&1
if ($LASTEXITCODE -eq 0 -and ($transferState -join "`n") -match '(?m)^\s*pid = \d+') {
    throw 'A file sync is running. Run the installer again after it finishes.'
}

Push-Location $repoRoot
try {
    & go build -o $newBinary ./listener
    if ($LASTEXITCODE -ne 0) { throw 'Listener build failed' }
} finally {
    Pop-Location
}

New-Item -ItemType Directory -Path $Destination -Force | Out-Null
New-Item -ItemType Directory -Path $agentDir -Force | Out-Null
$listenerConfig.media_dir = $Destination
if ($ListenAddr) { $listenerConfig.listen_addr = $ListenAddr }
if (-not $listenerConfig.listen_addr) { $listenerConfig.listen_addr = '127.0.0.1:8787' }
$transferConfig.destination = $Destination
if ($Remote) { $transferConfig.remote = $Remote }
if ($RemoteDir) { $transferConfig.remote_dir = $RemoteDir }
if (-not $transferConfig.remote) { $transferConfig.remote = 'root@reviewer' }
if (-not $transferConfig.remote_dir) { $transferConfig.remote_dir = '/root/TwitchRecorder/recordings' }

foreach ($label in @($listenerLabel, $transferLabel)) {
    & /bin/launchctl print "$domain/$label" *> $null
    if ($LASTEXITCODE -eq 0) {
        & /bin/launchctl bootout "$domain/$label"
        if ($LASTEXITCODE -ne 0) { throw "Could not stop $label" }
    }
}
$listenerConfig | ConvertTo-Json | Set-Content -LiteralPath $listenerConfigPath -Encoding utf8
$transferConfig | ConvertTo-Json | Set-Content -LiteralPath $transferConfigPath -Encoding utf8
Move-Item -LiteralPath $newBinary -Destination $listenerBinary -Force

$listenerPlist = Join-Path $agentDir "$listenerLabel.plist"
$transferPlist = Join-Path $agentDir "$transferLabel.plist"
New-LaunchAgentPlist -Label $listenerLabel -ProgramArguments @($listenerBinary, '-config', $listenerConfigPath) -WorkingDirectory $listenerDir -ExecutablePath $executablePath -LogPrefix (Join-Path $listenerDir 'launch-agent') -RestartOnFailure |
    Set-Content -LiteralPath $listenerPlist -Encoding utf8
New-LaunchAgentPlist -Label $transferLabel -ProgramArguments @($pwshPath, '-NoProfile', '-NonInteractive', '-File', (Join-Path $transferDir 'transfer-recordings.ps1')) -WorkingDirectory $transferDir -ExecutablePath $executablePath -LogPrefix (Join-Path $transferDir 'launch-agent') -StartInterval 7200 |
    Set-Content -LiteralPath $transferPlist -Encoding utf8
foreach ($plist in @($listenerPlist, $transferPlist)) {
    & /usr/bin/plutil -lint $plist
    if ($LASTEXITCODE -ne 0) { throw "Invalid launch agent: $plist" }
}
& /bin/launchctl bootstrap $domain $listenerPlist
if ($LASTEXITCODE -ne 0) { throw 'Could not start listener launch agent' }
for ($attempt = 0; $attempt -lt 20; $attempt++) {
    try {
        $health = Invoke-RestMethod -Uri "http://$($listenerConfig.listen_addr)/api/health" -TimeoutSec 2
        if ($health.status -eq 'ok') { break }
    } catch { }
    Start-Sleep -Seconds 1
}
if (-not $health -or $health.status -ne 'ok') { throw "Listener did not become healthy. Check $listenerDir/listener.log." }
& /bin/launchctl bootstrap $domain $transferPlist
if ($LASTEXITCODE -ne 0) { throw 'Could not start transfer launch agent' }
Write-Output "Player: http://$($listenerConfig.listen_addr)"
Write-Output "Recordings: $Destination"
Write-Output 'Sync runs now, at login, and every two hours while logged in.'
