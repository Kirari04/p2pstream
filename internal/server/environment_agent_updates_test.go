package server

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

func TestEnvironmentProxyAgentUpdateOperations(t *testing.T) {
	for _, transport := range []string{environmentTransportDirect, environmentTransportAgent} {
		t.Run(transport, func(t *testing.T) {
			ctx := context.Background()
			remote := newAgentUpdateTestApp(t, newServerTestDB(t))
			remote.AgentUpdateBootstrap = agentUpdateTestBootstrapProvider{repository: "remote/releases"}
			remoteAgent := createAgentUpdateTestAgent(t, remote.DB, "remote-update-agent")
			bootstrapAgent := createAgentUpdateTestAgent(t, remote.DB, "remote-bootstrap-agent")
			campaignID, assignmentID := insertAgentUpdateTestCampaign(t, remote.DB, remoteAgent.ID, "failed", "none", false)
			remoteToken, tokenHash, err := newManagementAccessToken()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := remote.DB.CreateManagementAccessToken(ctx, db.CreateManagementAccessTokenParams{
				Name: "parent", TokenHash: tokenHash, Enabled: 1,
			}); err != nil {
				t.Fatal(err)
			}
			remoteMux := http.NewServeMux()
			remote.RegisterManagementRoutes(remoteMux)
			var remoteCalls atomic.Int64
			remoteServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				remoteCalls.Add(1)
				if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "Bearer "+remoteToken {
					t.Error("forwarded update request did not use only the saved remote management token")
				}
				remoteMux.ServeHTTP(w, r)
			}))
			defer remoteServer.Close()

			local := NewApp(nil, newServerTestDB(t))
			adminHeader := createTestAdminSession(t, local)
			localAgent := createAgentUpdateTestAgent(t, local.DB, "local-agent-with-overlapping-id")
			localCampaignID, _ := insertAgentUpdateTestCampaign(t, local.DB, localAgent.ID, "pending", "stage", false)
			var transportAgentID sql.NullInt64
			if transport == environmentTransportAgent {
				agentConn, fake := newFakeYamuxAgent(t, localAgent.ID, localAgent.PublicID)
				if err := local.AgentHub.connect(agentConn); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					local.AgentHub.disconnect(agentConn)
					fake.close()
				})
				transportAgentID = sql.NullInt64{Int64: localAgent.ID, Valid: true}
			}
			environment, err := local.DB.CreateEnvironment(ctx, db.CreateEnvironmentParams{
				Name: "remote", ManagementUrl: remoteServer.URL, Transport: transport,
				AgentID: transportAgentID, AccessToken: remoteToken, Enabled: 1, ResponseHeaderTimeoutMillis: 10000,
			})
			if err != nil {
				t.Fatal(err)
			}
			localMux := http.NewServeMux()
			local.RegisterManagementRoutes(localMux)
			call := func(method, body string, header http.Header) *httptest.ResponseRecorder {
				t.Helper()
				path := fmt.Sprintf("/environments/%d/p2pstream.v1.AgentManagementService/%s", environment.ID, method)
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
				req.Header = header.Clone()
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Connect-Protocol-Version", "1")
				response := httptest.NewRecorder()
				localMux.ServeHTTP(response, req)
				return response
			}
			assertStatus := func(t *testing.T, response *httptest.ResponseRecorder, want int) {
				t.Helper()
				if response.Code != want {
					t.Fatalf("response = %d %s, want %d", response.Code, response.Body.String(), want)
				}
			}
			assertStatus(t, call("GetAgentUpdateOverview", "{}", adminHeader), http.StatusPreconditionFailed)
			if remoteCalls.Load() != 0 {
				t.Fatal("update request reached an untrusted environment")
			}
			discoverReq := connect.NewRequest(&p2pstreamv1.DiscoverEnvironmentCertificateRequest{Id: environment.ID})
			discoverReq.Header().Set("Cookie", adminHeader.Get("Cookie"))
			discovery, err := local.DiscoverEnvironmentCertificate(ctx, discoverReq)
			if err != nil {
				t.Fatal(err)
			}
			trustReq := connect.NewRequest(&p2pstreamv1.TrustEnvironmentCertificateRequest{
				Id: environment.ID, Sha256Fingerprint: discovery.Msg.Certificate.Sha256Fingerprint,
			})
			trustReq.Header().Set("Cookie", adminHeader.Get("Cookie"))
			if _, err := local.TrustEnvironmentCertificate(ctx, trustReq); err != nil {
				t.Fatal(err)
			}

			viewer, err := local.DB.CreateUser(ctx, db.CreateUserParams{Username: "viewer", PasswordHash: "unused", Role: "user"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := local.DB.CreateSession(ctx, db.CreateSessionParams{
				UserID: viewer.ID, TokenHash: hashSessionToken("viewer-session"), ExpiresAt: time.Now().Add(time.Hour),
			}); err != nil {
				t.Fatal(err)
			}
			viewerHeader := http.Header{"Cookie": {sessionCookieName + "=viewer-session"}}
			for _, method := range []string{
				"GenerateAgentUpdaterEnrollmentToken", "GetAgentUpdateOverview", "ListAgentUpdateCampaigns",
				"PreviewAgentUpdateCampaign", "CreateAgentUpdateCampaign", "PauseAgentUpdateCampaign",
				"ResumeAgentUpdateCampaign", "CancelAgentUpdateCampaign", "RetryAgentUpdateAssignments",
			} {
				assertStatus(t, call(method, "{}", http.Header{}), http.StatusUnauthorized)
				assertStatus(t, call(method, "{}", viewerHeader), http.StatusForbidden)
			}
			for _, method := range []string{"EnrollAgentUpdater", "CheckAgentUpdate", "ReportAgentUpdate", "FutureSensitiveMethod"} {
				assertStatus(t, call(method, "{}", adminHeader), http.StatusForbidden)
			}
			if remoteCalls.Load() != 0 {
				t.Fatal("unauthorized or host-only update request reached the remote server")
			}

			for _, test := range []struct {
				method, body, contains string
				status                 int
			}{
				{"GetAgentUpdateOverview", "{}", remoteAgent.PublicID, http.StatusOK},
				{"ListAgentUpdateCampaigns", "{}", "campaigns", http.StatusOK},
				{"GenerateAgentUpdaterEnrollmentToken", fmt.Sprintf(`{"agentId":"%d"}`, bootstrapAgent.ID), remote.AgentUpdateAuthority.Identity().KeyID, http.StatusOK},
				// The remote catalog still decides whether a target may be used.
				{"PreviewAgentUpdateCampaign", fmt.Sprintf(`{"target":{"manifestSha256":%q}}`, strings.Repeat("a", 64)), "trusted agent update catalog is not configured", http.StatusBadRequest},
				{"CreateAgentUpdateCampaign", fmt.Sprintf(`{"name":"remote campaign","target":{"manifestSha256":%q}}`, strings.Repeat("a", 64)), "trusted agent update catalog is not configured", http.StatusBadRequest},
				{"RetryAgentUpdateAssignments", fmt.Sprintf(`{"campaignId":"%d","assignmentIds":["%d"],"expectedCampaignGeneration":"1"}`, campaignID, assignmentID), "PENDING", http.StatusOK},
				{"PauseAgentUpdateCampaign", fmt.Sprintf(`{"campaignId":"%d","expectedGeneration":"2"}`, campaignID), "PAUSED", http.StatusOK},
				{"ResumeAgentUpdateCampaign", fmt.Sprintf(`{"campaignId":"%d","expectedGeneration":"3"}`, campaignID), "RUNNING", http.StatusOK},
				{"CancelAgentUpdateCampaign", fmt.Sprintf(`{"campaignId":"%d","expectedGeneration":"4"}`, campaignID), "CANCELLED", http.StatusOK},
			} {
				t.Run(test.method, func(t *testing.T) {
					before := remoteCalls.Load()
					response := call(test.method, test.body, adminHeader)
					assertStatus(t, response, test.status)
					if remoteCalls.Load() != before+1 || !strings.Contains(response.Body.String(), test.contains) {
						t.Fatalf("expected remote %s response containing %q, got %s", test.method, test.contains, response.Body.String())
					}
				})
			}
			var localState string
			if err := local.DB.QueryRow(`SELECT state FROM agent_update_campaigns WHERE id=?`, localCampaignID).Scan(&localState); err != nil || localState != "running" {
				t.Fatalf("remote operations changed the overlapping local campaign: %q, %v", localState, err)
			}
			var localTokens int
			if err := local.DB.QueryRow(`SELECT COUNT(*) FROM agent_updater_enrollment_tokens`).Scan(&localTokens); err != nil || localTokens != 0 {
				t.Fatalf("remote enrollment created local tokens: %d, %v", localTokens, err)
			}
			if _, err := remote.DB.Exec(`UPDATE management_access_tokens SET enabled=0`); err != nil {
				t.Fatal(err)
			}
			assertStatus(t, call("GetAgentUpdateOverview", "{}", adminHeader), http.StatusUnauthorized)
		})
	}
}
