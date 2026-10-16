package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Copies of this test executable act as SSH/SCP, so no test contacts a server.
func TestMain(m *testing.M) {
	command := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	if fixture := os.Getenv("TWITCH_TRANSFER_FIXTURE"); fixture != "" && (command == "ssh" || command == "scp") {
		if err := fakeTransferCommand(fixture, command); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fakeTransferCommand(fixture, command string) error {
	arguments := os.Args[1:]
	log, err := os.OpenFile(filepath.Join(fixture, "calls.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	fmt.Fprintln(log, command, strings.Join(arguments, " "))
	log.Close()
	if command == "scp" {
		source := filepath.Join(fixture, "remote", filepath.Base(arguments[len(arguments)-2]))
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		if os.Getenv("TWITCH_TRANSFER_CORRUPT") == "1" {
			data = append(data, 'x')
		}
		return os.WriteFile(arguments[len(arguments)-1], data, 0600)
	}
	remoteCommand := arguments[len(arguments)-1]
	if strings.Contains(remoteCommand, "-type f -name '*.ts'") {
		entries, err := os.ReadDir(filepath.Join(fixture, "remote"))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".ts") {
				fmt.Println(entry.Name())
			}
		}
		return nil
	}
	if strings.HasPrefix(remoteCommand, "find ") {
		match := regexp.MustCompile(`-name '([^']+)'`).FindStringSubmatch(remoteCommand)
		if len(match) != 2 {
			return fmt.Errorf("unexpected find: %s", remoteCommand)
		}
		_, err := os.Stat(filepath.Join(fixture, "remote", match[1]))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Println("f")
		return nil
	}
	fields := strings.Fields(remoteCommand)
	var name string
	switch fields[0] {
	case "sha256sum":
		name = filepath.Base(fields[len(fields)-1])
	case "bash":
		name = fields[len(fields)-2]
	default:
		return fmt.Errorf("unexpected remote command: %s", remoteCommand)
	}
	path := filepath.Join(fixture, "remote", name)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	if fields[0] == "sha256sum" {
		fmt.Printf("%s  %s\n", hash, name)
		return nil
	}
	if fields[len(fields)-1] != hash {
		return fmt.Errorf("refusing to delete mismatched file")
	}
	return os.Remove(path)
}

func TestTransferVerification(t *testing.T) {
	powershell, err := exec.LookPath("pwsh")
	if err != nil {
		powershell, err = exec.LookPath("powershell.exe")
	}
	if err != nil {
		t.Skip("PowerShell is required for transfer integration tests")
	}
	transferScript, err := os.ReadFile("transfer-recordings.ps1")
	if err != nil {
		t.Fatal(err)
	}
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper, err := os.ReadFile(testExecutable)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"success", "dry-run", "corrupt-download", "existing-different", "chat-not-finalized", "invalid-remote"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := t.TempDir()
			remoteDir := filepath.Join(fixture, "remote")
			destination := filepath.Join(fixture, "library with spaces")
			binDir := filepath.Join(fixture, "bin")
			for _, dir := range []string{remoteDir, destination, binDir} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path string, data []byte, mode os.FileMode) {
				t.Helper()
				if err := os.WriteFile(path, data, mode); err != nil {
					t.Fatal(err)
				}
			}
			for _, command := range []string{"ssh", "scp"} {
				if runtime.GOOS == "windows" {
					command += ".exe"
				}
				write(filepath.Join(binDir, command), helper, 0700)
			}
			name := "sample-2026-10-17.ts"
			chatName := "sample-2026-10-17.chat.jsonl"
			write(filepath.Join(remoteDir, name), []byte("completed media"), 0600)
			if scenario == "chat-not-finalized" {
				write(filepath.Join(remoteDir, chatName+".part"), []byte("pending"), 0600)
			} else {
				write(filepath.Join(remoteDir, chatName), []byte("chat"), 0600)
			}
			if scenario == "existing-different" {
				write(filepath.Join(destination, name), []byte("keep local file"), 0600)
			}
			remote := "root@fixture"
			if scenario == "invalid-remote" {
				remote = "root@fixture;invalid"
			}
			config, _ := json.Marshal(map[string]string{"destination": destination, "remote": remote, "remote_dir": "/root/TwitchRecorder/recordings"})
			write(filepath.Join(fixture, "transfer-config.json"), config, 0600)
			scriptPath := filepath.Join(fixture, "transfer-recordings.ps1")
			write(scriptPath, transferScript, 0600)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			arguments := []string{"-NoProfile", "-NonInteractive", "-File", scriptPath}
			if scenario == "dry-run" {
				arguments = append(arguments, "-DryRun")
			}
			command := exec.CommandContext(ctx, powershell, arguments...)
			command.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "TWITCH_TRANSFER_FIXTURE="+fixture)
			if scenario == "corrupt-download" {
				command.Env = append(command.Env, "TWITCH_TRANSFER_CORRUPT=1")
			}
			output, err := command.CombinedOutput()
			wantSuccess := scenario == "success" || scenario == "dry-run"
			if (err == nil) != wantSuccess {
				t.Fatalf("unexpected result: %v\n%s", err, output)
			}
			_, sourceErr := os.Stat(filepath.Join(remoteDir, name))
			if scenario == "success" {
				if !os.IsNotExist(sourceErr) {
					t.Fatal("verified server copy was not removed")
				}
				for _, file := range []string{name, chatName} {
					data, err := os.ReadFile(filepath.Join(destination, file))
					if err != nil || len(data) == 0 {
						t.Fatalf("download missing: %s (%v)", file, err)
					}
				}
			} else if sourceErr != nil {
				t.Fatalf("server media must be retained: %v", sourceErr)
			}
			calls, _ := os.ReadFile(filepath.Join(fixture, "calls.log"))
			if scenario == "success" {
				chatDelete := strings.Index(string(calls), "delete-verified-recording.sh "+chatName)
				mediaDelete := strings.Index(string(calls), "delete-verified-recording.sh "+name)
				if chatDelete < 0 || mediaDelete <= chatDelete || !strings.Contains(string(calls), "root@fixture") {
					t.Fatal("chat must transfer first and use the configured remote")
				}
			}
			if scenario == "dry-run" && (strings.Contains(string(calls), "scp ") || strings.Contains(string(calls), "delete-verified-recording.sh")) {
				t.Fatal("dry-run copied or removed data")
			}
			if scenario == "invalid-remote" && len(calls) != 0 {
				t.Fatal("invalid remote must fail before any external command")
			}
			if scenario == "existing-different" {
				data, _ := os.ReadFile(filepath.Join(destination, name))
				if string(data) != "keep local file" {
					t.Fatal("existing file was overwritten")
				}
			}
		})
	}
}
