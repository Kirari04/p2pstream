package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"p2pstream/internal/db"
)

func TestRandomAgentPublicIDFormatAndUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for range 1000 {
		publicID, err := randomAgentPublicID()
		if err != nil {
			t.Fatalf("generate agent public id: %v", err)
		}
		if _, err := validateGeneratedAgentPublicID(publicID); err != nil {
			t.Fatalf("generated public id %q did not validate: %v", publicID, err)
		}
		if publicID != strings.ToLower(publicID) {
			t.Fatalf("generated public id %q is not lower-case", publicID)
		}
		if len(publicID) != len(agentPublicIDPrefix)+agentPublicIDEncodedBytes {
			t.Fatalf("generated public id %q length = %d, want %d", publicID, len(publicID), len(agentPublicIDPrefix)+agentPublicIDEncodedBytes)
		}
		if seen[publicID] {
			t.Fatalf("generated duplicate public id %q", publicID)
		}
		seen[publicID] = true
	}
}

func TestCreateAgentWithGeneratedPublicIDCollisionRetry(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	existingID := "agent-aaaaaaaaaaaaaaaaaaaaaaaaaa"
	nextID := "agent-bbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := database.CreateAgent(context.Background(), db.CreateAgentParams{
		PublicID:  existingID,
		Name:      "Existing Agent",
		TokenHash: hashAgentToken("existing-token"),
		Enabled:   1,
	}); err != nil {
		t.Fatalf("seed existing agent: %v", err)
	}

	oldGenerator := newAgentPublicID
	attempts := 0
	newAgentPublicID = func() (string, error) {
		attempts++
		if attempts == 1 {
			return existingID, nil
		}
		return nextID, nil
	}
	t.Cleanup(func() {
		newAgentPublicID = oldGenerator
	})

	app := NewApp(nil, database)
	agent, err := app.createAgentWithGeneratedPublicID(context.Background(), "Retry Agent", hashAgentToken("retry-token"), 1)
	if err != nil {
		t.Fatalf("create with retry: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("generator attempts = %d, want 2", attempts)
	}
	if agent.PublicID != nextID {
		t.Fatalf("public id = %q, want %q", agent.PublicID, nextID)
	}
}

func TestCreateAgentWithGeneratedPublicIDCollisionFailure(t *testing.T) {
	database := newAgentRegistryTestDB(t)
	existingID := "agent-cccccccccccccccccccccccccc"
	if _, err := database.CreateAgent(context.Background(), db.CreateAgentParams{
		PublicID:  existingID,
		Name:      "Existing Agent",
		TokenHash: hashAgentToken("existing-token"),
		Enabled:   1,
	}); err != nil {
		t.Fatalf("seed existing agent: %v", err)
	}

	oldGenerator := newAgentPublicID
	attempts := 0
	newAgentPublicID = func() (string, error) {
		attempts++
		return existingID, nil
	}
	t.Cleanup(func() {
		newAgentPublicID = oldGenerator
	})

	app := NewApp(nil, database)
	if _, err := app.createAgentWithGeneratedPublicID(context.Background(), "Fail Agent", hashAgentToken("fail-token"), 1); connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("expected internal error after repeated collisions, got %v", err)
	}
	if attempts != agentPublicIDMaxAttempts {
		t.Fatalf("generator attempts = %d, want %d", attempts, agentPublicIDMaxAttempts)
	}
}

func newAgentRegistryTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "agent-registry-test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close test db: %v", err)
		}
	})
	return database
}
