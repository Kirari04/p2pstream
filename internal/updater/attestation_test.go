package updater

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"p2pstream/internal/agentupdateauth"
)

func TestParseArtifactDigestRequiresExactSHA256(t *testing.T) {
	want := sha256.Sum256([]byte("artifact"))
	for _, value := range []string{hex.EncodeToString(want[:]), strings.ToUpper(hex.EncodeToString(want[:]))} {
		got, err := parseArtifactDigest(value)
		if err != nil || got != want {
			t.Fatalf("parse digest=%x, err=%v, want=%x", got, err, want)
		}
	}
	for _, value := range []string{"", "0", strings.Repeat("a", 62), strings.Repeat("a", 66),
		strings.Repeat("a", 63) + "g", strings.Repeat("a", 64) + "gg", "sha256:" + strings.Repeat("a", 64)} {
		got, err := parseArtifactDigest(value)
		if err == nil || got != [sha256.Size]byte{} {
			t.Fatalf("invalid digest %q produced %x, err=%v", value, got, err)
		}
	}
}

func TestPersistHealthyActivationRejectsMalformedReceiptBeforeWrites(t *testing.T) {
	for _, value := range []string{"", strings.Repeat("a", 62), strings.Repeat("a", 66), strings.Repeat("a", 63) + "g"} {
		t.Run(value, func(t *testing.T) {
			f := newFixture(t)
			authorizationDigest, err := agentupdateauth.AssignmentAuthorizationDigest(f.authorization.Authorization)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := createRootActionReceipt(f.paths, f.authorization, hex.EncodeToString(authorizationDigest[:]), releaseSlotMetadata(f.release))
			if err != nil {
				t.Fatal(err)
			}
			// Mutating the signed receipt makes it malformed: receipt validation
			// rejects it before parseArtifactDigest, which is tested directly above.
			receipt.Receipt.ResultArtifactSHA256 = value
			journal := activationJournal{Phase: journalHealthy, Authorization: f.authorization, Receipt: &receipt,
				Version: f.release.Version, Sequence: f.release.Sequence, SecurityEpoch: f.release.SecurityEpoch,
				MinimumSafeVersion: f.release.MinimumSafeVersion, PreviousSlot: f.bootstrap,
				CandidateTarget: "slots/" + f.release.Version + "/p2pstream"}
			paths := []string{f.paths.lastActivationPath(), f.paths.rootActionReceiptPath(), f.paths.rootActionCounterPath(),
				f.paths.floorPath(), f.paths.previousSlotPath(), f.paths.currentSlotMetadataPath(), f.paths.readyPath(), f.paths.journalPath()}
			before := make(map[string][]byte)
			for _, path := range paths {
				data, err := os.ReadFile(path)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
				if err == nil {
					before[path] = data
				}
			}
			if err := persistHealthyActivation(f.paths, journal, f.paths.readyPath(), nil); err == nil || err.Error() != "artifact is invalid" {
				t.Fatalf("malformed receipt: got %v, want artifact is invalid", err)
			}
			for _, path := range paths {
				data, err := os.ReadFile(path)
				previous, existed := before[path]
				if existed {
					if err != nil || !bytes.Equal(data, previous) {
						t.Fatalf("invalid receipt changed %s: %v", path, err)
					}
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("invalid receipt created %s: %v", path, err)
				}
			}
		})
	}
}
