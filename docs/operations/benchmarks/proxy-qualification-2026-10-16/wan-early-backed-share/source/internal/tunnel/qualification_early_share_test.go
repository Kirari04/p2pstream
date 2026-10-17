//go:build linux && p2pstream_qualification

package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Serial tests swap an isolated recorder. This test-only reset has no exported
// or runtime API and is never called by the endpoint fixture.
func setQualificationEarlyShareTestMode(enabled, configured bool) func() {
	previous := qualificationEarlyShareState.Swap(&qualificationEarlyShareRecorder{
		enabled: enabled, configured: configured,
	})
	return func() { qualificationEarlyShareState.Store(previous) }
}

func earlyShareFixtureForTest(t *testing.T, role, mode string) (func(bool, time.Time) error, string) {
	t.Helper()
	t.Cleanup(setQualificationEarlyShareTestMode(false, false))
	directory := t.TempDir()
	t.Setenv(qualificationEarlyShareEnv, mode)
	t.Setenv(qualificationEarlyShareDirectoryEnv, directory)
	finish, err := StartQualificationEarlyShareFixture(role)
	if err != nil {
		t.Fatal(err)
	}
	return finish, directory
}

func earlyShareTerminalForTest(t *testing.T, directory, role string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, "early-share-"+role+"-terminal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestQualificationEarlyShareStartupAndTerminalAreExclusive(t *testing.T) {
	finish, directory := earlyShareFixtureForTest(t, "agent", "0")
	if enabled, configured := qualificationEarlyShareMode(); enabled || !configured {
		t.Fatal("disabled startup was not actually configured")
	}
	if _, err := StartQualificationEarlyShareFixture("agent"); err == nil {
		t.Fatal("repeated configuration accepted")
	}
	if err := finish(true, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if value := earlyShareTerminalForTest(t, directory, "agent"); value["complete"] != true || value["selected_policy"] != false {
		t.Fatal("startup/terminal policy or completion differs")
	}
	if err := finish(true, time.Time{}); err == nil {
		t.Fatal("second terminal drain accepted")
	}
}

func TestQualificationEarlyShareRejectsMalformedLocalSelector(t *testing.T) {
	for _, mode := range []string{"true", "1 ", "-1", "2", ""} {
		t.Run(mode, func(t *testing.T) {
			t.Cleanup(setQualificationEarlyShareTestMode(false, false))
			t.Setenv(qualificationEarlyShareEnv, mode)
			t.Setenv(qualificationEarlyShareDirectoryEnv, t.TempDir())
			if _, err := StartQualificationEarlyShareFixture("agent"); err == nil {
				t.Fatal("invalid/missing exact selector accepted")
			}
		})
	}
}

func TestQualificationEarlyShareSealPermanentlyRejectsActiveAttempt(t *testing.T) {
	finish, directory := earlyShareFixtureForTest(t, "agent", "1")
	qualificationEarlyShareAttemptStarted()
	if err := finish(true, time.Time{}); err == nil {
		t.Fatal("in-flight attempt accepted as complete")
	}
	value := earlyShareTerminalForTest(t, directory, "agent")
	if value["complete"] != false || value["active_attempts"] != float64(1) {
		t.Fatal("incomplete terminal proof lost the seal-time active attempt")
	}
}

func TestQualificationEarlyShareLateBeginIsFatalBeforePublication(t *testing.T) {
	const child = "P2PSTREAM_EARLY_SHARE_LATE_BEGIN_TEST_CHILD"
	if os.Getenv(child) == "1" {
		finish, _ := earlyShareFixtureForTest(t, "agent", "1")
		if err := finish(true, time.Time{}); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if recover() != nil {
				_, _ = os.Stderr.WriteString("late invariant was recoverable\n")
			}
		}()
		qualificationEarlyShareAttemptStarted()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestQualificationEarlyShareLateBeginIsFatalBeforePublication$", "-test.count=1")
	command.Env = append(os.Environ(), child+"=1")
	output, err := command.CombinedOutput()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 87 || ctx.Err() != nil {
		t.Fatalf("late invariant exit=%v output=%q", err, output)
	}
	if string(output) != "early-share diagnostic invariant failed\n" || strings.Contains(string(output), "recoverable") {
		t.Fatalf("late invariant did not emit the bounded fatal marker: %q", output)
	}
}

func TestQualificationEarlyShareNeverClosesLiveSessionForProof(t *testing.T) {
	finish, directory := earlyShareFixtureForTest(t, "server", "0")
	_, server, _, reader := earlySharePair(t)
	c := &GrowingConn{stream: reader}
	qualificationEarlyShareAttemptStarted()
	c.recordQualificationEarlyShare(time.Now(), BackedEarlyShareObservation{Outcome: "disabled"})
	if err := finish(true, time.Now().Add(time.Second)); err == nil {
		t.Fatal("live event session accepted at terminal")
	}
	if server.IsClosed() {
		t.Fatal("observer closed a live session to manufacture proof")
	}
	if value := earlyShareTerminalForTest(t, directory, "server"); value["complete"] != false || value["event_sessions_live_before_join"] != float64(1) {
		t.Fatal("live session rejection was not preserved")
	}
}

func TestQualificationEarlyShareOverflowIsBoundedAndRejects(t *testing.T) {
	finish, directory := earlyShareFixtureForTest(t, "agent", "0")
	client, server, _, reader := earlySharePair(t)
	for range qualificationEarlyShareCapacity + 1 {
		c := &GrowingConn{stream: reader}
		qualificationEarlyShareAttemptStarted()
		c.recordQualificationEarlyShare(time.Now(), BackedEarlyShareObservation{Outcome: "disabled"})
	}
	_ = client.Close()
	_ = server.Close()
	if err := finish(true, time.Time{}); err == nil {
		t.Fatal("overflow accepted")
	}
	value := earlyShareTerminalForTest(t, directory, "agent")
	if value["complete"] != false || value["dropped_events"] != float64(1) || len(value["records"].([]any)) != qualificationEarlyShareCapacity {
		t.Fatal("overflow lost its bound or rejection evidence")
	}
}
