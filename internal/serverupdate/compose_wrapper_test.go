package serverupdate

import (
	"os/exec"
	"strings"
	"testing"
)

func TestHostComposeWrapperBlocksUnvalidatedMutations(t *testing.T) {
	for _, action := range []string{"up", "run", "build", "down", "restart", "stop", "rm"} {
		cmd := exec.Command("bash", "../../scripts/server-updater-compose.sh", action, "p2pstream")
		out, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(out), "manage") {
			t.Fatalf("wrapper admitted %s: %s", action, out)
		}
	}
}
