package updater

import (
	"context"
	"crypto/sha256"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestGitHubSourceUsesOnlyImmutableReleaseAssetPaths(t *testing.T) {
	body := []byte("raw-binary")
	digest := sha256.Sum256(body)
	wantPaths := []string{
		"/owner/repo/releases/download/v1.2.3/" + manifestAssetName,
		"/owner/repo/releases/download/v1.2.3/" + signatureAssetName,
		"/owner/repo/releases/download/v1.2.3/" + runtimeArtifactName("v1.2.3"),
	}
	index := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Scheme != "https" || request.URL.Host != "github.com" || request.URL.Path != wantPaths[index] {
			t.Fatalf("request URL = %s, want path %s", request.URL, wantPaths[index])
		}
		var data []byte
		switch index {
		case 0:
			data = []byte("manifest")
		case 1:
			data = []byte("signatures")
		default:
			data = body
		}
		index++
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(data))), ContentLength: int64(len(data)), Header: make(http.Header)}, nil
	})}
	config := HostConfig{Repository: "owner/repo", ManagementOrigin: "https://management.example", AgentPublicID: "agent-a", Channel: "stable"}
	source, err := NewGitHubSource(config, "v1.2.3", client)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.FetchMetadata(context.Background()); err != nil {
		t.Fatal(err)
	}
	artifact := Artifact{Name: runtimeArtifactName("v1.2.3"), Size: int64(len(body)), SHA256: digest}
	reader, err := source.FetchArtifact(context.Background(), artifact)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if got, _ := io.ReadAll(reader); string(got) != string(body) {
		t.Fatalf("artifact = %q", got)
	}
	if index != 3 {
		t.Fatalf("request count = %d", index)
	}
}

func TestGitHubSourceRejectsMutableOrPreReleaseVersion(t *testing.T) {
	config := HostConfig{Repository: "owner/repo", ManagementOrigin: "https://management.example", AgentPublicID: "agent-a", Channel: "stable"}
	for _, version := range []string{"latest", "staging", "v1.2.3-rc.1", "../v1.2.3"} {
		if _, err := NewGitHubSource(config, version, &http.Client{}); err == nil {
			t.Fatalf("version %q accepted", version)
		}
	}
}

func TestGitHubSourceDefaultClientIgnoresEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://attacker.invalid:8080")
	t.Setenv("HTTP_PROXY", "http://attacker.invalid:8080")
	client := newGitHubHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Fatal("release artifact transport inherited an environment proxy")
	}
}
