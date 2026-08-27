package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRetryFixtureHandlersRejectUnlistedAssetsWithoutReflection(t *testing.T) {
	t.Parallel()

	const marker = "script-alert"
	tests := []struct {
		name    string
		path    string
		handler http.HandlerFunc
	}{
		{name: "asset", path: "/retry-assets/" + marker + ".js", handler: retryAssetHandler},
		{name: "response asset", path: "/retry-response-assets/" + marker + ".html", handler: retryResponseAssetHandler},
		{name: "close delimited", path: "/retry-close-delimited/" + marker + ".txt", handler: retryCloseDelimitedAssetHandler},
		{name: "status", path: "/retry-status/502/" + marker + ".js", handler: retryStatusHandler},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			recorder := httptest.NewRecorder()
			test.handler(recorder, httptest.NewRequest(http.MethodGet, test.path, nil))

			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
			}
			if strings.Contains(recorder.Body.String(), marker) {
				t.Fatalf("response reflected unlisted asset: %q", recorder.Body.String())
			}
		})
	}
}

func TestRetryFixtureAllowlistsReturnCanonicalNames(t *testing.T) {
	t.Parallel()

	if got, ok := retryAssetFixture("app.js"); !ok || got != "app.js" {
		t.Fatalf("retry asset fixture = %q, %v", got, ok)
	}
	if got, ok := retryResponseAssetFixture("archive.gzip.bin"); !ok || got != "archive.gzip.bin" {
		t.Fatalf("retry response fixture = %q, %v", got, ok)
	}
	if got, ok := retryStatusAssetFixture("theme.css"); !ok || got != "theme.css" {
		t.Fatalf("retry status fixture = %q, %v", got, ok)
	}
}
