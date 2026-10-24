package serverupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
)

// These tests run without root. Only metadata is substituted: reads, writes,
// renames, symlinks and mode assertions still use ordinary temporary fixtures.
// Production entry points cannot select this traversal.
type canonicalMetadata struct {
	os.FileInfo
	mode os.FileMode
	uid  uint32
}

func (i canonicalMetadata) Mode() os.FileMode { return i.mode }
func (i canonicalMetadata) IsDir() bool       { return i.mode.IsDir() }
func (i canonicalMetadata) Sys() any          { return &syscall.Stat_t{Uid: i.uid} }

func canonicalTraversal(root string) layoutLstat {
	return func(path string) (os.FileInfo, error) {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		mode := info.Mode()
		if path != root && layoutContains(path, root) || path == "/" {
			// /tmp and user-owned ancestors cannot meet production policy. Model
			// protected ancestors explicitly, never chmod the real directories.
			mode = os.ModeDir | 0755
		}
		return canonicalMetadata{FileInfo: info, mode: mode, uid: 0}, nil
	}
}

func canonicalFixture(t *testing.T) (ComposeConfig, []byte, layoutLstat) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	state, deployment := filepath.Join(root, "private"), filepath.Join(root, "deployment")
	for _, dir := range []string{state, deployment} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	input, err := json.Marshal(enrollmentFixture())
	if err != nil {
		t.Fatal(err)
	}
	stat := canonicalTraversal(root)
	if err := enrollDeploymentLayout(state, deployment, input, "ghcr.io/test/repo@sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64), "v1.0.0", "test/repo", Floor{Sequence: 1, SecurityEpoch: 1, MinimumSafeVersion: "v1.0.0", ManifestSHA256: strings.Repeat("c", 64)}, uuid.NewString(), DeploymentLayoutSingleFileV1, stat); err != nil {
		t.Fatal(err)
	}
	var config ComposeConfig
	raw, err := os.ReadFile(filepath.Join(state, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	return config, input, stat
}

func TestSingleFileEnrollmentStagesPrivately(t *testing.T) {
	c, input, stat := canonicalFixture(t)
	if c.DeploymentLayout != DeploymentLayoutSingleFileV1 || c.DeploymentProtocol != 2 || filepath.Base(c.DeploymentFile) != "compose.yaml" {
		t.Fatalf("incorrect single file contract: %+v", c)
	}
	if err := c.validate(stat); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(c.DeploymentFile); !os.IsNotExist(err) {
		t.Fatalf("canonical model published before host handoff: %v", err)
	}
	for name, mode := range map[string]os.FileMode{"staged-compose.yaml": 0600, "config.json": 0600, "original-compose.json": 0600, "control/token": 0444} {
		info, err := os.Lstat(filepath.Join(c.StateDir, name))
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("%s mode: %v %v", name, info, err)
		}
	}
	original, err := os.ReadFile(filepath.Join(c.StateDir, "original-compose.json"))
	if err != nil || !bytes.Equal(original, input) {
		t.Fatal("original snapshot changed")
	}
	candidate, err := os.ReadFile(StagedDeploymentFile(c.StateDir))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(candidate, []byte("\n  \"services\"")) || candidate[len(candidate)-1] != '\n' || bytes.Contains(candidate, []byte(c.Token)) {
		t.Fatal("candidate is not readable or exposes control secret")
	}
	var doc map[string]any
	if err := json.Unmarshal(candidate, &doc); err != nil {
		t.Fatal(err)
	}
	services := doc["services"].(map[string]any)
	server := services["p2pstream"].(map[string]any)
	if server["environment"].(map[string]any)["SECRET"] != "literal-$$HOME-$${SECRET}-$$$$" {
		t.Fatal("dollars changed")
	}
	mounts := services["p2pstream-server-updater"].(map[string]any)["volumes"].([]any)
	expected := map[string]string{c.StateDir: c.StateDir, filepath.Dir(c.DeploymentFile): filepath.Dir(c.DeploymentFile), "/var/run/docker.sock": "/var/run/docker.sock", "server-updater-data": "/server-data"}
	if len(mounts) != len(expected) {
		t.Fatalf("executor mount count: %v", mounts)
	}
	for _, value := range mounts {
		mount := value.(map[string]any)
		source := mount["source"].(string)
		if target, ok := expected[source]; !ok || target != mount["target"] {
			t.Fatalf("unexpected executor authority: %v", mount)
		}
	}
}

