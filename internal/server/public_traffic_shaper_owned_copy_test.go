package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestTrafficShaperDecisionSurvivesContextReuse(t *testing.T) {
	app := &App{TrafficShaper: newPublicTrafficShaper()}
	pool := newBoundedPublicProxyContextPool(1)
	owned := pool.acquire()
	defer func() {
		if owned != nil {
			pool.release(owned)
		}
	}()
	first := owned
	first.App = app
	first.ListenerID = 41
	first.Request = testRateLimitRequest(http.MethodGet, "http://first.example/body", "198.51.100.10:1234")
	first.Snapshot = &publicProxySnapshot{
		Listeners:          map[int64]publicListenerConfig{41: {ID: 41, Protocol: publicListenerProtocolHTTP}},
		TrafficShaperRules: []publicTrafficShaperRuleConfig{testTrafficShaperRule(101, "first-rule", 1, publicTrafficShaperBudgetScopePerRequest, 1024, 1024)},
	}
	if trafficShaperStage(first) != publicProxyStageContinue || !first.TrafficShaperSelected {
		t.Fatal("first request did not select its traffic shaper")
	}
	retained := trafficShaperDecisionIfSelected(first.TrafficShaperDecision, first.TrafficShaperSelected)
	if retained == nil || retained.UploadBucket == nil || retained.DownloadBucket == nil {
		t.Fatal("selected request lost its shaping decision or buckets")
	}
	upload, download := retained.UploadBucket, retained.DownloadBucket
	body := retained.wrapDownloadBody(context.Background(), io.NopCloser(strings.NewReader("first-body")))
	defer body.Close()

	pool.release(owned)
	owned = nil
	second := pool.acquire()
	owned = second
	if second != first {
		t.Fatal("fixture did not reuse the released context")
	}
	if second.TrafficShaperSelected || trafficShaperDecisionIfSelected(second.TrafficShaperDecision, second.TrafficShaperSelected) != nil {
		t.Fatal("reset request retained the first selected shaper")
	}
	second.App = app
	second.ListenerID = 42
	second.Request = testRateLimitRequest(http.MethodGet, "http://second.example/body", "198.51.100.20:5678")
	second.Snapshot = &publicProxySnapshot{
		Listeners:          map[int64]publicListenerConfig{42: {ID: 42, Protocol: publicListenerProtocolHTTP}},
		TrafficShaperRules: []publicTrafficShaperRuleConfig{testTrafficShaperRule(202, "second-rule", 1, publicTrafficShaperBudgetScopePerRequest, 2048, 2048)},
	}
	if trafficShaperStage(second) != publicProxyStageContinue || !second.TrafficShaperSelected {
		t.Fatal("second request did not select its traffic shaper")
	}
	current := trafficShaperDecisionIfSelected(second.TrafficShaperDecision, second.TrafficShaperSelected)
	if current == nil || current == retained || current.Rule.ID != 202 || current.Listener.ID != 42 {
		t.Fatal("second request did not retain an independent selected decision")
	}
	if retained.Rule.ID != 101 || retained.Rule.Name != "first-rule" || retained.Listener.ID != 41 || retained.UploadBucket != upload || retained.DownloadBucket != download || retained.MatchError != nil {
		t.Fatal("context reset/reuse mutated the retained first decision")
	}
	if current.UploadBucket == upload || current.DownloadBucket == download {
		t.Fatal("per-request buckets were shared between requests")
	}
	got, err := io.ReadAll(body)
	if err != nil || string(got) != "first-body" {
		t.Fatalf("retained first shaped body = %q, %v", got, err)
	}
}

// Read global masks so each call has a runtime selection condition, as on the
// real pipeline. Keep the pointer in a sink to model a consumer retaining it.
var benchmarkTrafficShaperSelectionMasks = [3]uint8{0, 3, 1}
var benchmarkTrafficShaperDecisionSink *publicTrafficShaperDecision

func BenchmarkTrafficShaperDecisionOwnership(b *testing.B) {
	for index, name := range []string{"false", "true", "mixed"} {
		b.Run(name, func(b *testing.B) {
			decision := publicTrafficShaperDecision{
				Rule:     publicTrafficShaperRuleConfig{ID: 101, Name: "configured-rule"},
				Listener: publicListenerConfig{ID: 41, Protocol: publicListenerProtocolHTTP},
			}
			mask := benchmarkTrafficShaperSelectionMasks[index]
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				selected := mask&(1<<uint(i&1)) != 0
				benchmarkTrafficShaperDecisionSink = trafficShaperDecisionIfSelected(decision, selected)
			}
		})
	}
}
