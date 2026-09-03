package updater

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"p2pstream/internal/agentupdate"
	"p2pstream/internal/agentupdateauth"
	"p2pstream/internal/buildinfo"
)

func TestEnsureIdentityUsesCanonicalSeedAndDoesNotRotate(t *testing.T) {
	dir := t.TempDir()
	privatePath := filepath.Join(dir, "identity.key")
	publicPath := filepath.Join(dir, "identity.pub")
	uid, gid := os.Geteuid(), os.Getegid()
	first, err := ensureIdentity(privatePath, publicPath, uid, gid)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ensureIdentity(privatePath, publicPath, uid, gid)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("identity rotated during idempotent bootstrap")
	}
	encoded, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agentupdate.ParsePrivateKey(string(encoded[:len(encoded)-1])); err != nil {
		t.Fatalf("private key is not canonical seed encoding: %v", err)
	}
	info, err := os.Stat(privatePath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private key mode = %v, %v", info.Mode().Perm(), err)
	}
}

func TestParseAccountIDRejectsValuesThatDoNotFitTheHostInt(t *testing.T) {
	for _, value := range []string{"-1", "4294967296", "not-an-id"} {
		if _, err := parseAccountID(value); err == nil {
			t.Fatalf("parseAccountID(%q) unexpectedly succeeded", value)
		}
	}

	if got, err := parseAccountID("4294967295"); strconv.IntSize == 64 {
		if err != nil || uint64(got) != uint64(^uint32(0)) {
			t.Fatalf("parseAccountID(max uint32) = %d, %v", got, err)
		}
	} else if err == nil {
		t.Fatal("max uint32 unexpectedly fit a 32-bit int")
	}
}

func TestBootstrapCannotLowerFloor(t *testing.T) {
	root := t.TempDir()
	paths := Paths{
		ConfigPath: filepath.Join(root, "etc", "updater.json"),
		StateDir:   filepath.Join(root, "state"), InstallRoot: filepath.Join(root, "install"), CommandPath: filepath.Join(root, "bin", "p2pstream"),
	}
	if err := os.MkdirAll(paths.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	uid, gid := os.Geteuid(), os.Getegid()
	if err := pinBootstrapState(paths, "v1.4.0", true, uid, gid); err != nil {
		t.Fatal(err)
	}
	wantFloor := Floor{Version: "v1.4.0", Sequence: 9, SecurityEpoch: 4, MinimumSafeVersion: "v1.3.0"}
	if err := atomicJSON(paths.floorPath(), wantFloor, 0640); err != nil {
		t.Fatal(err)
	}
	if err := pinBootstrapState(paths, "v1.2.0", true, uid, gid); err != nil {
		t.Fatal(err)
	}
	gotFloor, err := loadFloor(paths.floorPath())
	if err != nil || gotFloor != wantFloor {
		t.Fatalf("floor lowered on re-bootstrap: %+v, %v", gotFloor, err)
	}
}

func TestBootstrapVersionFloorIncludesNewerExistingTunnel(t *testing.T) {
	for _, test := range []struct {
		name, rescue, tunnel, want string
	}{
		{name: "newer tunnel", rescue: "v1.5.0", tunnel: "v2.0.0", want: "v2.0.0"},
		{name: "newer rescue", rescue: "v2.1.0", tunnel: "v2.0.0", want: "v2.1.0"},
		{name: "new install", rescue: "v1.5.0", want: "v1.5.0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := bootstrapVersionFloor(test.rescue, test.tunnel); got != test.want {
				t.Fatalf("bootstrap floor = %s, want %s", got, test.want)
			}
		})
	}

	root := t.TempDir()
	paths := Paths{
		ConfigPath: filepath.Join(root, "etc", "updater.json"),
		StateDir:   filepath.Join(root, "state"), InstallRoot: filepath.Join(root, "install"), CommandPath: filepath.Join(root, "bin", "p2pstream"),
	}
	if err := os.MkdirAll(paths.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	uid, gid := os.Geteuid(), os.Getegid()
	if err := pinBootstrapState(paths, bootstrapVersionFloor("v1.5.0", "v2.0.0"), true, uid, gid); err != nil {
		t.Fatal(err)
	}
	floor, err := loadFloor(paths.floorPath())
	if err != nil || floor.Version != "v2.0.0" {
		t.Fatalf("persisted bootstrap floor = %+v, %v", floor, err)
	}
}