func TestSingleFileConfigureAndAtomicDeployment(t *testing.T) {
	c, input, stat := canonicalFixture(t)
	candidate, err := os.ReadFile(StagedDeploymentFile(c.StateDir))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate only the host's publication, not its external runtime handoff.
	if err := AtomicWrite(c.DeploymentFile, candidate, 0640); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(c.DeploymentFile)
	if err != nil {
		t.Fatal(err)
	}
	configured, err := configureModel(c, input, current, stat)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]any
	if err := json.Unmarshal(current, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(configured, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || !bytes.Contains(configured, []byte("\n  \"services\"")) {
		t.Fatal("configuration changed the native model, dollar values or executor mounts")
	}
	// Holding the old file open models why a file bind mount would stay stale.
	old, err := os.Open(c.DeploymentFile)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	oldInfo, _ := old.Stat()
	c.Docker = filepath.Join(t.TempDir(), "nonexistent-docker")
	d := &ComposeDriver{Config: c, runtimeLease: &RuntimeLease{}}
	image := "ghcr.io/test/repo@sha256:" + strings.Repeat("d", 64)
	// A missing executable deliberately stops at the first external command,
	// after the production Deploy path has atomically written the new model.
	if err := d.deploy(context.Background(), image, stat); err == nil {
		t.Fatal("unexpected Docker execution")
	}
	next, err := os.ReadFile(c.DeploymentFile)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(c.DeploymentFile)
	if err != nil || info.Mode().Perm() != 0640 || os.SameFile(oldInfo, info) {
		t.Fatal("canonical model was not atomically replaced at mode 0640")
	}
	if !bytes.Contains(next, []byte(image)) || !bytes.Contains(next, []byte("\n  \"services\"")) {
		t.Fatal("driver did not replace the actual canonical model")
	}
	if err := json.Unmarshal(next, &after); err != nil {
		t.Fatal(err)
	}
	oldServer := before["services"].(map[string]any)["p2pstream"].(map[string]any)
	newServer := after["services"].(map[string]any)["p2pstream"].(map[string]any)
	oldEnv, newEnv := oldServer["environment"].(map[string]any), newServer["environment"].(map[string]any)
	if oldEnv["SERVER_UPDATE_GENERATION"] == newEnv["SERVER_UPDATE_GENERATION"] || oldEnv["SECRET"] != newEnv["SECRET"] {
		t.Fatal("generation did not advance or dollars changed")
	}
	staged, _ := os.ReadFile(StagedDeploymentFile(c.StateDir))
	if !bytes.Equal(staged, candidate) {
		t.Fatal("driver wrote staging instead of canonical model")
	}
}

func TestSingleFileRejectsUnsafePaths(t *testing.T) {
	c, _, stat := canonicalFixture(t)
	for name, mutate := range map[string]func(*ComposeConfig){
		"unknown layout":  func(c *ComposeConfig) { c.DeploymentLayout = "single-file-v2" },
		"legacy protocol": func(c *ComposeConfig) { c.DeploymentProtocol = 0 },
		"wrong basename": func(c *ComposeConfig) {
			c.DeploymentFile = filepath.Join(filepath.Dir(c.DeploymentFile), "compose.json")
		},
		"relative": func(c *ComposeConfig) { c.StateDir = "private" },
		"unclean": func(c *ComposeConfig) {
			c.DeploymentFile = filepath.Dir(c.DeploymentFile) + "/../deployment/compose.yaml"
		},
		"root deployment":       func(c *ComposeConfig) { c.DeploymentFile = "/compose.yaml" },
		"root data":             func(c *ComposeConfig) { c.DataDir = "/" },
		"private in deployment": func(c *ComposeConfig) { c.DeploymentFile = filepath.Join(filepath.Dir(c.StateDir), "compose.yaml") },
		"deployment in private": func(c *ComposeConfig) { c.DeploymentFile = filepath.Join(c.StateDir, "deployment", "compose.yaml") },
		"data in deployment":    func(c *ComposeConfig) { c.DataDir = filepath.Join(filepath.Dir(c.DeploymentFile), "data") },
		"deployment in data":    func(c *ComposeConfig) { c.DataDir = filepath.Dir(filepath.Dir(c.DeploymentFile)) },
		"data in private":       func(c *ComposeConfig) { c.DataDir = filepath.Join(c.StateDir, "data") },
		"private in data":       func(c *ComposeConfig) { c.DataDir = filepath.Dir(c.StateDir) },
		"foreign control":       func(c *ComposeConfig) { c.ControlDir = filepath.Join(filepath.Dir(c.DeploymentFile), "control") },
	} {
		t.Run(name, func(t *testing.T) {
			next := c
			mutate(&next)
			if err := next.validate(stat); err == nil {
				t.Fatal("accepted unsafe layout")
			}
		})
	}
	for _, path := range []string{"/", filepath.Dir(c.StateDir), c.StateDir, filepath.Dir(c.DeploymentFile), c.ControlDir} {
		for _, uid := range []uint32{1000, 65532} {
			bad := func(p string) (os.FileInfo, error) {
				info, err := stat(p)
				if err == nil && p == path {
					return canonicalMetadata{FileInfo: info, mode: info.Mode(), uid: uid}, nil
				}
				return info, err
			}
			if err := c.validate(bad); err == nil {
				t.Fatalf("accepted nonroot owner %d at %s", uid, path)
			}
		}
		bad := func(p string) (os.FileInfo, error) {
			info, err := stat(p)
			if err == nil && p == path {
				return canonicalMetadata{FileInfo: info, mode: info.Mode() | 0020, uid: 0}, nil
			}
			return info, err
		}
		if err := c.validate(bad); err == nil {
			t.Fatalf("accepted writable path %s", path)
		}
	}
}

func TestSingleFileRejectsSymlinksAndDoesNotRepairModes(t *testing.T) {
	for _, target := range []string{"deployment", "private", "control", "canonical", "staged", "token"} {
		t.Run(target, func(t *testing.T) {
			c, _, stat := canonicalFixture(t)
			paths := map[string]string{"deployment": filepath.Dir(c.DeploymentFile), "private": c.StateDir, "control": c.ControlDir, "canonical": c.DeploymentFile, "staged": StagedDeploymentFile(c.StateDir), "token": filepath.Join(c.ControlDir, "token")}
			path := paths[target]
			backing := path + "-backing"
			if _, err := os.Lstat(path); os.IsNotExist(err) {
				if err := os.WriteFile(backing, nil, 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Rename(path, backing); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(backing, path); err != nil {
				t.Fatal(err)
			}
			if err := c.validate(stat); err == nil {
				t.Fatal("accepted symlink")
			}
		})
	}
	c, input, stat := canonicalFixture(t)
	if err := os.Remove(filepath.Join(c.StateDir, "config.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(c.StateDir, 0777); err != nil {
		t.Fatal(err)
	}
	err := enrollDeploymentLayout(c.StateDir, filepath.Dir(c.DeploymentFile), input, "", "", "", "", Floor{}, c.InstanceID, DeploymentLayoutSingleFileV1, stat)
	info, _ := os.Stat(c.StateDir)
	if err == nil || info.Mode().Perm() != 0777 {
		t.Fatal("unsafe state was accepted or chmod repaired")
	}
}

func TestSingleFilePublicEntryPointUsesRealOwnership(t *testing.T) {
	c, input, _ := canonicalFixture(t)
	if os.Geteuid() == 0 {
		t.Skip("nonroot refusal is exercised only with an actual nonroot fixture")
	}
	if err := c.Validate(); err == nil {
		t.Fatal("public validation bypassed real ownership")
	}
	if err := os.Remove(filepath.Join(c.StateDir, "config.json")); err != nil {
		t.Fatal(err)
	}
	if err := EnrollSingleFileInstallation(c.StateDir, filepath.Dir(c.DeploymentFile), input, "", "", "", "", Floor{}, c.InstanceID); err == nil {
		t.Fatal("enrolled nonroot paths")
	}
}

func TestSingleFileRejectsIncludesAndRetainsLegacySerialization(t *testing.T) {
	c, input, stat := canonicalFixture(t)
	current, _ := os.ReadFile(StagedDeploymentFile(c.StateDir))
	for _, key := range []string{"include", "extends"} {
		var doc map[string]any
		if err := json.Unmarshal(input, &doc); err != nil {
			t.Fatal(err)
		}
		if key == "include" {
			doc[key] = []any{"other.yaml"}
		} else {
			doc["services"].(map[string]any)["p2pstream"].(map[string]any)[key] = "other"
		}
		raw, _ := json.Marshal(doc)
		if _, err := configureModel(c, raw, current, stat); err == nil {
			t.Fatal("accepted unresolved model")
		}
	}
	dir, err := enrollFixture(t, enrollmentFixture())
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := os.ReadFile(filepath.Join(dir, "compose.json"))
	if err != nil || bytes.Contains(legacy, []byte("\n")) {
		t.Fatal("legacy serialization changed")
	}
	config, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if bytes.Contains(config, []byte("deployment_layout")) {
		t.Fatal("legacy implicitly selected new layout")
	}
}

func TestSingleFileEnrollmentDoesNotReplaceExistingCanonical(t *testing.T) {
	c, input, stat := canonicalFixture(t)
	original := []byte("existing operator model\n")
	if err := AtomicWrite(c.DeploymentFile, original, 0640); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(c.DeploymentFile)
	if err := os.Remove(filepath.Join(c.StateDir, "config.json")); err != nil {
		t.Fatal(err)
	}
	if err := enrollDeploymentLayout(c.StateDir, filepath.Dir(c.DeploymentFile), input, "ghcr.io/test/repo@sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64), "v1.0.0", "test/repo", c.BootstrapFloor, c.InstanceID, DeploymentLayoutSingleFileV1, stat); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(c.DeploymentFile)
	actual, _ := os.ReadFile(c.DeploymentFile)
	if !os.SameFile(before, after) || !bytes.Equal(actual, original) || after.Mode().Perm() != 0640 {
		t.Fatal("enrollment replaced the existing canonical model")
	}
}

func TestSingleFileDataMountAncestorsAndSymlinks(t *testing.T) {
	c, _, stat := canonicalFixture(t)
	c.DataDir = filepath.Join(filepath.Dir(c.StateDir), "data")
	if err := os.Mkdir(c.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	appOwned := func(path string) (os.FileInfo, error) {
		info, err := stat(path)
		if err == nil && path == c.DataDir {
			return canonicalMetadata{FileInfo: info, mode: info.Mode(), uid: 65532}, nil
		}
		return info, err
	}
	if err := c.validate(appOwned); err != nil {
		t.Fatalf("application-owned named volume rejected: %v", err)
	}
	if err := os.Remove(c.DataDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(c.StateDir, c.DataDir); err != nil {
		t.Fatal(err)
	}
	if err := c.validate(stat); err == nil {
		t.Fatal("accepted data symlink into private state")
	}
}

func stagedRuntimeFixture(t *testing.T) (*ComposeDriver, string, layoutLstat) {
	t.Helper()
	c, _, stat := canonicalFixture(t)
	c.DataDir = filepath.Join(filepath.Dir(c.StateDir), "data")
	if err := os.Mkdir(c.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	// No test may invoke Docker, including if the candidate is otherwise valid.
	c.Docker = filepath.Join(filepath.Dir(c.StateDir), "nonexistent-docker")
	return &ComposeDriver{Config: c}, "ghcr.io/test/repo@sha256:" + strings.Repeat("a", 64), stat
}

func stagedCapability(context.Context, string) (DeploymentCapabilities, error) {
	return DeploymentCapabilities{RuntimeGuard: 2, Version: "v1.0.0", Commit: strings.Repeat("a", 40)}, nil
}

func TestStagedRuntimeCreatesBlockedOwnerWithoutPublicationOrLease(t *testing.T) {
	for _, existingCanonical := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "standalone"}[existingCanonical], func(t *testing.T) {
			d, image, stat := stagedRuntimeFixture(t)
			canonical := []byte("standalone canonical model\n")
			var before os.FileInfo
			if existingCanonical {
				if err := AtomicWrite(d.Config.DeploymentFile, canonical, 0640); err != nil {
					t.Fatal(err)
				}
				before, _ = os.Stat(d.Config.DeploymentFile)
			}
			candidate, _ := os.ReadFile(StagedDeploymentFile(d.Config.StateDir))
			binding, _, err := d.parseRuntimeBinding(candidate)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := AcquireRuntimeLease(context.Background(), d.Config.DataDir)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			probes := 0
			probe := func(ctx context.Context, got string) (DeploymentCapabilities, error) {
				probes++
				if got != image {
					t.Fatalf("probed unbound image %s", got)
				}
				return stagedCapability(ctx, got)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := d.blockStagedRuntime(ctx, d.Config.InstanceID, image, stat, probe); err != nil {
				t.Fatal(err)
			}
			owner, err := ReadRuntimeOwner(d.Config.DataDir)
			if err != nil || owner == nil {
				t.Fatalf("missing blocked owner: %v", err)
			}
			binding.Version, binding.Commit = "v1.0.0", strings.Repeat("a", 40)
			if *owner != binding || owner.Allowed || probes != 1 || d.runtimeLease != nil {
				t.Fatalf("wrong initial owner: %+v", owner)
			}
			if existingCanonical {
				after, _ := os.Stat(d.Config.DeploymentFile)
				actual, _ := os.ReadFile(d.Config.DeploymentFile)
				if !os.SameFile(before, after) || !bytes.Equal(canonical, actual) {
					t.Fatal("canonical model changed")
				}
			} else if _, err := os.Lstat(d.Config.DeploymentFile); !os.IsNotExist(err) {
				t.Fatal("canonical model published")
			}
			actual, _ := os.ReadFile(StagedDeploymentFile(d.Config.StateDir))
			if !bytes.Equal(candidate, actual) {
				t.Fatal("candidate changed")
			}
		})
	}
}

func TestStagedRuntimeReplayPreservesOwnerAndStillChecksImage(t *testing.T) {
	d, image, stat := stagedRuntimeFixture(t)
	old := RuntimeOwner{API: 2, InstanceID: d.Config.InstanceID, Generation: uuid.NewString(), Allowed: true, Version: "v0.9.0", Commit: strings.Repeat("b", 40)}
	if err := WriteRuntimeOwner(d.Config.DataDir, old); err != nil {
		t.Fatal(err)
	}
	noProbe := func(context.Context, string) (DeploymentCapabilities, error) {
		t.Fatal("replay probed image")
		return DeploymentCapabilities{}, nil
	}
	if err := d.blockStagedRuntime(context.Background(), d.Config.InstanceID, "ghcr.io/test/repo@sha256:"+strings.Repeat("c", 64), stat, noProbe); err == nil {
		t.Fatal("replay accepted wrong image")
	}
	unchanged, _ := ReadRuntimeOwner(d.Config.DataDir)
	if unchanged == nil || *unchanged != old {
		t.Fatal("failed replay changed owner")
	}
	for i := 0; i < 2; i++ {
		if err := d.blockStagedRuntime(context.Background(), d.Config.InstanceID, image, stat, noProbe); err != nil {
			t.Fatal(err)
		}
		owner, _ := ReadRuntimeOwner(d.Config.DataDir)
		old.Allowed = false
		if owner == nil || *owner != old {
			t.Fatalf("replay replaced identity: %+v", owner)
		}
	}
}

func TestStagedRuntimeRefusesForeignOwnerAndProbeFailure(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		d, image, stat := stagedRuntimeFixture(t)
		var old RuntimeOwner
		if foreign {
			old = RuntimeOwner{API: 2, InstanceID: uuid.NewString(), Generation: uuid.NewString(), Allowed: true, Version: "v1.0.0", Commit: strings.Repeat("a", 40)}
			if err := WriteRuntimeOwner(d.Config.DataDir, old); err != nil {
				t.Fatal(err)
			}
		}
		probe := func(context.Context, string) (DeploymentCapabilities, error) {
			if foreign {
				t.Fatal("probed image for foreign owner")
			}
			return DeploymentCapabilities{}, errors.New("fixture image unavailable")
		}
		if err := d.blockStagedRuntime(context.Background(), d.Config.InstanceID, image, stat, probe); err == nil {
			t.Fatal("unsafe block accepted")
		}
		owner, err := ReadRuntimeOwner(d.Config.DataDir)
		if err != nil || foreign && (owner == nil || *owner != old) || !foreign && owner != nil {
			t.Fatal("failed block changed owner")
		}
	}
}

func TestStagedRuntimeRejectsInvalidCandidateBeforeReplay(t *testing.T) {
	for _, kind := range []string{"absent", "malformed", "oversize", "symlink", "hardlink", "directory", "public-mode", "wrong-project", "wrong-instance", "wrong-generation", "wrong-protocol", "wrong-token", "wrong-token-file", "wrong-socket", "wrong-gate", "wrong-image"} {
		t.Run(kind, func(t *testing.T) {
			d, image, stat := stagedRuntimeFixture(t)
			old := RuntimeOwner{API: 2, InstanceID: d.Config.InstanceID, Generation: uuid.NewString(), Allowed: true, Version: "v1.0.0", Commit: strings.Repeat("b", 40)}
			if err := WriteRuntimeOwner(d.Config.DataDir, old); err != nil {
				t.Fatal(err)
			}
			path := StagedDeploymentFile(d.Config.StateDir)
			raw, _ := os.ReadFile(path)
			var doc map[string]any
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			server := doc["services"].(map[string]any)["p2pstream"].(map[string]any)
			env := server["environment"].(map[string]any)
			switch kind {
			case "absent", "symlink", "directory":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					if err := os.Symlink(filepath.Join(d.Config.StateDir, "original-compose.json"), path); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "directory" {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				}
			case "hardlink":
				if err := os.Link(path, path+"-linked"); err != nil {
					t.Fatal(err)
				}
			case "public-mode":
				if err := os.Chmod(path, 0640); err != nil {
					t.Fatal(err)
				}
			case "malformed", "oversize":
				content := []byte("{")
				if kind == "oversize" {
					content = bytes.Repeat([]byte(" "), (2<<20)+1)
				}
				if err := AtomicWrite(path, content, 0600); err != nil {
					t.Fatal(err)
				}
			default:
				switch kind {
				case "wrong-project":
					doc["name"] = "foreign"
				case "wrong-instance":
					env["SERVER_UPDATE_INSTANCE_ID"] = uuid.NewString()
				case "wrong-generation":
					env["SERVER_UPDATE_GENERATION"] = "invalid"
				case "wrong-protocol":
					env["SERVER_UPDATE_RUNTIME_PROTOCOL"] = "0"
				case "wrong-token":
					env["SERVER_UPDATE_TOKEN"] = "legacy"
				case "wrong-token-file":
					env["SERVER_UPDATE_TOKEN_FILE"] = "/foreign/token"
				case "wrong-socket":
					env["SERVER_UPDATE_SOCKET"] = "/foreign/socket"
				case "wrong-gate":
					env["SERVER_UPDATE_GATE_FILE"] = "/foreign/gate"
				case "wrong-image":
					server["image"] = "ghcr.io/test/repo:latest"
				}
				changed, _ := json.Marshal(doc)
				if err := AtomicWrite(path, changed, 0600); err != nil {
					t.Fatal(err)
				}
			}
			noProbe := func(context.Context, string) (DeploymentCapabilities, error) {
				t.Fatal("invalid candidate probed")
				return DeploymentCapabilities{}, nil
			}
			if err := d.blockStagedRuntime(context.Background(), d.Config.InstanceID, image, stat, noProbe); err == nil {
				t.Fatal("invalid candidate accepted")
			}
			owner, _ := ReadRuntimeOwner(d.Config.DataDir)
			if owner == nil || *owner != old {
				t.Fatal("invalid candidate changed existing owner")
			}
		})
	}
}

func TestStagedRuntimeRequiresExplicitLayoutAndExpectedInstallation(t *testing.T) {
	for _, kind := range []string{"legacy-layout", "legacy-protocol", "wrong-expected-instance", "invalid-instance"} {
		t.Run(kind, func(t *testing.T) {
			d, image, stat := stagedRuntimeFixture(t)
			instance := d.Config.InstanceID
			switch kind {
			case "legacy-layout":
				d.Config.DeploymentLayout = ""
			case "legacy-protocol":
				d.Config.DeploymentProtocol = 0
			case "wrong-expected-instance":
				instance = uuid.NewString()
			case "invalid-instance":
				instance, d.Config.InstanceID = "invalid", "invalid"
			}
			if err := d.blockStagedRuntime(context.Background(), instance, image, stat, stagedCapability); err == nil {
				t.Fatal("invalid staged block accepted")
			}
			if owner, err := ReadRuntimeOwner(d.Config.DataDir); err != nil || owner != nil {
				t.Fatal("owner created on rejection")
			}
		})
	}
	d, image, _ := stagedRuntimeFixture(t)
	if os.Geteuid() != 0 {
		if err := d.BlockStagedRuntime(context.Background(), d.Config.InstanceID, image); err == nil {
			t.Fatal("public entry point bypassed real ownership")
		}
	}
}

func TestStagedRuntimeLeavesLegacyBlockSemanticsUnchanged(t *testing.T) {
	d, _, _ := stagedRuntimeFixture(t)
	old := RuntimeOwner{API: 2, InstanceID: d.Config.InstanceID, Generation: uuid.NewString(), Allowed: true, Version: "v1.0.0", Commit: strings.Repeat("b", 40)}
	if err := WriteRuntimeOwner(d.Config.DataDir, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(StagedDeploymentFile(d.Config.StateDir)); err != nil {
		t.Fatal(err)
	}
	// Legacy BlockRuntime needs neither canonical nor staged model on replay.
	if err := d.BlockRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	owner, _ := ReadRuntimeOwner(d.Config.DataDir)
	old.Allowed = false
	if owner == nil || *owner != old {
		t.Fatal("legacy replay semantics changed")
	}
	d.Config.DeploymentProtocol = 0
	d.Config.DataDir = "/nonexistent-data"
	if err := d.BlockRuntime(context.Background()); err != nil {
		t.Fatal("protocol zero no longer no-ops")
	}
}

func TestCanonicalComposeCapabilityIsAdditive(t *testing.T) {
	legacy := DeploymentCapabilities{RuntimeGuard: 2, Version: "v1.0.0", Commit: strings.Repeat("a", 40)}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("canonical_compose")) || bytes.Contains(raw, []byte("enrollment_preflight")) {
		t.Fatal("legacy capability payload expanded")
	}
	legacy.CanonicalCompose = 1
	raw, err = json.Marshal(legacy)
	if err != nil || !bytes.Contains(raw, []byte(`"canonical_compose":1`)) {
		t.Fatal("canonical capability missing")
	}
}
