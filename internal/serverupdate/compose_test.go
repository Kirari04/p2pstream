package serverupdate

import (
	"strings"
	"testing"
)

func TestRuntimeCannotForgeInstalledIdentityToAuthorizeDowngrade(t *testing.T) {
	driver := &ComposeDriver{Config: ComposeConfig{Channel: "stable", Repository: "test/repo"}}
	labels := map[string]string{"org.opencontainers.image.version": "v1.2.0", "org.opencontainers.image.revision": strings.Repeat("a", 40), "org.opencontainers.image.source": "https://github.com/test/repo"}
	status := RuntimeStatus{Version: "v1.2.0", Commit: labels["org.opencontainers.image.revision"]}
	if err := driver.validateRuntimeIdentity(status, labels); err != nil {
		t.Fatal(err)
	}
	status.Version = "v1.0.0"
	if err := driver.validateRuntimeIdentity(status, labels); err == nil {
		t.Fatal("runtime could lie about its version to select an older image")
	}
	status.Version = "v1.2.0"
	status.Commit = strings.Repeat("b", 40)
	if err := driver.validateRuntimeIdentity(status, labels); err == nil {
		t.Fatal("runtime could lie about installed commit")
	}
	status.Commit = labels["org.opencontainers.image.revision"]
	labels["org.opencontainers.image.source"] = "https://github.com/attacker/repo"
	if err := driver.validateRuntimeIdentity(status, labels); err == nil {
		t.Fatal("image from an unrelated repository accepted")
	}
}
