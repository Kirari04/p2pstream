package server

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

const maxPublicGeoDownloadRedirects = 5

// HTTPClient is implemented by *http.Client and permits deterministic, offline
// provider and MaxMind download tests.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

func defaultMaxMindHTTPClient() HTTPClient {
	return &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: maxMindDownloadRedirectPolicy,
	}
}

func maxMindDownloadRedirectPolicy(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return errors.New("refusing non-HTTPS MaxMind download redirect")
	}
	if len(via) >= maxPublicGeoDownloadRedirects {
		return errors.New("too many MaxMind download redirects")
	}
	if len(via) == 0 {
		return nil
	}
	origin := via[0].URL
	if req.URL.Scheme != origin.Scheme || !strings.EqualFold(req.URL.Host, origin.Host) {
		req.Header.Del("Authorization")
		req.Header.Del("Proxy-Authorization")
	}
	return nil
}

func defaultProviderRangeHTTPClient() HTTPClient {
	return &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
