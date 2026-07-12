package server

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

func TestMaxMindHTTPClientStripsAuthorizationOffExactOrigin(t *testing.T) {
	var destinationAuthorization string
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationAuthorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/presigned", http.StatusFound)
	}))
	defer origin.Close()

	client := maxMindTestHTTPClient()
	req, err := http.NewRequest(http.MethodGet, origin.URL+"/database", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("12345", "test-secret")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if destinationAuthorization != "" {
		t.Fatalf("Authorization leaked to redirected origin: %q", destinationAuthorization)
	}
}

func TestMaxMindHTTPClientKeepsAuthorizationOnExactOrigin(t *testing.T) {
	var finalAuthorization string
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, server.URL+"/final", http.StatusFound)
			return
		}
		finalAuthorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := maxMindTestHTTPClient()
	req, err := http.NewRequest(http.MethodGet, server.URL+"/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("12345", "test-secret")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if finalAuthorization == "" {
		t.Fatal("Authorization was unexpectedly removed on an exact-origin redirect")
	}
}

func TestMaxMindHTTPClientRejectsHTTPSDowngrade(t *testing.T) {
	var insecureDestinationHits atomic.Int64
	insecureDestination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		insecureDestinationHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer insecureDestination.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, insecureDestination.URL+"/database", http.StatusFound)
	}))
	defer origin.Close()

	client := maxMindTestHTTPClient()
	resp, err := client.Get(origin.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("HTTPS-to-HTTP redirect was accepted")
	}
	if got := insecureDestinationHits.Load(); got != 0 {
		t.Fatalf("insecure redirect destination received %d requests", got)
	}
}

func TestMaxMindHTTPClientCapsRedirectHops(t *testing.T) {
	var requests atomic.Int64
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := requests.Add(1)
		http.Redirect(w, r, server.URL+"/?hop="+strconv.FormatInt(count, 10), http.StatusFound)
	}))
	defer server.Close()

	client := maxMindTestHTTPClient()
	resp, err := client.Get(server.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("unbounded redirect chain was accepted")
	}
	if got := requests.Load(); got != maxPublicGeoDownloadRedirects {
		t.Fatalf("requests = %d, want cap %d", got, maxPublicGeoDownloadRedirects)
	}
}

func TestProviderRangeHTTPClientDoesNotFollowRedirects(t *testing.T) {
	var destinationHits atomic.Int64
	destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/ranges", http.StatusFound)
	}))
	defer origin.Close()

	client, ok := defaultProviderRangeHTTPClient().(*http.Client)
	if !ok {
		t.Fatal("default provider client has unexpected type")
	}
	client.Transport = insecureLocalTLSTransport()
	resp, err := client.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want redirect response", resp.StatusCode)
	}
	if got := destinationHits.Load(); got != 0 {
		t.Fatalf("redirect destination received %d requests", got)
	}
}

func maxMindTestHTTPClient() *http.Client {
	client := defaultMaxMindHTTPClient().(*http.Client)
	client.Transport = insecureLocalTLSTransport()
	return client
}

func insecureLocalTLSTransport() *http.Transport {
	// The test servers use ephemeral self-signed certificates. Production uses
	// the default verified transport because these clients leave Transport nil.
	return &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec
}
