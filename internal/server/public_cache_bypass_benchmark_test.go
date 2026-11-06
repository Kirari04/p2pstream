package server

import (
	"net/http/httptest"
	"testing"
)

func BenchmarkPublicCacheUnavailableBypass(b *testing.B) {
	ctx := &publicProxyContext{
		App:     &App{},
		Request: httptest.NewRequest("GET", "https://public.test/small", nil),
	}
	ctx.Resolution.Target.TargetType = publicRouteTargetTypeProxy
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if cacheLookupStage(ctx) != publicProxyStageContinue || ctx.CacheDecision != nil || ctx.Resolution.CacheStatus != publicCacheStatusBypass {
			b.Fatal("cache bypass changed forwarding or cache status")
		}
	}
}
