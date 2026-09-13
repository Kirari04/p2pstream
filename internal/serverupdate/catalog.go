package serverupdate

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"p2pstream/internal/releaseversion"
)

type GitHubSource struct {
	Repository, Channel, Arch string
	client                    *http.Client
	api, download             string
}

func NewGitHubSource(repository, channel, arch string) (*GitHubSource, error) {
	if !regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*/[A-Za-z0-9_-][A-Za-z0-9_.-]*$`).MatchString(repository) ||
		(channel != "stable" && channel != "staging") || (arch != "amd64" && arch != "arm64") {
		return nil, errors.New("unsupported update repository, channel or platform")
	}
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	client := &http.Client{Transport: t, Timeout: 20 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 || r.URL.Scheme != "https" || r.URL.User != nil || r.URL.Port() != "" {
			return errors.New("untrusted update redirect")
		}
		switch r.URL.Hostname() {
		case "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
			return nil
		}
		return errors.New("untrusted update redirect host")
	}}
	return &GitHubSource{Repository: repository, Channel: channel, Arch: arch, client: client, api: "https://api.github.com", download: "https://github.com"}, nil
}

type githubRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

func (s *GitHubSource) Latest(ctx context.Context, current RuntimeStatus, floor Floor) (*Release, error) {
	var candidates []string
	// Bound discovery to recent published releases. Never infer the next tag or
	// use the running server's tag (the agent catalog intentionally does that).
	for page := 1; page <= 5; page++ {
		data, err := s.fetch(ctx, fmt.Sprintf("%s/repos/%s/releases?per_page=100&page=%d", s.api, s.Repository, page), 2<<20)
		if err != nil {
			return nil, err
		}
		var releases []githubRelease
		if err := json.Unmarshal(data, &releases); err != nil {
			return nil, err
		}
		for _, r := range releases {
			if !r.Draft && r.Prerelease == (s.Channel == "staging") && validVersion(r.Tag, s.Channel) && releaseversion.Compare(r.Tag, current.Version) > 0 {
				candidates = append(candidates, r.Tag)
			}
		}
		if len(releases) < 100 {
			break
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sort.Slice(candidates, func(i, j int) bool { return releaseversion.Compare(candidates[i], candidates[j]) > 0 })
	// Fail closed on an incompatible newest release; do not silently choose an
	// older release after a verification failure.
	r, err := s.Resolve(ctx, candidates[0], current, floor)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (s *GitHubSource) Resolve(ctx context.Context, version string, current RuntimeStatus, floor Floor) (Release, error) {
	return s.resolve(ctx, version, current, floor, false)
}

// Installed is used only by the trusted host enrollment command to seed the
// anti-rollback floor from the image that is already installed.
func (s *GitHubSource) Installed(ctx context.Context, image string, current RuntimeStatus) (Release, error) {
	r, err := s.resolve(ctx, current.Version, current, Floor{}, true)
	if err != nil {
		return Release{}, err
	}
	if r.Image != image || r.Commit != current.Commit {
		return Release{}, errors.New("installed image differs from its canonical release identity")
	}
	return r, nil
}

func (s *GitHubSource) resolve(ctx context.Context, version string, current RuntimeStatus, floor Floor, installed bool) (Release, error) {
	if !validVersion(version, s.Channel) {
		return Release{}, errors.New("invalid release version")
	}
	data, err := s.fetch(ctx, s.api+"/repos/"+s.Repository+"/releases/tags/"+url.PathEscape(version), 256<<10)
	if err != nil {
		return Release{}, err
	}
	var r githubRelease
	if err := json.Unmarshal(data, &r); err != nil {
		return Release{}, err
	}
	if r.Tag != version || r.Draft || r.Prerelease != (s.Channel == "staging") {
		return Release{}, errors.New("release channel mismatch or draft release")
	}
	base := s.download + "/" + s.Repository + "/releases/download/" + version + "/"
	manifest, err := s.fetch(ctx, base+"p2pstream_agent_update_manifest.json", 64<<10)
	if err != nil {
		return Release{}, err
	}
	metadata, err := s.fetch(ctx, base+MetadataAsset, 16<<10)
	if err != nil {
		return Release{}, err
	}
	return verifyRelease(manifest, metadata, "ghcr.io/"+strings.ToLower(s.Repository), s.Channel, version, s.Arch, current, floor, time.Now(), installed)
}

func (s *GitHubSource) fetch(ctx context.Context, address string, limit int64) ([]byte, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Accept", "application/octet-stream")
	r.Header.Set("User-Agent", "p2pstream-server-updater/1")
	if strings.HasPrefix(address, s.api+"/") {
		r.Header.Set("Accept", "application/vnd.github+json")
	}
	resp, err := s.client.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release source returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("release response exceeds size limit")
	}
	return data, nil
}
