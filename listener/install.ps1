$ErrorActionPreference = 'Stop'
$taskName = 'TwitchListener'
$repoRoot = Split-Path -Parent $PSScriptRoot
$configPath = Join-Path $PSScriptRoot 'config.json'
$binaryPath = Join-Path $PSScriptRoot 'twitch-listener.exe'
$newBinaryPath = Join-Path $PSScriptRoot 'twitch-listener.new.exe'
$user = '{0}\{1}' -f $env:USERDOMAIN, $env:USERNAME

if (-not (Test-Path -LiteralPath $configPath -PathType Leaf)) {
    throw "Missing $configPath. Copy config.example.json to config.json and set media_dir."
}
foreach ($name in @('go.exe', 'ffmpeg.exe', 'ffprobe.exe')) {
    Get-Command $name -ErrorAction Stop | Out-Null
}

Push-Location $repoRoot
try {
    & (Get-Command go.exe).Source build -ldflags '-H windowsgui' -o $newBinaryPath ./listener
    if ($LASTEXITCODE -ne 0) { throw 'Listener build failed' }
} finally {
    Pop-Location
}

$oldTask = Get-ScheduledTask -TaskName $taskName -ErrorAction SilentlyContinue
if ($oldTask -and $oldTask.State -eq 'Running') {
    Stop-ScheduledTask -TaskName $taskName
    for ($attempt = 0; $attempt -lt 20; $attempt++) {
        if ((Get-ScheduledTask -TaskName $taskName).State -ne 'Running') { break }
        Start-Sleep -Milliseconds 500
    }
    if ((Get-ScheduledTask -TaskName $taskName).State -eq 'Running') {
        throw 'Previous listener process did not stop'
    }
}
Move-Item -LiteralPath $newBinaryPath -Destination $binaryPath -Force

$arguments = '-config "{0}"' -f $configPath
$action = New-ScheduledTaskAction -Execute $binaryPath -Argument $arguments -WorkingDirectory $PSScriptRoot
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $user
$settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -MultipleInstances IgnoreNew -ExecutionTimeLimit (New-TimeSpan -Seconds 0) -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
$principal = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Limited
Register-ScheduledTask -TaskName $taskName -Description 'Twitch Listener local web interface' -Action $action -Trigger $trigger -Settings $settings -Principal $principal -Force | Out-Null
Start-ScheduledTask -TaskName $taskName

$address = (Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json).listen_addr
for ($attempt = 0; $attempt -lt 20; $attempt++) {
    try {
        $health = Invoke-RestMethod -Uri "http://$address/api/health" -TimeoutSec 2
        if ($health.status -eq 'ok') {
            Write-Output "Twitch Listener is running at http://$address"
            exit 0
        }
    } catch {
        Start-Sleep -Seconds 1
    }
}
throw "Listener did not become healthy. Check $(Join-Path $PSScriptRoot 'listener.log')."
