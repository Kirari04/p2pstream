package serverupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"p2pstream/internal/agentupdate"
)

const InstallationAsset = "p2pstream_install.json"
const IdentityFile = "server-installation-id"

// InstallationIdentity exists before enrollment and survives container recreation.
// A copied data volume is intentionally the same installation; operators must not
// run cloned writable installations concurrently.
func InstallationIdentity(directory string, create bool) (string, error) {
	path := filepath.Join(directory, IdentityFile)
	if create {
		lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
		if err != nil {
			return "", err
		}
		defer lock.Close()
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
			return "", err
		}
		defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	}
	data, err := readProtected(path, 128)
	if errors.Is(err, os.ErrNotExist) && create {
		id := uuid.NewString()
		return id, AtomicWrite(path, []byte(id+"\n"), 0600)
	}
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if _, err := uuid.Parse(id); err != nil {
		return "", errors.New("invalid persisted installation identity")
	}
	return id, nil
}

// Installation is a hashed attachment of the canonical release manifest. Image
// references are platform digests, never mutable tags or arbitrary registries.
type Installation struct {
	API           int               `json:"api"`
	Version       string            `json:"version"`
	Commit        string            `json:"commit"`
	Channel       string            `json:"channel"`
	Bundle        string            `json:"bundle"`
	UpdaterImages map[string]string `json:"updater_images"`
}

func (i Installation) Validate(repository string) error {
	if i.API != 1 || !validVersion(i.Version, i.Channel) || !commitPattern.MatchString(i.Commit) || i.Bundle != "p2pstream_"+i.Version+"_docker.tar.gz" || len(i.UpdaterImages) != 2 {
		return errors.New("unsupported installation descriptor")
	}
	for _, arch := range []string{"amd64", "arm64"} {
		ref := i.UpdaterImages["linux/"+arch]
		if !strings.HasPrefix(ref, "ghcr.io/"+strings.ToLower(repository)+"-updater@sha256:") || !digestPattern.MatchString(strings.TrimPrefix(ref, "ghcr.io/"+strings.ToLower(repository)+"-updater@sha256:")) {
			return errors.New("invalid updater image binding")
		}
	}
	return nil
}

type InstallationRecipe struct {
	Descriptor     Installation
	Bundle         agentupdate.ReleaseAsset
	ManifestSHA256 string
	ExpiresAt      time.Time
}

func verifyInstallation(manifest, descriptor []byte, repository, version, commit, channel string) (InstallationRecipe, error) {
	m, err := agentupdate.ParseManifest(manifest)
	if err != nil {
		return InstallationRecipe{}, err
	}
	asset, err := m.ReleaseAssetFor(InstallationAsset)
	if err != nil {
		return InstallationRecipe{}, errors.New("this release has no Docker installation bundle; first manually deploy a newer published release with installation support")
	}
	if asset.Size != uint64(len(descriptor)) || asset.SHA256 != hash(descriptor) {
		return InstallationRecipe{}, errors.New("installation descriptor digest mismatch")
	}
	var i Installation
	if err := decode(descriptor, &i); err != nil {
		return InstallationRecipe{}, err
	}
	canonical, _ := json.Marshal(i)
	if !bytes.Equal(canonical, descriptor) {
		return InstallationRecipe{}, errors.New("installation descriptor is not canonical")
	}
	if err := i.Validate(repository); err != nil {
		return InstallationRecipe{}, err
	}
	if i.Version != version || i.Commit != commit || i.Channel != channel || m.Version != version || m.Commit != commit || m.Channel != channel {
		return InstallationRecipe{}, errors.New("installation release identity mismatch")
	}
	bundle, err := m.ReleaseAssetFor(i.Bundle)
	if err != nil {
		return InstallationRecipe{}, err
	}
	if bundle.Size > 1<<20 {
		return InstallationRecipe{}, errors.New("installation bundle exceeds size limit")
	}
	expires, err := time.Parse(time.RFC3339, m.ExpiresAt)
	if err != nil {
		return InstallationRecipe{}, err
	}
	return InstallationRecipe{Descriptor: i, Bundle: bundle, ManifestSHA256: hash(manifest), ExpiresAt: expires}, nil
}
func (s *GitHubSource) Installation(ctx context.Context, current RuntimeStatus) (InstallationRecipe, error) {
	manifest, metadata, err := s.releaseInputs(ctx, current.Version)
	if err != nil {
		return InstallationRecipe{}, err
	}
	if _, err = verifyRelease(manifest, metadata, "ghcr.io/"+strings.ToLower(s.Repository), s.Channel, current.Version, s.Arch, current, Floor{}, time.Now(), true); err != nil {
		return InstallationRecipe{}, err
	}
	base := s.download + "/" + s.Repository + "/releases/download/" + current.Version + "/"
	descriptor, err := s.fetch(ctx, base+InstallationAsset, 16<<10)
	if err != nil {
		return InstallationRecipe{}, errors.New("Docker installation assets are unavailable for this release; manually deploy a newer published release containing them, or retry when GitHub is reachable")
	}
	return verifyInstallation(manifest, descriptor, s.Repository, current.Version, current.Commit, s.Channel)
}

