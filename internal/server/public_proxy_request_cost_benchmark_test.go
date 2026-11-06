package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// This benchmark intentionally uses only preexisting production entry points so
// it can be copied unchanged into a baseline checkout for alternating screens.
func BenchmarkPublicProxyRequestPathCosts(b *testing.B) {
	for _, bodyKind := range []string{"nil-body", "no-body", "body"} {
		for _, cleanupCount := range []int{0, 5, 8} {
			b.Run(bodyKind+"/cleanup-"+strconv.Itoa(cleanupCount), func(b *testing.B) {
				pool := newBoundedPublicProxyContextPool(1)
				writer := httptest.NewRecorder()
				template := httptest.NewRequest(http.MethodPost, "http://benchmark.example/resource", nil)
				var reader strings.Reader
				body := io.NopCloser(&reader)
				if bodyKind == "nil-body" {
					template.Body = nil
				}
				noop := func() {}
				var buf [32]byte
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					// Request parsing is outside this benchmark. Copy only the
					// request container whose body initialization mutates it.
					request := *template
					if bodyKind == "body" {
						reader.Reset("request-body")
						request.Body = body
						request.ContentLength = int64(reader.Len())
					}
					ctx := pool.acquire()
					initializePublicProxyContext(ctx, nil, 1, writer, &request)
					for range cleanupCount {
						ctx.deferCleanup(noop)
					}
					if bodyKind == "body" {
						for {
							if _, err := ctx.Request.Body.Read(buf[:]); err != nil {
								if err != io.EOF {
									b.Fatal(err)
								}
								break
							}
						}
						if err := ctx.Request.Body.Close(); err != nil {
							b.Fatal(err)
						}
					}
					ctx.runCleanup()
					pool.release(ctx)
				}
			})
		}
	}
}
