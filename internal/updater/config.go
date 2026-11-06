package updater

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}/[A-Za-z0-9_.-]{1,64}$`)

type HostConfig struct {
	Repository       string `json:"repository"`
	ManagementOrigin string `json:"management_origin"`
	AgentPublicID    string `json:"agent_public_id"`
	Channel          string `json:"channel"`
}

func (c HostConfig) Validate() error {
	if !repositoryPattern.MatchString(c.Repository) {
		return errors.New("updater repository must be a fixed GitHub owner/repository")
	}
	if !agentIDPattern.MatchString(c.AgentPublicID) {
		return errors.New("updater agent public ID is invalid")
	}
	origin, err := url.Parse(c.ManagementOrigin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil ||
		origin.Path != "" || origin.RawPath != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return errors.New("updater management origin must be an HTTPS origin without credentials, path, query, or fragment")
	}
	if c.Channel != "stable" && c.Channel != "staging" {
		return errors.New("managed update channel must be stable or staging")
	}
	return nil
}

func LoadHostConfig(configPath string) (HostConfig, error) {
	data, err := readRegularNoFollow(configPath, 64<<10)
	if err != nil {
		return HostConfig{}, err
	}
	var config HostConfig
	if err := strictJSON(data, &config); err != nil {
		return HostConfig{}, err
	}
	if err := config.Validate(); err != nil {
		return HostConfig{}, err
	}
	return config, nil
}

// ArtifactURL constructs the only release download URL accepted by the host:
// an exact GitHub release version and verified safe raw-binary asset name.
func (c HostConfig) ArtifactURL(release VerifiedRelease) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	if err := validateRelease(release); err != nil {
		return "", err
	}
	if strings.ContainsAny(release.Version+release.Artifact.Name, "/\\") {
		return "", errors.New("release URL components contain a path separator")
	}
	endpoint := &url.URL{
		Scheme: "https", Host: "github.com",
		Path: path.Join("/", c.Repository, "releases", "download", release.Version, release.Artifact.Name),
	}
	return endpoint.String(), nil
}

func ManagementOrigin(managementURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(managementURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("managed updates require an HTTPS origin without credentials, path, query, or fragment")
	}
	return fmt.Sprintf("https://%s", parsed.Host), nil
}
