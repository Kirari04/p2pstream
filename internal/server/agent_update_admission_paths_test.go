package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"p2pstream/gen/proto/p2pstream/v1/p2pstreamv1connect"
	"p2pstream/internal/config"
)

func TestManagementUpdaterPathVariantsCannotBypassAdmission(t *testing.T) {
	const prefix = "/p2pstream.v1.AgentManagementService/"
	for _, control := range []struct {
		name   string
		setup  func(*App)
		status int
	}{
		{"size", func(*App) {}, http.StatusRequestEntityTooLarge},
		{"compression", func(*App) {}, http.StatusUnsupportedMediaType},
		{"rate", func(a *App) {
			a.agentUpdateGlobalRate = newAgentUpdateRateLimiter(1, time.Hour, 1, 1)
			a.agentUpdateGlobalRate.allow("global", time.Now())
		}, http.StatusTooManyRequests},
		{"gate", func(a *App) {
			for range cap(a.agentUpdateRequestGate) {
				a.agentUpdateRequestGate <- struct{}{}
			}
		}, http.StatusServiceUnavailable},
	} {
		t.Run(control.name, func(t *testing.T) {
			app := NewApp(&config.Config{ManagementUIDisabled: true}, nil)
			control.setup(app)
			mux := http.NewServeMux()
			app.RegisterManagementRoutes(mux)
			for _, procedure := range []string{
				p2pstreamv1connect.AgentManagementServiceEnrollAgentUpdaterProcedure,
				p2pstreamv1connect.AgentManagementServiceCheckAgentUpdateProcedure,
				p2pstreamv1connect.AgentManagementServiceReportAgentUpdateProcedure,
			} {
				method := strings.TrimPrefix(procedure, prefix)
				for _, variant := range []struct {
					name, path string
					status     int // zero means the canonical admission status
				}{
					{"canonical", procedure, 0},
					{"encoded first method byte", prefix + fmt.Sprintf("%%%02x", method[0]) + method[1:], 0},
					{"encoded separator", strings.TrimSuffix(prefix, "/") + "%2f" + method, http.StatusNotFound},
					{"encoded service", "/%70" + prefix[2:] + method, 0},
					{"double encoded separator", strings.TrimSuffix(prefix, "/") + "%252f" + method, http.StatusNotFound},
					{"trailing slash", procedure + "/", http.StatusNotFound},
					{"encoded trailing slash", procedure + "%2f", http.StatusNotFound},
					{"doubled slash", prefix + "/" + method, http.StatusTemporaryRedirect},
					{"dot segment", prefix + "./" + method, http.StatusTemporaryRedirect},
					{"parent segment", prefix + "alias/../" + method, http.StatusTemporaryRedirect},
					{"encoded dot segment", prefix + "%2e/" + method, http.StatusNotFound},
				} {
					t.Run(method+"/"+variant.name, func(t *testing.T) {
						serve := func(path string) *httptest.ResponseRecorder {
							req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
							req.Header.Set("Content-Type", "application/json")
							if control.name == "size" {
								req.ContentLength = agentUpdateReportMaxRequestBytes + 1
							}
							if control.name == "compression" {
								req.Header.Set("Content-Encoding", "gzip")
							}
							recorder := httptest.NewRecorder()
							mux.ServeHTTP(recorder, req)
							return recorder
						}
						want := variant.status
						if want == 0 {
							want = control.status
						}
						response := serve(variant.path)
						if response.Code != want {
							t.Fatalf("status=%d, want %d: %s", response.Code, want, response.Body.String())
						}
						if response.Code == http.StatusTemporaryRedirect {
							if location := response.Header().Get("Location"); location != procedure {
								t.Fatalf("redirect=%q, want canonical procedure %q", location, procedure)
							} else if redirected := serve(location); redirected.Code != control.status {
								t.Fatalf("redirect target bypassed admission: status=%d", redirected.Code)
							}
						}
					})
				}
			}
		})
	}
}

func TestManagementUpdaterCanonicalAndEncodedRequests(t *testing.T) {
	app := NewApp(&config.Config{ManagementUIDisabled: true}, nil)
	mux := http.NewServeMux()
	app.RegisterManagementRoutes(mux)
	for _, procedure := range []struct {
		path   string
		limit  int64
		status int
	}{
		{p2pstreamv1connect.AgentManagementServiceEnrollAgentUpdaterProcedure, agentUpdateEnrollMaxRequestBytes, http.StatusBadRequest},
		{p2pstreamv1connect.AgentManagementServiceCheckAgentUpdateProcedure, agentUpdateCheckMaxRequestBytes, http.StatusUnauthorized},
		{p2pstreamv1connect.AgentManagementServiceReportAgentUpdateProcedure, agentUpdateReportMaxRequestBytes, http.StatusBadRequest},
	} {
		for _, path := range []string{procedure.path, strings.Replace(procedure.path, "/p", "/%70", 1)} {
			t.Run(path, func(t *testing.T) {
				for _, oversized := range []bool{false, true} {
					body := "{}"
					if oversized {
						body += strings.Repeat(" ", int(procedure.limit))
					}
					request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
					request.Header.Set("Content-Type", "application/json")
					// Exercise the streaming wire bound independently of the
					// Content-Length precheck and the larger Connect read limit.
					request.ContentLength = -1
					response := httptest.NewRecorder()
					mux.ServeHTTP(response, request)
					if oversized {
						if response.Code != http.StatusTooManyRequests || !strings.Contains(response.Body.String(), "http.MaxBytesReader limit") {
							t.Fatalf("oversized response: %d %s", response.Code, response.Body.String())
						}
					} else if response.Code != procedure.status {
						t.Fatalf("ordinary request did not reach method validation: %d %s", response.Code, response.Body.String())
					}
					if len(app.agentUpdateRequestGate) != 0 || len(app.agentUpdatePeerRequests.inUse) != 0 {
						t.Fatal("method validation or body rejection leaked admission")
					}
				}
			})
		}
	}
}
