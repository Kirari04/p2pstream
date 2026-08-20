package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	p2pstreamv1 "p2pstream/gen/proto/p2pstream/v1"
	"p2pstream/internal/db"
)

func TestManagementTLSRuntimeRotatesRollsBackAndPersists(t *testing.T) {
	cfg := managementTLSTestConfig(t)
	database, err := db.Open(filepath.Join(t.TempDir(), "rotation.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	tlsConfig, enabled, err := NewManagementTLSConfig(cfg)
	if err != nil {
		t.Fatalf("create management TLS config: %v", err)
	}
	runtime, err := NewManagementTLSRuntime(cfg, database, tlsConfig, enabled)
	if err != nil {
		t.Fatalf("create management TLS runtime: %v", err)
	}
	before := runtime.cert.Load()
	if err := runtime.generateAndStage(); err != nil {
		t.Fatalf("generate and stage: %v", err)
	}
	if stat, err := os.Stat(runtime.stateFile); err != nil || stat.Mode().Perm() != 0600 {
		t.Fatalf("rotation state mode = %v, err = %v; want 0600", statMode(stat), err)
	}
	if stat, err := os.Stat(runtime.state.StagedKeyFile); err != nil || stat.Mode().Perm() != 0600 {
		t.Fatalf("staged key mode = %v, err = %v; want 0600", statMode(stat), err)
	}
	snapshot, err := runtime.snapshot(context.Background(), nil)
	if err != nil {
		t.Fatalf("snapshot staged rotation: %v", err)
	}
	if snapshot.Phase != p2pstreamv1.ManagementTlsRotationPhase_MANAGEMENT_TLS_ROTATION_PHASE_DISTRIBUTING || snapshot.StagedCertificate == nil {
		t.Fatalf("unexpected staged snapshot: %+v", snapshot)
	}
	if err := runtime.activate(context.Background(), false); err != nil {
		t.Fatalf("activate with empty fleet: %v", err)
	}
	if runtime.cert.Load() == before {
		t.Fatal("active TLS certificate pointer was not replaced")
	}
	if err := runtime.rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if runtime.cert.Load() == nil {
		t.Fatal("rollback left no active certificate")
	}

	// A fresh TLS config must honor the persisted active generation rather than
	// blindly reverting to the original configured certificate.
	restartedConfig, restartedEnabled, err := NewManagementTLSConfig(cfg)
	if err != nil {
		t.Fatalf("create restarted management TLS config: %v", err)
	}
	if _, err := NewManagementTLSRuntime(cfg, database, restartedConfig, restartedEnabled); err != nil {
		t.Fatalf("restore persisted management TLS runtime: %v", err)
	}
}

func TestManagementTLSRuntimeBlocksUntilEnabledAgentDurablyAcknowledges(t *testing.T) {
	ctx := context.Background()
	cfg := managementTLSTestConfig(t)
	database, err := db.Open(filepath.Join(t.TempDir(), "rotation-blocker.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	result, err := database.ExecContext(ctx, `INSERT INTO agents (public_id, name, token_hash, enabled) VALUES ('agent-test', 'Test agent', 'hash', 1)`)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	agentID, _ := result.LastInsertId()
	tlsConfig, enabled, err := NewManagementTLSConfig(cfg)
	if err != nil {
		t.Fatalf("create management TLS config: %v", err)
	}
	runtime, err := NewManagementTLSRuntime(cfg, database, tlsConfig, enabled)
	if err != nil {
		t.Fatalf("create management TLS runtime: %v", err)
	}
	if err := runtime.generateAndStage(); err != nil {
		t.Fatalf("generate and stage: %v", err)
	}
	snapshot, err := runtime.snapshot(ctx, nil)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.BlockingAgentCount != 1 {
		t.Fatalf("blocking agents = %d, want 1", snapshot.BlockingAgentCount)
	}
	if err := runtime.activate(ctx, false); err == nil {
		t.Fatal("activation unexpectedly ignored unacknowledged enabled agent")
	}
	status := &p2pstreamv1.ManagementTrustStatus{
		InstalledGeneration:   snapshot.RolloutGeneration,
		InstalledBundleSha256: snapshot.RolloutBundleSha256,
		State:                 p2pstreamv1.ManagementTrustInstallState_MANAGEMENT_TRUST_INSTALL_STATE_READY,
		AgentVersion:          "test",
		Capabilities:          []string{managementTrustCapability},
	}
	if err := runtime.recordTrustReport(ctx, agentID, status); err != nil {
		t.Fatalf("record trust report: %v", err)
	}
	if err := runtime.activate(ctx, false); err != nil {
		t.Fatalf("activate after durable acknowledgement: %v", err)
	}
}

func TestManagementTLSRuntimeCancelRequiresAcknowledgedTrustCleanup(t *testing.T) {
	ctx := context.Background()
	cfg := managementTLSTestConfig(t)
	database, err := db.Open(filepath.Join(t.TempDir(), "cancel-cleanup.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	compatibleResult, err := database.ExecContext(ctx, `INSERT INTO agents (public_id, name, token_hash, enabled) VALUES ('agent-compatible', 'Compatible', 'hash', 1)`)
	if err != nil {
		t.Fatalf("insert compatible agent: %v", err)
	}
	compatibleID, _ := compatibleResult.LastInsertId()
	if _, err := database.ExecContext(ctx, `INSERT INTO agents (public_id, name, token_hash, enabled) VALUES ('agent-old', 'Old agent', 'hash', 1)`); err != nil {
		t.Fatalf("insert incompatible agent: %v", err)
	}
	disabledResult, err := database.ExecContext(ctx, `INSERT INTO agents (public_id, name, token_hash, enabled) VALUES ('agent-disabled', 'Disabled', 'hash', 0)`)
	if err != nil {
		t.Fatalf("insert disabled agent: %v", err)
	}
	disabledID, _ := disabledResult.LastInsertId()
	tlsConfig, enabled, err := NewManagementTLSConfig(cfg)
	if err != nil {
		t.Fatalf("create management TLS config: %v", err)
	}
	runtime, err := NewManagementTLSRuntime(cfg, database, tlsConfig, enabled)
	if err != nil {
		t.Fatalf("create management TLS runtime: %v", err)
	}
	if err := runtime.generateAndStage(); err != nil {
		t.Fatalf("generate and stage: %v", err)
	}
	stagedCertFile := runtime.state.StagedCertFile
	stagedKeyFile := runtime.state.StagedKeyFile
	staged, err := runtime.snapshot(ctx, nil)
	if err != nil {
		t.Fatalf("snapshot staged rotation: %v", err)
	}
	if _, digest := runtime.currentAgentCA(); digest != staged.RolloutBundleSha256 {
		t.Fatalf("published staged agent CA digest = %q, want %q", digest, staged.RolloutBundleSha256)
	}
	dualReady := &p2pstreamv1.ManagementTrustStatus{
		InstalledGeneration:   staged.RolloutGeneration,
		InstalledBundleSha256: staged.RolloutBundleSha256,
		State:                 p2pstreamv1.ManagementTrustInstallState_MANAGEMENT_TRUST_INSTALL_STATE_READY,
		AgentVersion:          "test",
		Capabilities:          []string{managementTrustCapability},
	}
	if err := runtime.recordTrustReport(ctx, compatibleID, dualReady); err != nil {
		t.Fatalf("record dual trust report: %v", err)
	}
	if err := runtime.recordTrustReport(ctx, disabledID, dualReady); err != nil {
		t.Fatalf("record disabled dual trust report: %v", err)
	}

	if err := runtime.cancel(); err != nil {
		t.Fatalf("cancel rotation: %v", err)
	}
	cleanup, err := runtime.snapshot(ctx, nil)
	if err != nil {
		t.Fatalf("snapshot cleanup: %v", err)
	}
	if cleanup.Phase != p2pstreamv1.ManagementTlsRotationPhase_MANAGEMENT_TLS_ROTATION_PHASE_CLEANING_UP {
		t.Fatalf("cancel phase = %s, want cleaning up", cleanup.Phase)
	}
	if cleanup.CleanupReason != p2pstreamv1.ManagementTlsCleanupReason_MANAGEMENT_TLS_CLEANUP_REASON_CANCELLED_STAGING {
		t.Fatalf("cleanup reason = %s, want cancelled staging", cleanup.CleanupReason)
	}
	if cleanup.BlockingAgentCount != 1 {
		t.Fatalf("cleanup blockers = %d, want only the compatible agent", cleanup.BlockingAgentCount)
	}
	for _, path := range []string{stagedCertFile, stagedKeyFile} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("cancelled staged file still exists at %q: %v", path, err)
		}
	}
	if err := runtime.finalizeCleanup(ctx, false); err == nil {
		t.Fatal("cleanup finalized without compatible agent acknowledgement")
	}
	update := runtime.trustUpdate(dualReady)
	if update == nil || update.Generation != cleanup.RolloutGeneration || update.BundleSha256 != cleanup.RolloutBundleSha256 {
		t.Fatalf("unexpected cleanup update: %+v", update)
	}
	cleanupReady := &p2pstreamv1.ManagementTrustStatus{
		InstalledGeneration:   cleanup.RolloutGeneration,
		InstalledBundleSha256: cleanup.RolloutBundleSha256,
		State:                 p2pstreamv1.ManagementTrustInstallState_MANAGEMENT_TRUST_INSTALL_STATE_READY,
		AgentVersion:          "test",
		Capabilities:          []string{managementTrustCapability},
	}
	if err := runtime.recordTrustReport(ctx, compatibleID, cleanupReady); err != nil {
		t.Fatalf("record cleanup trust report: %v", err)
	}
	if err := runtime.finalizeCleanup(ctx, false); err != nil {
		t.Fatalf("finalize cleanup: %v", err)
	}
	idle, err := runtime.snapshot(ctx, nil)
	if err != nil {
		t.Fatalf("snapshot idle trust health: %v", err)
	}
	if idle.Phase != p2pstreamv1.ManagementTlsRotationPhase_MANAGEMENT_TLS_ROTATION_PHASE_IDLE || !idle.TrustManagementActive {
		t.Fatalf("unexpected idle trust state: %+v", idle)
	}
	if _, digest := runtime.currentAgentCA(); digest != idle.DesiredTrustBundleSha256 {
		t.Fatalf("published idle agent CA digest = %q, want %q", digest, idle.DesiredTrustBundleSha256)
	}
	if idle.TrustAttentionAgentCount != 2 {
		t.Fatalf("idle trust attention count = %d, want incompatible and disabled agents retained", idle.TrustAttentionAgentCount)
	}
	foundDisabledAttention := false
	for _, agent := range idle.Agents {
		if agent.AgentId == disabledID {
			foundDisabledAttention = agent.State == p2pstreamv1.ManagementTlsAgentRolloutState_MANAGEMENT_TLS_AGENT_ROLLOUT_STATE_DISABLED && agent.NeedsTrustAttention
		}
	}
	if !foundDisabledAttention {
		t.Fatal("disabled stale agent was not retained as a trust-attention item")
	}
	if update := runtime.trustUpdate(dualReady); update == nil || update.BundleSha256 != idle.DesiredTrustBundleSha256 {
		t.Fatalf("idle server did not retain active trust repair target: %+v", update)
	}
}

func TestManagementTLSRuntimeRollbackCleansManagedCertificateFiles(t *testing.T) {
	ctx := context.Background()
	cfg := managementTLSTestConfig(t)
	database, err := db.Open(filepath.Join(t.TempDir(), "rollback-cleanup.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	tlsConfig, enabled, err := NewManagementTLSConfig(cfg)
	if err != nil {
		t.Fatalf("create management TLS config: %v", err)
	}
	runtime, err := NewManagementTLSRuntime(cfg, database, tlsConfig, enabled)
	if err != nil {
		t.Fatalf("create management TLS runtime: %v", err)
	}
	if err := runtime.generateAndStage(); err != nil {
		t.Fatalf("generate and stage: %v", err)
	}
	if err := runtime.activate(ctx, false); err != nil {
		t.Fatalf("activate: %v", err)
	}
	retiredCertFile := runtime.state.ActiveCertFile
	retiredKeyFile := runtime.state.ActiveKeyFile
	if err := runtime.rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	snapshot, err := runtime.snapshot(ctx, nil)
	if err != nil {
		t.Fatalf("snapshot rollback cleanup: %v", err)
	}
	if snapshot.Phase != p2pstreamv1.ManagementTlsRotationPhase_MANAGEMENT_TLS_ROTATION_PHASE_CLEANING_UP {
		t.Fatalf("rollback phase = %s, want cleaning up", snapshot.Phase)
	}
	for _, path := range []string{retiredCertFile, retiredKeyFile} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("rolled-back managed file still exists at %q: %v", path, err)
		}
	}
	if err := runtime.finalizeCleanup(ctx, false); err != nil {
		t.Fatalf("finalize empty-fleet rollback cleanup: %v", err)
	}
}

func TestManagementTLSRuntimeRetirementDeletesPreviousManagedKey(t *testing.T) {
	ctx := context.Background()
	cfg := managementTLSTestConfig(t)
	database, err := db.Open(filepath.Join(t.TempDir(), "retired-key-cleanup.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	tlsConfig, enabled, err := NewManagementTLSConfig(cfg)
	if err != nil {
		t.Fatalf("create management TLS config: %v", err)
	}
	runtime, err := NewManagementTLSRuntime(cfg, database, tlsConfig, enabled)
	if err != nil {
		t.Fatalf("create management TLS runtime: %v", err)
	}
	finishRotation := func() {
		t.Helper()
		if err := runtime.generateAndStage(); err != nil {
			t.Fatalf("generate and stage: %v", err)
		}
		if err := runtime.activate(ctx, false); err != nil {
			t.Fatalf("activate: %v", err)
		}
		if err := runtime.beginRetirement(ctx); err != nil {
			t.Fatalf("begin retirement: %v", err)
		}
		if err := runtime.finalizeRetirement(ctx, false); err != nil {
			t.Fatalf("finalize retirement: %v", err)
		}
	}
	finishRotation()
	firstManagedCert := runtime.state.ActiveCertFile
	firstManagedKey := runtime.state.ActiveKeyFile
	finishRotation()
	for _, path := range []string{firstManagedCert, firstManagedKey} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("retired managed TLS file still exists at %q: %v", path, err)
		}
	}
}

func TestManagementTLSRuntimeRejectsInconsistentPersistedState(t *testing.T) {
	cfg := managementTLSTestConfig(t)
	database, err := db.Open(filepath.Join(t.TempDir(), "invalid-state.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	tlsConfig, enabled, err := NewManagementTLSConfig(cfg)
	if err != nil {
		t.Fatalf("create management TLS config: %v", err)
	}
	runtime, err := NewManagementTLSRuntime(cfg, database, tlsConfig, enabled)
	if err != nil {
		t.Fatalf("create management TLS runtime: %v", err)
	}
	state := runtime.state
	state.Phase = "cleaning"
	state.CleanupReason = ""
	state.RequiresTrustRollout = true
	state.RolloutGeneration = 2
	state.RolloutCAPEM = state.ActiveCAPEM
	state.RolloutBundleSHA256 = managementCAPEMSHA256(state.ActiveCAPEM)
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal invalid state: %v", err)
	}
	if err := os.WriteFile(runtime.stateFile, raw, 0600); err != nil {
		t.Fatalf("write invalid state: %v", err)
	}
	restartedConfig, restartedEnabled, err := NewManagementTLSConfig(cfg)
	if err != nil {
		t.Fatalf("create restarted management TLS config: %v", err)
	}
	if _, err := NewManagementTLSRuntime(cfg, database, restartedConfig, restartedEnabled); err == nil {
		t.Fatal("inconsistent persisted cleanup state was accepted")
	}
}

func TestManagementTLSRuntimeForcedCleanupRetainsIdleAttentionUntilReconciled(t *testing.T) {
	ctx := context.Background()
	cfg := managementTLSTestConfig(t)
	database, err := db.Open(filepath.Join(t.TempDir(), "forced-cleanup.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	result, err := database.ExecContext(ctx, `INSERT INTO agents (public_id, name, token_hash, enabled) VALUES ('agent-force-cleanup', 'Force cleanup', 'hash', 1)`)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	agentID, _ := result.LastInsertId()
	tlsConfig, enabled, err := NewManagementTLSConfig(cfg)
	if err != nil {
		t.Fatalf("create management TLS config: %v", err)
	}
	runtime, err := NewManagementTLSRuntime(cfg, database, tlsConfig, enabled)
	if err != nil {
		t.Fatalf("create management TLS runtime: %v", err)
	}
	if err := runtime.generateAndStage(); err != nil {
		t.Fatalf("generate and stage: %v", err)
	}
	staged, err := runtime.snapshot(ctx, nil)
	if err != nil {
		t.Fatalf("snapshot staged rotation: %v", err)
	}
	dualReady := &p2pstreamv1.ManagementTrustStatus{InstalledGeneration: staged.RolloutGeneration, InstalledBundleSha256: staged.RolloutBundleSha256, State: p2pstreamv1.ManagementTrustInstallState_MANAGEMENT_TRUST_INSTALL_STATE_READY, AgentVersion: "test", Capabilities: []string{managementTrustCapability}}
	if err := runtime.recordTrustReport(ctx, agentID, dualReady); err != nil {
		t.Fatalf("record dual trust report: %v", err)
	}
	if err := runtime.cancel(); err != nil {
		t.Fatalf("cancel rotation: %v", err)
	}
	if err := runtime.finalizeCleanup(ctx, true); err != nil {
		t.Fatalf("force cleanup: %v", err)
	}
	idle, err := runtime.snapshot(ctx, nil)
	if err != nil {
		t.Fatalf("snapshot forced cleanup: %v", err)
	}
	if !idle.ForcedCleanup || idle.TrustAttentionAgentCount != 1 || idle.Agents[0].State == p2pstreamv1.ManagementTlsAgentRolloutState_MANAGEMENT_TLS_AGENT_ROLLOUT_STATE_STRANDED {
		t.Fatalf("forced cleanup did not retain a non-stranded attention item: %+v", idle)
	}
	update := runtime.trustUpdate(dualReady)
	if update == nil || update.BundleSha256 != idle.DesiredTrustBundleSha256 {
		t.Fatalf("forced cleanup did not retain repair target: %+v", update)
	}
	reconciled := &p2pstreamv1.ManagementTrustStatus{InstalledGeneration: update.Generation, InstalledBundleSha256: update.BundleSha256, State: p2pstreamv1.ManagementTrustInstallState_MANAGEMENT_TRUST_INSTALL_STATE_READY, AgentVersion: "test", Capabilities: []string{managementTrustCapability}}
	if err := runtime.recordTrustReport(ctx, agentID, reconciled); err != nil {
		t.Fatalf("record reconciled trust: %v", err)
	}
	idle, err = runtime.snapshot(ctx, nil)
	if err != nil {
		t.Fatalf("snapshot reconciled cleanup: %v", err)
	}
	if idle.TrustAttentionAgentCount != 0 {
		t.Fatalf("reconciled forced cleanup still has %d attention agents", idle.TrustAttentionAgentCount)
	}
}

func TestManagementTLSRuntimeLeafReplacementUnderSameCADoesNotBlockOnAgents(t *testing.T) {
	ctx := context.Background()
	cfg := managementTLSTestConfig(t)
	database, err := db.Open(filepath.Join(t.TempDir(), "leaf-rotation.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.ExecContext(ctx, `INSERT INTO agents (public_id, name, token_hash, enabled) VALUES ('old-agent', 'Old agent', 'hash', 1)`); err != nil {
		t.Fatalf("insert incompatible agent: %v", err)
	}
	tlsConfig, enabled, err := NewManagementTLSConfig(cfg)
	if err != nil {
		t.Fatalf("create management TLS config: %v", err)
	}
	runtime, err := NewManagementTLSRuntime(cfg, database, tlsConfig, enabled)
	if err != nil {
		t.Fatalf("create management TLS runtime: %v", err)
	}
	caCertPEM, err := os.ReadFile(filepath.Join(cfg.CertsDir, managementCertDirName, managementCACertFileName))
	if err != nil {
		t.Fatalf("read CA certificate: %v", err)
	}
	caKeyPEM, err := os.ReadFile(filepath.Join(cfg.CertsDir, managementCertDirName, managementCAKeyFileName))
	if err != nil {
		t.Fatalf("read CA key: %v", err)
	}
	caCert, caKey, err := parseManagementCA(caCertPEM, caKeyPEM)
	if err != nil {
		t.Fatalf("parse CA: %v", err)
	}
	certPEM, keyPEM, err := generateManagementServerCertificatePEM(caCert, caKey, managementTLSHosts(cfg, detectManagementAdvertiseHost(cfg)))
	if err != nil {
		t.Fatalf("generate replacement leaf: %v", err)
	}
	if err := runtime.stage(string(certPEM), string(keyPEM), string(caCertPEM), ""); err != nil {
		t.Fatalf("stage same-CA leaf: %v", err)
	}
	snapshot, err := runtime.snapshot(ctx, nil)
	if err != nil {
		t.Fatalf("snapshot staged leaf: %v", err)
	}
	if snapshot.BlockingAgentCount != 0 {
		t.Fatalf("same-CA leaf replacement has %d blockers, want 0", snapshot.BlockingAgentCount)
	}
	if err := runtime.activate(ctx, false); err != nil {
		t.Fatalf("activate same-CA leaf: %v", err)
	}
	snapshot, err = runtime.snapshot(ctx, nil)
	if err != nil {
		t.Fatalf("snapshot active leaf: %v", err)
	}
	if snapshot.RolloutGeneration != 0 {
		t.Fatalf("same-CA active rollout generation = %d, want 0", snapshot.RolloutGeneration)
	}
	if err := runtime.beginRetirement(ctx); err != nil {
		t.Fatalf("finish same-CA rotation: %v", err)
	}
	snapshot, err = runtime.snapshot(ctx, nil)
	if err != nil {
		t.Fatalf("snapshot finished leaf: %v", err)
	}
	if snapshot.Phase != p2pstreamv1.ManagementTlsRotationPhase_MANAGEMENT_TLS_ROTATION_PHASE_IDLE {
		t.Fatalf("finished leaf phase = %s, want idle", snapshot.Phase)
	}
}