func TestBootstrapCannotRepointPinnedManagementOriginOrRepository(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "updater.json")
	uid, gid := os.Geteuid(), os.Getegid()
	original := HostConfig{Repository: "owner/repo", ManagementOrigin: "https://management.example", AgentPublicID: "agent-a", Channel: "stable"}
	if err := pinHostConfig(path, original, uid, gid); err != nil {
		t.Fatal(err)
	}
	changed := original
	changed.ManagementOrigin = "https://attacker.example"
	if err := pinHostConfig(path, changed, uid, gid); err == nil {
		t.Fatal("bootstrap repointed the pinned management origin")
	}
	got, err := LoadHostConfig(path)
	if err != nil || got != original {
		t.Fatalf("pinned config changed: %+v, %v", got, err)
	}
	if err := os.Chmod(path, 0660); err != nil {
		t.Fatal(err)
	}
	if err := pinHostConfig(path, original, uid, gid); err == nil {
		t.Fatal("bootstrap accepted relaxed pinned-config permissions")
	}
}

func TestBootstrapCannotReplacePinnedManagementAuthority(t *testing.T) {
	root := t.TempDir()
	paths := Paths{ConfigPath: filepath.Join(root, "updater.json")}
	firstPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	secondPublic, _, _ := ed25519.GenerateKey(rand.Reader)
	firstID, _ := agentupdateauth.KeyID(firstPublic)
	secondID, _ := agentupdateauth.KeyID(secondPublic)
	uid, gid := os.Geteuid(), os.Getegid()
	if err := pinManagementAuthority(paths, firstPublic, firstID, 7, uid, gid); err != nil {
		t.Fatal(err)
	}
	if err := pinManagementAuthority(paths, secondPublic, secondID, 8, uid, gid); err == nil {
		t.Fatal("bootstrap replaced the pinned management authority")
	}
	if err := pinManagementAuthority(paths, firstPublic, secondID, 7, uid, gid); err == nil {
		t.Fatal("bootstrap accepted a key ID that did not match the public key")
	}
	pinned, publicKey, err := loadManagementAuthority(paths)
	if err != nil || pinned.KeyID != firstID || pinned.Epoch != 7 || !firstPublic.Equal(publicKey) {
		t.Fatalf("pinned authority changed: %+v, %v", pinned, err)
	}
}

func TestBootstrapSlotMetadataBindsInstalledContentIdentity(t *testing.T) {
	previousVersion, previousCommit := buildinfo.Version, buildinfo.Commit
	buildinfo.Version, buildinfo.Commit = "v1.2.3", strings.Repeat("d", 40)
	t.Cleanup(func() { buildinfo.Version, buildinfo.Commit = previousVersion, previousCommit })
	root := t.TempDir()
	paths := Paths{StateDir: filepath.Join(root, "state"), InstallRoot: filepath.Join(root, "install")}
	if err := os.MkdirAll(paths.rootStateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte("bootstrap agent binary")
	digest := sha256.Sum256(body)
	version := "bootstrap-" + fmt.Sprintf("%x", digest)[:16]
	bootstrapSlot(t, paths, version, body)
	if err := pinBootstrapSlotMetadata(paths, buildinfo.Version, buildinfo.Commit); err != nil {
		t.Fatal(err)
	}
	data, err := readRegularNoFollow(paths.currentSlotMetadataPath(), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	var metadata slotMetadata
	if err := strictJSON(data, &metadata); err != nil || metadata.Version != version || metadata.BuildVersion != "v1.2.3" || metadata.BuildCommit != strings.Repeat("d", 40) || metadata.ArtifactSHA256 != fmt.Sprintf("%x", digest) {
		t.Fatalf("bootstrap metadata = %+v, %v", metadata, err)
	}
	if err := os.WriteFile(filepath.Join(paths.InstallRoot, filepath.FromSlash(metadata.Target)), []byte("changed"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := pinBootstrapSlotMetadata(paths, buildinfo.Version, buildinfo.Commit); err == nil {
		t.Fatal("bootstrap metadata accepted content that no longer matched its slot identity")
	}
}

func TestEnsureIdentityRejectsRelaxedPrivateKeyMode(t *testing.T) {
	dir := t.TempDir()
	privatePath := filepath.Join(dir, "identity.key")
	publicPath := filepath.Join(dir, "identity.pub")
	uid, gid := os.Geteuid(), os.Getegid()
	if _, err := ensureIdentity(privatePath, publicPath, uid, gid); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(privatePath, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := ensureIdentity(privatePath, publicPath, uid, gid); err == nil {
		t.Fatal("relaxed private key permissions were accepted")
	}
}
