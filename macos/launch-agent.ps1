function New-LaunchAgentPlist {
    param(
        [Parameter(Mandatory)][string]$Label,
        [Parameter(Mandatory)][string[]]$ProgramArguments,
        [Parameter(Mandatory)][string]$WorkingDirectory,
        [Parameter(Mandatory)][string]$ExecutablePath,
        [Parameter(Mandatory)][string]$LogPrefix,
        [int]$StartInterval = 0,
        [switch]$RestartOnFailure
    )
    $argumentsXml = foreach ($argument in $ProgramArguments) {
        '<string>{0}</string>' -f [System.Security.SecurityElement]::Escape($argument)
    }
    $scheduleXml = ''
    if ($StartInterval -gt 0) {
        $scheduleXml = '<key>StartInterval</key><integer>{0}</integer>' -f $StartInterval
    }
    if ($RestartOnFailure) {
        $scheduleXml += '<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict><key>ThrottleInterval</key><integer>10</integer>'
    }
    @"
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>$([System.Security.SecurityElement]::Escape($Label))</string>
<key>ProgramArguments</key><array>$($argumentsXml -join '')</array>
<key>WorkingDirectory</key><string>$([System.Security.SecurityElement]::Escape($WorkingDirectory))</string>
<key>EnvironmentVariables</key><dict><key>PATH</key><string>$([System.Security.SecurityElement]::Escape($ExecutablePath))</string></dict>
<key>RunAtLoad</key><true/>
<key>ProcessType</key><string>Background</string>
$scheduleXml
<key>StandardOutPath</key><string>$([System.Security.SecurityElement]::Escape("$LogPrefix.stdout.log"))</string>
<key>StandardErrorPath</key><string>$([System.Security.SecurityElement]::Escape("$LogPrefix.stderr.log"))</string>
</dict></plist>
"@
}
