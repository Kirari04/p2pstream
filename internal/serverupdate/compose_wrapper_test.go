package serverupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostComposeWrapperPinsToolsAndPreservesArguments(t *testing.T) {
	directory := t.TempDir()
	docker := filepath.Join(directory, "docker")
	script := `#!/bin/bash
set -eu
if [[ "$1" == compose ]]; then
 printf '%s\n' "$P2PSTREAM_WRAPPER_TEST_IMAGE"
else
 printf '%s\0' "$@" > "$P2PSTREAM_WRAPPER_TEST_LOG"
fi
`
	if err := os.WriteFile(docker, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	wrapper, err := filepath.Abs("../../scripts/server-updater-compose.sh")
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(directory, "args")
	for _, image := range []string{"sha256:" + strings.Repeat("a", 64), "mutable:latest"} {
		_ = os.Remove(log)
		command := exec.Command("bash", wrapper, "ps", "literal argument with spaces", "$(not-a-command)")
		command.Env = append(os.Environ(), "PATH="+directory+":"+os.Getenv("PATH"), "P2PSTREAM_WRAPPER_TEST_IMAGE="+image, "P2PSTREAM_WRAPPER_TEST_LOG="+log)
		output, err := command.CombinedOutput()
		if image == "mutable:latest" {
			if err == nil {
				t.Fatal("accepted mutable updater image")
			}
			if _, err := os.Stat(log); !os.IsNotExist(err) {
				t.Fatal("started unpinned tooling")
			}
			continue
		}
		if err != nil {
			t.Fatalf("wrapper: %v %s", err, output)
		}
		data, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		args := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
		if args[0] != "run" || !strings.Contains(string(data), image+"\x00compose\x00") {
			t.Fatalf("not pinned to enrolled tooling: %q", args)
		}
		got := args[len(args)-3:]
		if got[0] != "ps" || got[1] != "literal argument with spaces" || got[2] != "$(not-a-command)" {
			t.Fatalf("host arguments changed: %q", got)
		}
	}
}
