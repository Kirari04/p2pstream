//go:build linux && p2pstream_qualification

package tunnel

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hashicorp/yamux"
)

const qualificationEarlyShareCapacity = 64
const qualificationEarlyShareEnv = "P2PSTREAM_QUALIFICATION_EARLY_SHARE"
const qualificationEarlyShareDirectoryEnv = "P2PSTREAM_QUALIFICATION_EARLY_SHARE_PROOF_DIR"

type qualificationEarlyShareProcess struct {
	PID             int    `json:"pid"`
	StartTicks      uint64 `json:"start_ticks"`
	Namespace       string `json:"namespace"`
	NamespaceDevice uint64 `json:"namespace_device"`
	NamespaceInode  uint64 `json:"namespace_inode"`
	PIDNamespace    string `json:"pid_namespace"`
	BootID          string `json:"boot_id"`
}

type qualificationEarlyShareRecord struct {
	Ordinal         uint64                      `json:"ordinal"`
	SampleUnixNS    int64                       `json:"sample_unix_ns"`
	WrapperIdentity string                      `json:"wrapper_identity"`
	SessionIdentity string                      `json:"session_identity"`
	StreamID        uint32                      `json:"stream_id"`
	LocalAddress    string                      `json:"local_address"`
	RemoteAddress   string                      `json:"remote_address"`
	Observation     BackedEarlyShareObservation `json:"observation"`
	// Retain the wrapper and session through the one terminal drain. Pointer
	// identities cannot be recycled while a bounded proof record owns them.
	wrapper *GrowingConn
	session *yamux.Session
}

type qualificationEarlyShareRecorder struct {
	mu         sync.Mutex
	configured bool
	enabled    bool
	sealing    bool
	sealed     bool
	started    uint64
	completed  uint64
	active     uint64
	dropped    uint64
	count      int
	records    [qualificationEarlyShareCapacity]qualificationEarlyShareRecord
}

var qualificationEarlyShareState atomic.Pointer[qualificationEarlyShareRecorder]

func init() {
	qualificationEarlyShareState.Store(&qualificationEarlyShareRecorder{})
}

func qualificationEarlyShareMode() (bool, bool) {
	r := qualificationEarlyShareState.Load()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enabled, r.configured
}

// HTTP handlers may recover panics. This tagged invariant must reject the whole
// endpoint even if a terminal sidecar was already written; no publication follows.
func qualificationEarlyShareInvariantFailure() {
	_, _ = os.Stderr.WriteString("early-share diagnostic invariant failed\n")
	os.Exit(87)
}

func qualificationEarlyShareAttemptStarted() {
	r := qualificationEarlyShareState.Load()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.configured || r.sealing || r.sealed {
		// A late attempt after a supposedly complete dump invalidates the
		// process, rather than silently leaving an accepted terminal proof.
		qualificationEarlyShareInvariantFailure()
	}
	r.started++
	r.active++
}

func (c *GrowingConn) recordQualificationEarlyShare(now time.Time, observation BackedEarlyShareObservation) {
	r := qualificationEarlyShareState.Load()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.configured || r.sealed || r.active == 0 {
		qualificationEarlyShareInvariantFailure()
	}
	r.active--
	r.completed++
	if r.count == len(r.records) {
		r.dropped++
		return
	}
	session := c.stream.Session()
	r.records[r.count] = qualificationEarlyShareRecord{
		Ordinal: r.completed, SampleUnixNS: now.UnixNano(),
		WrapperIdentity: fmt.Sprintf("%p", c), SessionIdentity: fmt.Sprintf("%p", session),
		StreamID: c.stream.StreamID(), LocalAddress: session.LocalAddr().String(),
		RemoteAddress: session.RemoteAddr().String(), Observation: observation,
		wrapper: c, session: session,
	}
	r.count++
}

func qualificationEarlyShareIdentity() (qualificationEarlyShareProcess, error) {
	var result qualificationEarlyShareProcess
	result.PID = os.Getpid()
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return result, err
	}
	text := string(data)
	end := strings.LastIndexByte(text, ')')
	if end < 0 {
		return result, errors.New("early-share process stat malformed")
	}
	fields := strings.Fields(text[end+1:])
	if len(fields) <= 19 {
		return result, errors.New("early-share process stat truncated")
	}
	result.StartTicks, err = strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return result, err
	}
	if result.Namespace, err = os.Readlink("/proc/self/ns/net"); err != nil {
		return result, err
	}
	if result.PIDNamespace, err = os.Readlink("/proc/self/ns/pid"); err != nil {
		return result, err
	}
	info, err := os.Stat("/proc/self/ns/net")
	if err != nil {
		return result, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return result, errors.New("early-share namespace stat unavailable")
	}
	result.NamespaceDevice, result.NamespaceInode = uint64(stat.Dev), stat.Ino
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return result, err
	}
	result.BootID = strings.TrimSpace(string(boot))
	return result, nil
}

