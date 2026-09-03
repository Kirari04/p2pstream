package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"p2pstream/internal/agentupdateauthority"
)

func TestInitializeAgentUpdateManagementAuthorityPinsAndReloadsIdentity(t *testing.T) {
	database := newServerTestDB(t)
	directory := secureAgentUpdateTestDirectory(t)
	path := filepath.Join(directory, "management-authority.json")

	first, err := InitializeAgentUpdateManagementAuthority(context.Background(), database, path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := InitializeAgentUpdateManagementAuthority(context.Background(), database, path)
	if err != nil {
		t.Fatal(err)
	}
	if first.Identity().KeyID != second.Identity().KeyID || first.Identity().Epoch != second.Identity().Epoch {
		t.Fatalf("reloaded authority changed identity: first=%+v second=%+v", first.Identity(), second.Identity())
	}

	var keyID string
	var epoch int64
	if err := database.QueryRow(`SELECT key_id,epoch FROM agent_update_management_authority WHERE id=1`).Scan(&keyID, &epoch); err != nil {
		t.Fatal(err)
	}
	if keyID != first.Identity().KeyID || epoch != int64(first.Identity().Epoch) {
		t.Fatalf("database pin = %q/%d, want %q/%d", keyID, epoch, first.Identity().KeyID, first.Identity().Epoch)
	}
}

func TestInitializeAgentUpdateManagementAuthorityRejectsMissingOrReplacedPinnedKey(t *testing.T) {
	database := newServerTestDB(t)
	directory := secureAgentUpdateTestDirectory(t)
	path := filepath.Join(directory, "management-authority.json")
	if _, err := InitializeAgentUpdateManagementAuthority(context.Background(), database, path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if _, err := InitializeAgentUpdateManagementAuthority(context.Background(), database, path); !errors.Is(err, agentupdateauthority.ErrKeyMissing) {
		t.Fatalf("missing pinned key error = %v", err)
	}
	if _, err := agentupdateauthority.Generate(path, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := InitializeAgentUpdateManagementAuthority(context.Background(), database, path); !errors.Is(err, agentupdateauthority.ErrKeyMismatch) {
		t.Fatalf("replaced pinned key error = %v", err)
	}

	app := NewApp(nil, database)
	app.SetAgentUpdateManagementAuthority(nil, agentupdateauthority.ErrKeyMismatch)
	if _, _, err := app.requireAgentUpdateAuthority(); err == nil {
		t.Fatal("managed updates remained enabled without the pinned authority")
	}
	if app.AgentUpdateAuthorityWarning == "" {
		t.Fatal("authority failure was not exposed as a bounded operational warning")
	}
}

func TestInitializeAgentUpdateManagementAuthorityRecoversOnlyPristineInstall(t *testing.T) {
	t.Run("pristine key file", func(t *testing.T) {
		database := newServerTestDB(t)
		directory := secureAgentUpdateTestDirectory(t)
		path := filepath.Join(directory, "management-authority.json")
		existing, err := agentupdateauthority.Generate(path, 1)
		if err != nil {
			t.Fatal(err)
		}
		recovered, err := InitializeAgentUpdateManagementAuthority(context.Background(), database, path)
		if err != nil {
			t.Fatal(err)
		}
		if existing.Identity().KeyID != recovered.Identity().KeyID {
			t.Fatal("first-install recovery pinned a different key")
		}
	})

	t.Run("non-pristine database", func(t *testing.T) {
		database := newServerTestDB(t)
		agent := createAgentUpdateTestAgent(t, database, "authority-missing-pin")
		insertAgentUpdateTestCampaign(t, database, agent.ID, "pending", "stage", false)
		directory := secureAgentUpdateTestDirectory(t)
		path := filepath.Join(directory, "management-authority.json")
		if _, err := InitializeAgentUpdateManagementAuthority(context.Background(), database, path); err == nil {
			t.Fatal("non-pristine managed-update database silently established a new authority")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("authority key was created despite missing database trust pin: %v", err)
		}
	})
}

func secureAgentUpdateTestDirectory(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}
