package server

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestPublicProxyCacheEventRetainsBypassAttribution(t *testing.T) {
	for _, name := range []string{"nil-decision-bypass", "nil-decision-bypass-rule", "empty-resolution", "retained-bypass", "retained-final-store"} {
		t.Run(name, func(t *testing.T) {
			app, resolution, closeDB := newTestPublicCacheApp(t)
			defer closeDB()
			defer app.DirectTransports.closeAll()
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := app.CloseObservabilityRecorder(ctx); err != nil {
					t.Error(err)
				}
			}()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("Cache-Control", "public, max-age=300")
				_, _ = io.WriteString(w, "cache-event-body")
			}))
			defer origin.Close()
			parsed, err := url.Parse(origin.URL)
			if err != nil {
				t.Fatal(err)
			}
			resolution.Target.ParsedURL = parsed
			req := httptest.NewRequest(http.MethodGet, "http://assets.example.test/assets/event.txt", nil)
			var decision *publicCacheDecision
			wantStatus, wantRule, wantBytes := "", sql.NullInt64{}, int64(0)
			switch name {
			case "nil-decision-bypass", "nil-decision-bypass-rule":
				resolution.CacheStatus = publicCacheStatusBypass
				wantStatus = publicCacheStatusBypass
				if name == "nil-decision-bypass-rule" {
					// A rule can be selected before a later eligibility/key failure.
					resolution.CacheRuleID = resolution.CacheRuleID + 7
					wantRule = sql.NullInt64{Int64: resolution.CacheRuleID, Valid: true}
				} else {
					resolution.CacheRuleID = 0
				}
			case "empty-resolution":
				resolution.CacheRuleID = 0
			case "retained-bypass":
				// Lower forwarding callers can supply their own retained decision.
				// It must win over stale resolution metadata in every field.
				resolution.CacheStatus, resolution.CacheRuleID = publicCacheStatusMiss, 999
				decision = &publicCacheDecision{Status: publicCacheStatusBypass, Rule: publicCacheRuleConfig{ID: 72}}
				wantStatus, wantRule = publicCacheStatusBypass, sql.NullInt64{Int64: 72, Valid: true}
			case "retained-final-store":
				selected := app.checkPublicCache(req, resolution)
				if selected.Status != publicCacheStatusMiss {
					t.Fatalf("initial cache status=%q, want miss", selected.Status)
				}
				applyCacheResolutionFields(&resolution, selected)
				decision = &selected
				wantStatus, wantRule = publicCacheStatusStored, sql.NullInt64{Int64: selected.Rule.ID, Valid: true}
				wantBytes = int64(len("cache-event-body"))
			}
			recorder := httptest.NewRecorder()
			app.proxyDirectTargetRequest(recorder, req, resolution, nil, nil, decision, proxyRequestObservability{})
			if recorder.Code != http.StatusOK || recorder.Body.String() != "cache-event-body" {
				t.Fatalf("response=%d %q", recorder.Code, recorder.Body.String())
			}
			if name == "retained-final-store" && (decision.Status != publicCacheStatusStored || decision.StoredBytes != wantBytes || resolution.CacheStatus != publicCacheStatusMiss) {
				t.Fatalf("store did not mutate only retained final decision: decision=%+v resolution_status=%s", decision, resolution.CacheStatus)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if err := app.FlushObservabilityRecorder(ctx); err != nil {
				t.Fatal(err)
			}
			var count, status int
			var cacheRule sql.NullInt64
			var cacheStatus string
			var cacheBytes int64
			if err := app.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM proxy_request_events`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("event count=%d err=%v", count, err)
			}
			if err := app.DB.QueryRowContext(ctx, `SELECT status_code,cache_rule_id,cache_status,cache_bytes FROM proxy_request_events`).Scan(&status, &cacheRule, &cacheStatus, &cacheBytes); err != nil {
				t.Fatal(err)
			}
			if status != 200 || cacheRule != wantRule || cacheStatus != wantStatus || cacheBytes != wantBytes {
				t.Fatalf("event status=%d cache=%v/%s/%d want=%v/%s/%d", status, cacheRule, cacheStatus, cacheBytes, wantRule, wantStatus, wantBytes)
			}
		})
	}
}