// SetupCommand contains only publisher-verified identity and a local data ID.
// Compose options are supplied by the UI as individually shell-quoted arguments.
func (s *GitHubSource) SetupCommand(recipe InstallationRecipe, id string) (string, error) {
	if _, err := uuid.Parse(id); err != nil {
		return "", err
	}
	if err := recipe.Descriptor.Validate(s.Repository); err != nil {
		return "", err
	}
	if !digestPattern.MatchString(recipe.Bundle.SHA256) || !digestPattern.MatchString(recipe.ManifestSHA256) || recipe.Bundle.Name != recipe.Descriptor.Bundle {
		return "", errors.New("invalid recipe")
	}
	return fmt.Sprintf(`(
  set -eu
  work=$(mktemp -d)
  trap 'rm -rf -- "$work"' EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM HUP
  curl --proto '=https' --proto-redir '=https' --tlsv1.2 --fail --show-error --location --connect-timeout 15 --max-time 120 --max-filesize 1048576 --output "$work/install.tar.gz" '%s/%s/releases/download/%s/%s'
  printf '%%s  %%s\n' '%s' "$work/install.tar.gz" | sha256sum --check --status
  python3 -I -c 'import sys,tarfile; t=tarfile.open(sys.argv[1]); names={"server-updater-host.py","install-server-updater.sh","server-updater-compose.sh","compose.yaml",".env.example"}; members=t.getmembers(); assert len(members)==len(names) and {m.name for m in members}==names and all(m.isfile() and m.size<=262144 for m in members); t.extractall(sys.argv[2],members=members)' "$work/install.tar.gz" "$work"
  sudo python3 -I "$work/server-updater-host.py" install --bundle "$work" --expect-installation '%s' --expect-version '%s' --expect-commit '%s' --expect-channel '%s' --expect-arch '%s' --manifest-sha256 '%s' --repository '%s' --COMPOSE_OPTIONS--
)`, s.download, s.Repository, recipe.Descriptor.Version, recipe.Bundle.Name, recipe.Bundle.SHA256, id, recipe.Descriptor.Version, recipe.Descriptor.Commit, recipe.Descriptor.Channel, s.Arch, recipe.ManifestSHA256, s.Repository), nil
}

// VerifyEnrollmentInputs permits offline resume using protected, previously
// downloaded assets. The host bootstrap binds these bytes to its pinned
// GitHub manifest before starting this exact release executor image.
func VerifyEnrollmentInputs(directory, repository, channel, arch, image, updater string, current RuntimeStatus) (Floor, error) {
	manifest, err := readProtected(filepath.Join(directory, "p2pstream_agent_update_manifest.json"), 64<<10)
	if err != nil {
		return Floor{}, err
	}
	metadata, err := readProtected(filepath.Join(directory, MetadataAsset), 16<<10)
	if err != nil {
		return Floor{}, err
	}
	descriptor, err := readProtected(filepath.Join(directory, InstallationAsset), 16<<10)
	if err != nil {
		return Floor{}, err
	}
	release, err := verifyRelease(manifest, metadata, "ghcr.io/"+strings.ToLower(repository), channel, current.Version, arch, current, Floor{}, time.Now(), true)
	if err != nil {
		return Floor{}, err
	}
	recipe, err := verifyInstallation(manifest, descriptor, repository, current.Version, current.Commit, channel)
	if err != nil {
		return Floor{}, err
	}
	if release.Image != image || release.Commit != current.Commit || recipe.Descriptor.UpdaterImages["linux/"+arch] != updater {
		return Floor{}, errors.New("enrollment image differs from published installation identity")
	}
	return release.VerificationFloor(), nil
}
