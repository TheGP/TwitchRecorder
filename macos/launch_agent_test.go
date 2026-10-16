package macos

import (
	"context"
	"encoding/xml"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestLaunchAgentPlists(t *testing.T) {
	powershell, err := exec.LookPath("pwsh")
	if err != nil {
		powershell, err = exec.LookPath("powershell.exe")
	}
	if err != nil {
		t.Skip("PowerShell is required to test launch agent generation")
	}
	for _, schedule := range []string{"-StartInterval 7200", "-RestartOnFailure"} {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		command := exec.CommandContext(ctx, powershell, "-NoProfile", "-NonInteractive", "-Command",
			`. ./launch-agent.ps1; New-LaunchAgentPlist -Label 'com.twitchrecorder.test' -ProgramArguments @('/Users/test/Music & files/player', '-config', '/Users/test/a<b>/config.json') -WorkingDirectory '/Users/test/Music & files' -ExecutablePath '/opt/homebrew/bin:/usr/bin' -LogPrefix '/Users/test/Music & files/agent' `+schedule)
		output, err := command.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("generate plist: %v\n%s", err, output)
		}
		var plist struct {
			Dict struct {
				Arguments []string `xml:"array>string"`
			} `xml:"dict"`
		}
		if err := xml.Unmarshal(output, &plist); err != nil {
			t.Fatalf("invalid XML: %v\n%s", err, output)
		}
		if len(plist.Dict.Arguments) != 3 || plist.Dict.Arguments[0] != "/Users/test/Music & files/player" || plist.Dict.Arguments[2] != "/Users/test/a<b>/config.json" {
			t.Fatalf("paths were split or escaped incorrectly: %v", plist.Dict.Arguments)
		}
		text := string(output)
		if !strings.Contains(text, "<key>RunAtLoad</key><true/>") || !strings.Contains(text, "<key>PATH</key>") {
			t.Fatal("login startup or runtime PATH missing")
		}
		if schedule == "-StartInterval 7200" && (!strings.Contains(text, "<key>StartInterval</key><integer>7200</integer>") || strings.Contains(text, "<key>KeepAlive</key>")) {
			t.Fatal("sync must run every two hours without an endless restart loop")
		}
		if schedule == "-RestartOnFailure" && (!strings.Contains(text, "<key>SuccessfulExit</key><false/>") || strings.Contains(text, "<key>StartInterval</key>")) {
			t.Fatal("listener must restart on failure, without a periodic restart")
		}
	}
}