func qualificationEarlyShareWrite(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > 256<<10 {
		return errors.New("early-share sidecar exceeds fixed 256KiB bound")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// StartQualificationEarlyShareFixture is called by the tagged external fixture
// before endpoint creation. No public handler or production option calls it.
// The returned terminal writer is called once after existing shutdown/join.
func StartQualificationEarlyShareFixture(role string) (func(bool, time.Time) error, error) {
	noop := func(bool, time.Time) error { return nil }
	raw, directory := os.Getenv(qualificationEarlyShareEnv), os.Getenv(qualificationEarlyShareDirectoryEnv)
	if raw == "" && directory == "" {
		return noop, nil // Unrelated existing qualification fixtures stay inert.
	}
	if (raw != "0" && raw != "1") || !filepath.IsAbs(directory) || (role != "agent" && role != "server") {
		return nil, errors.New("early-share requires exact local0/1, absolute proof directory and endpoint role")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("early-share proof directory unavailable or aliased")
	}
	process, err := qualificationEarlyShareIdentity()
	if err != nil {
		return nil, err
	}
	r := qualificationEarlyShareState.Load()
	r.mu.Lock()
	if r.configured || r.started != 0 || r.sealing || r.sealed {
		r.mu.Unlock()
		return nil, errors.New("early-share startup configuration repeated or preceded by an attempt")
	}
	r.enabled, r.configured = raw == "1", true
	r.mu.Unlock()
	startup := map[string]any{"schema_version": 1, "phase": "startup-before-endpoints", "role": role,
		"selected_policy": r.enabled, "process": process, "observed_unix_ns": time.Now().UnixNano(),
		"threshold_bytes": earlyShareThreshold, "maximum_target_bytes": earlyShareAbsoluteMaximum,
		"capacity": qualificationEarlyShareCapacity}
	if err := qualificationEarlyShareWrite(filepath.Join(directory, "early-share-"+role+"-startup.json"), startup); err != nil {
		return nil, err
	}
	return func(joined bool, deadline time.Time) error {
		problems := []string{}
		r.mu.Lock()
		if r.sealing || r.sealed {
			r.mu.Unlock()
			return errors.New("early-share terminal drain repeated")
		}
		r.sealing = true
		if r.active != 0 || r.started != r.completed || uint64(r.count) != r.completed || r.dropped != 0 {
			problems = append(problems, "attempts were active, missing or dropped when sealing began")
		}
		records := append([]qualificationEarlyShareRecord{}, r.records[:r.count]...)
		r.mu.Unlock()
		if !joined {
			problems = append(problems, "fixture shutdown/producer join failed")
		}
		seen := map[*yamux.Session]bool{}
		joinedSessions := 0
		liveSessions := 0
		for _, record := range records {
			if seen[record.session] {
				continue
			}
			seen[record.session] = true
			if !record.session.IsClosed() {
				liveSessions++
				problems = append(problems, "retained event session still live before terminal join")
				continue // Never close a live session just to obtain proof.
			}
			if role == "server" {
				remaining := time.Until(deadline)
				if !joined || deadline.IsZero() || remaining <= 0 {
					problems = append(problems, "no existing shutdown budget remains for idempotent join")
					continue
				}
				done := make(chan error, 1)
				go func(session *yamux.Session) { done <- session.Close() }(record.session)
				select {
				case err := <-done:
					if err != nil {
						problems = append(problems, "idempotent event-session join failed")
					} else {
						joinedSessions++
					}
				case <-time.After(remaining):
					problems = append(problems, "existing shutdown deadline expired during idempotent join")
				}
			} else if joined {
				joinedSessions++ // Agent RunContext already joined these workers.
			}
		}
		terminalProcess, identityErr := qualificationEarlyShareIdentity()
		if identityErr != nil || terminalProcess != process {
			problems = append(problems, "endpoint process/namespace identity changed")
		}
		r.mu.Lock()
		if r.active != 0 || r.started != r.completed || r.dropped != 0 || uint64(r.count) != r.completed || len(records) != r.count {
			problems = append(problems, "active, missing or dropped early-share attempts")
		}
		r.sealed = true
		terminal := map[string]any{"schema_version": 1, "phase": "terminal-after-join", "role": role,
			"selected_policy": r.enabled, "process": terminalProcess, "observed_unix_ns": time.Now().UnixNano(),
			"threshold_bytes": earlyShareThreshold, "maximum_target_bytes": earlyShareAbsoluteMaximum,
			"capacity": qualificationEarlyShareCapacity, "attempts_started": r.started,
			"attempts_completed": r.completed, "active_attempts": r.active, "dropped_events": r.dropped,
			"records": records, "sealed": true, "producer_joined": joined,
			"event_sessions_observed": len(seen), "event_sessions_joined": joinedSessions,
			"event_sessions_live_before_join": liveSessions, "complete": len(problems) == 0, "issues": problems}
		r.mu.Unlock()
		if err := qualificationEarlyShareWrite(filepath.Join(directory, "early-share-"+role+"-terminal.json"), terminal); err != nil {
			return err
		}
		if len(problems) != 0 {
			return errors.New("early-share terminal proof incomplete")
		}
		return nil
	}, nil
}
