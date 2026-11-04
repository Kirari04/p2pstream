//go:build linux

// This frozen overlay is copied into a clean 3ec137c archive. The testdata
// directory keeps it out of current package builds while preserving the exact
// historical external-process fixture source.
package server

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"p2pstream/internal/config"
	"p2pstream/internal/db"
	"p2pstream/internal/tunnel"
)

type legacyExternalQualificationCredential struct {
	ID       int64  `json:"id"`
	PublicID string `json:"public_id"`
	Token    string `json:"token"`
}

type legacyExternalQualificationReady struct {
	URL              string                                  `json:"url"`
	AdminURL         string                                  `json:"admin_url"`
	PID              int                                     `json:"pid"`
	ManagementURL    string                                  `json:"management_url"`
	ManagementCAFile string                                  `json:"management_ca_file"`
	Agents           []legacyExternalQualificationCredential `json:"agents"`
}

func TestExternalProxyQualificationServerProcess(t *testing.T) {
	if os.Getenv("P2PSTREAM_QUALIFICATION_ROLE") != "proxy-external-agent" {
		t.Skip("external qualification server subprocess only")
	}
	originURL := requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_ORIGIN_URL")
	certPath := requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_CERT")
	keyPath := requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_KEY")
	managementCAFile := requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_MANAGEMENT_CA_FILE")
	agentCount := qualificationOptionalEnvInt("P2PSTREAM_QUALIFICATION_EXTERNAL_AGENTS")
	if agentCount == 0 {
		agentCount = 1
	}
	if agentCount < 1 || agentCount > 4 {
		t.Fatalf("invalid external agent count %d", agentCount)
	}
	ballastMiB := qualificationOptionalEnvInt("P2PSTREAM_QUALIFICATION_BALLAST_MIB")
	if ballastMiB < 0 || ballastMiB > 2048 {
		t.Fatalf("invalid ballast MiB %d", ballastMiB)
	}
	ballast := make([]byte, ballastMiB<<20)
	for offset := 0; offset < len(ballast); offset += os.Getpagesize() {
		ballast[offset] = 1
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	database, err := db.Open(filepath.Join(t.TempDir(), "external-agent-qualification.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	app := NewApp(&config.Config{
		ManagementUIDisabled: true, ConfigDir: t.TempDir(),
		ServerTunnelMaxConcurrentStreams: tunnel.MaxServerConcurrentStreamsLimit,
		ServerTunnelCapacityAuto:         true,
	}, database)
	app.StartAdaptiveTunnelCapacity(ctx)
	defer app.CloseAgentTransports()

	credentials := make([]legacyExternalQualificationCredential, 0, agentCount)
	agentIDs := make([]int64, 0, agentCount)
	agents := make(map[int64]publicAgentConfig, agentCount)
	for index := 0; index < agentCount; index++ {
		publicID := fmt.Sprintf("qualification-external-agent-%d", index+1)
		token := fmt.Sprintf("qualification-external-token-%d", index+1)
		row, createErr := database.CreateAgent(ctx, db.CreateAgentParams{
			PublicID: publicID, Name: fmt.Sprintf("Qualification external agent %d", index+1),
			TokenHash: hashAgentToken(token), Enabled: 1,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		credentials = append(credentials, legacyExternalQualificationCredential{ID: row.ID, PublicID: row.PublicID, Token: token})
		agentIDs = append(agentIDs, row.ID)
		agents[row.ID] = publicAgentConfig{ID: row.ID, PublicID: row.PublicID, Enabled: true, Labels: map[string]string{"qualification": "external"}}
	}

	managementMux := http.NewServeMux()
	app.RegisterManagementRoutes(managementMux)
	management := httptest.NewUnstartedServer(managementMux)
	management.StartTLS()
	defer management.Close()
	managementCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: management.Certificate().Raw})
	if err := os.WriteFile(managementCAFile, managementCA, 0600); err != nil {
		t.Fatal(err)
	}

	parsed, err := url.Parse(originURL)
	if err != nil {
		t.Fatal(err)
	}
	target := publicRouteTargetConfig{
		ID: 902, RouteID: 901, Name: "qualification-external-agent", Enabled: true, Position: 0, Weight: 100,
		TargetType: publicRouteTargetTypeProxy, Transport: publicRouteTargetTransportAgent,
		URL: originURL, ParsedURL: parsed, TLSSkipVerify: parsed.Scheme == "https",
		AgentSelector:                 publicAgentSelectorConfig{MatchLabels: map[string]string{"qualification": "external"}},
		UpstreamResponseHeaderTimeout: 10 * time.Second,
	}
	listener := publicListenerConfig{ID: 1, Name: "qualification-external", Protocol: publicListenerProtocolHTTPS, BindAddress: "127.0.0.1", Enabled: true}
	snapshot := &publicProxySnapshot{
		Listeners: map[int64]publicListenerConfig{1: listener},
		RoutesByListener: map[int64][]publicRouteConfig{1: {{
			ID: 901, ListenerID: 1, Enabled: true, HostPattern: "public.test", PathPrefix: "/agent",
			PathSecurityMode: publicRoutePathSecurityModeStrict, Targets: []publicRouteTargetConfig{target},
		}}},
		RouteTargets: map[int64]publicRouteTargetConfig{902: target}, Agents: agents,
		CertsByListener: map[int64][]publicTLSCertificateConfig{1: {{
			ID: 1, ListenerID: 1, HostnamePattern: "public.test", CertPath: certPath, KeyPath: keyPath,
			Enabled: true, Source: publicTLSCertificateSourceManual, Status: publicTLSCertificateStatusReady,
		}}},
	}
	setPublicSnapshotForTest(t, app, snapshot)
	app.TargetHealth.reconcile(app, snapshot, false)
	if _, err := app.startPublicListenerFromSnapshot(listener, snapshot); err != nil {
		t.Fatal(err)
	}
	app.proxyMu.Lock()
	address := app.publicListenerState[1].BoundAddress
	app.proxyMu.Unlock()

	baseAdmin := qualificationRuntimeHandler(app)
	adminListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	admin := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := httptest.NewRecorder()
		baseAdmin.ServeHTTP(recorder, r)
		if recorder.Code != http.StatusOK {
			w.WriteHeader(recorder.Code)
			_, _ = w.Write(recorder.Body.Bytes())
			return
		}
		var result map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tunnels := make([]map[string]any, 0, len(agentIDs))
		for _, agentID := range agentIDs {
			row := map[string]any{"id": agentID, "connected": false}
			if agent := app.AgentHub.connectedByID(agentID); agent != nil {
				agent.tunnelMu.RLock()
				lanes := 0
				for _, session := range agent.tunnels {
					if session != nil && !session.IsClosed() {
						lanes++
					}
				}
				agent.tunnelMu.RUnlock()
				row["public_id"] = agent.PublicID
				row["connected"] = true
				row["flow_control"] = "legacy"
				row["control_connected"] = lanes > 0
				row["bulk_sessions"] = max(0, lanes-1)
			}
			tunnels = append(tunnels, row)
		}
		result["agent_tunnels"] = tunnels
		_ = json.NewEncoder(w).Encode(result)
	})}
	go func() { _ = admin.Serve(adminListener) }()
	if err := writeQualificationJSON(requiredQualificationEnv(t, "P2PSTREAM_QUALIFICATION_READY"), legacyExternalQualificationReady{
		URL: "https://" + address, AdminURL: "http://" + adminListener.Addr().String(), PID: os.Getpid(),
		ManagementURL: management.URL, ManagementCAFile: managementCAFile, Agents: credentials,
	}); err != nil {
		t.Fatal(err)
	}
	waitQualificationStop(t)
	runtime.KeepAlive(ballast)
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_, _ = app.stopPublicListenerRuntime(shutdownCtx, 1)
	_ = admin.Shutdown(shutdownCtx)
}
