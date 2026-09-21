param(
    [switch]$DryRun,
    [ValidateSet('S4U', 'Interactive')]
    [string]$LogonType = 'S4U'
)

$ErrorActionPreference = 'Stop'
$taskName = 'TwitchRecorderTransfer'
$scriptPath = Join-Path $PSScriptRoot 'transfer-recordings.ps1'
$powershellPath = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$user = '{0}\{1}' -f $env:USERDOMAIN, $env:USERNAME

if (-not (Test-Path -LiteralPath $scriptPath -PathType Leaf)) {
    throw "Transfer script is missing: $scriptPath"
}

$arguments = '-NoProfile -NonInteractive -ExecutionPolicy Bypass -File "{0}"' -f $scriptPath
if ($DryRun) { $arguments += ' -DryRun' }
$action = New-ScheduledTaskAction -Execute $powershellPath -Argument $arguments -WorkingDirectory $PSScriptRoot
$triggers = @(
    New-ScheduledTaskTrigger -Once -At (Get-Date).AddMinutes(2) -RepetitionInterval (New-TimeSpan -Hours 2) -RepetitionDuration (New-TimeSpan -Days 3650)
)
if ($LogonType -eq 'S4U') {
    $triggers += New-ScheduledTaskTrigger -AtStartup
} else {
    $triggers += New-ScheduledTaskTrigger -AtLogOn -User $user
}
$settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -MultipleInstances IgnoreNew -ExecutionTimeLimit (New-TimeSpan -Hours 4) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 10)
$principal = New-ScheduledTaskPrincipal -UserId $user -LogonType $LogonType -RunLevel Limited

Register-ScheduledTask -TaskName $taskName -Description 'Move completed Twitch recordings to the configured Windows folder every two hours' -Action $action -Trigger $triggers -Settings $settings -Principal $principal -Force | Out-Null
Get-ScheduledTask -TaskName $taskName | Select-Object TaskName, State
