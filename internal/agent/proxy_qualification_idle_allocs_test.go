//go:build linux

package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"
)

const qualificationIdleProfileMaximum = 8 << 20

// This test-only worker observes two fixed local flags, never calls GC, and is
// joined by the external fixture before its test returns. Its own allocations
// remain visible in the process counters/profile; they are never subtracted.
type qualificationIdleProfileWorker struct {
	cancel   context.CancelFunc
	done     chan struct{}
	err      error // read only after done closes
	complete bool
}

type qualificationIdleMemory struct {
	SampledUnixNano int64  `json:"sampled_unix_nano"`
	TotalAlloc      uint64 `json:"total_alloc_bytes"`
	HeapAlloc       uint64 `json:"heap_alloc_bytes"`
	HeapSys         uint64 `json:"heap_sys_bytes"`
	HeapInuse       uint64 `json:"heap_inuse_bytes"`
	HeapIdle        uint64 `json:"heap_idle_bytes"`
	HeapReleased    uint64 `json:"heap_released_bytes"`
	Mallocs         uint64 `json:"mallocs"`
	Frees           uint64 `json:"frees"`
	NumGC           uint32 `json:"num_gc"`
	NumForcedGC     uint32 `json:"num_forced_gc"`
	PauseTotalNS    uint64 `json:"pause_total_ns"`
}

func qualificationIdleMemorySnapshot() qualificationIdleMemory {
	var s runtime.MemStats
	runtime.ReadMemStats(&s)
	return qualificationIdleMemory{SampledUnixNano: time.Now().UnixNano(), TotalAlloc: s.TotalAlloc, HeapAlloc: s.HeapAlloc, HeapSys: s.HeapSys,
		HeapInuse: s.HeapInuse, HeapIdle: s.HeapIdle, HeapReleased: s.HeapReleased,
		Mallocs: s.Mallocs, Frees: s.Frees, NumGC: s.NumGC, NumForcedGC: s.NumForcedGC, PauseTotalNS: s.PauseTotalNs}
}

type qualificationIdleProfileReceipt struct {
	Role             string                  `json:"role"`
	Stage            string                  `json:"stage"`
	PID              int                     `json:"pid"`
	StartedUnixNano  int64                   `json:"started_unix_nano"`
	FinishedUnixNano int64                   `json:"finished_unix_nano"`
	ElapsedNS        int64                   `json:"elapsed_ns"`
	ProfileRate      int                     `json:"profile_rate"`
	ProfileBytes     int64                   `json:"profile_bytes"`
	ProfileSHA256    string                  `json:"profile_sha256"`
	StatsBefore      qualificationIdleMemory `json:"stats_before"`
	StatsAfter       qualificationIdleMemory `json:"stats_after"`
}

type qualificationIdleBoundedWriter struct {
	writer io.Writer
	bytes  int64
}

func (w *qualificationIdleBoundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > qualificationIdleProfileMaximum-w.bytes {
		return 0, errors.New("idle allocation profile exceeds 8 MiB")
	}
	n, err := w.writer.Write(p)
	w.bytes += int64(n)
	return n, err
}

func qualificationIdleDirectory(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("idle profile directory must be absolute and clean")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real != path {
		return errors.New("idle profile directory must exist without symlinks")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("idle profile directory must be mode 0700")
	}
	return nil
}

func qualificationIdleFlag(path, stage string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16 {
		return false, errors.New("invalid idle profile control flag")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if string(data) != stage+"\n" {
		return false, errors.New("incorrect idle profile stage flag")
	}
	return true, nil
}

func qualificationIdleWriteJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > 65536 {
		return errors.New("idle profile scalar receipt exceeds 64 KiB")
	}
	f, err := os.OpenFile(path+".tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("idle profile receipt already exists")
	}
	return os.Rename(path+".tmp", path)
}

func qualificationIdleCapture(directory, role, stage string) error {
	started := time.Now()
	result := qualificationIdleProfileReceipt{Role: role, Stage: stage, PID: os.Getpid(), StartedUnixNano: started.UnixNano(), ProfileRate: runtime.MemProfileRate}
	if result.ProfileRate != 512*1024 {
		return errors.New("idle allocation capture requires unchanged default profile rate")
	}
	f, err := os.OpenFile(filepath.Join(directory, stage+".pprof"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	writer := qualificationIdleBoundedWriter{writer: io.MultiWriter(f, hash)}
	result.StatsBefore = qualificationIdleMemorySnapshot()
	profile := pprof.Lookup("allocs")
	if profile == nil {
		f.Close()
		return errors.New("allocs profile unavailable")
	}
	err = profile.WriteTo(&writer, 0) // Does not invoke runtime.GC or FreeOSMemory.
	result.StatsAfter = qualificationIdleMemorySnapshot()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if writer.bytes == 0 {
		return errors.New("empty idle allocation profile")
	}
	result.ProfileBytes = writer.bytes
	result.ProfileSHA256 = hex.EncodeToString(hash.Sum(nil))
	result.FinishedUnixNano = time.Now().UnixNano()
	result.ElapsedNS = time.Since(started).Nanoseconds()
	return qualificationIdleWriteJSON(filepath.Join(directory, stage+".json"), result)
}

func newQualificationIdleProfileWorker(parent context.Context, directory, control, role string) (*qualificationIdleProfileWorker, error) {
	if err := qualificationIdleDirectory(directory); err != nil {
		return nil, err
	}
	if err := qualificationIdleDirectory(control); err != nil {
		return nil, err
	}
	if directory == control || (role != "server" && role != "agent") {
		return nil, errors.New("invalid idle profile role/directories")
	}
	ctx, cancel := context.WithTimeout(parent, 120*time.Second)
	worker := &qualificationIdleProfileWorker{cancel: cancel, done: make(chan struct{})}
	beforeFlag, afterFlag := filepath.Join(control, "before.request"), filepath.Join(control, "after.request")
	go func() {
		defer close(worker.done)
		defer cancel()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for _, stage := range []string{"before", "after"} {
			flag := beforeFlag
			if stage == "after" {
				flag = afterFlag
			}
			for {
				if err := ctx.Err(); err != nil {
					worker.err = err
					return
				}
				if stage == "before" {
					exists, err := qualificationIdleFlag(afterFlag, "after")
					if err != nil {
						worker.err = err
						return
					}
					if exists {
						worker.err = errors.New("after flag preceded before capture")
						return
					}
				}
				exists, err := qualificationIdleFlag(flag, stage)
				if err != nil {
					worker.err = err
					return
				}
				if exists {
					break
				}
				select {
				case <-ctx.Done():
					worker.err = ctx.Err()
					return
				case <-ticker.C:
				}
			}
			if err := qualificationIdleCapture(directory, role, stage); err != nil {
				worker.err = err
				return
			}
		}
		worker.complete = true
	}()
	return worker, nil
}

func (worker *qualificationIdleProfileWorker) join() error {
	worker.cancel()
	select {
	case <-worker.done:
		if worker.err != nil {
			return worker.err
		}
		if !worker.complete {
			return errors.New("idle profile worker stopped before two captures")
		}
		return nil
	case <-time.After(time.Second):
		return errors.New("idle profile worker did not join")
	}
}

func startQualificationIdleAllocationProfiles(t *testing.T, role string) func() {
	t.Helper()
	directory, control := os.Getenv("P2PSTREAM_QUALIFICATION_IDLE_ALLOCS_DIR"), os.Getenv("P2PSTREAM_QUALIFICATION_IDLE_CONTROL_DIR")
	if directory == "" && control == "" {
		return func() {}
	}
	worker, err := newQualificationIdleProfileWorker(t.Context(), directory, control, role)
	if err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := worker.join(); err != nil {
			t.Errorf("idle allocation profile worker: %v", err)
			return
		}
		if err := qualificationIdleWriteJSON(filepath.Join(directory, "joined.json"), map[string]any{"role": role, "pid": os.Getpid(), "worker_joined": true, "profile_count": 2}); err != nil {
			t.Error(err)
		}
	}
}

func TestQualificationIdleAllocationProfiles(t *testing.T) {
	directory, control := t.TempDir(), t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(control, 0700); err != nil {
		t.Fatal(err)
	}
	worker, err := newQualificationIdleProfileWorker(t.Context(), directory, control, "server")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		worker.cancel()
		select {
		case <-worker.done:
		case <-time.After(time.Second):
			t.Error("idle profile test worker did not join during cleanup")
		}
	})
	for _, stage := range []string{"before", "after"} {
		if err := os.WriteFile(filepath.Join(control, stage+".request"), []byte(stage+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		var data []byte
		for time.Now().Before(deadline) {
			data, err = os.ReadFile(filepath.Join(directory, stage+".json"))
			if err == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if err != nil {
			t.Fatal(err)
		}
		var receipt qualificationIdleProfileReceipt
		if err = json.Unmarshal(data, &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.Stage != stage || receipt.ProfileBytes <= 0 || receipt.ProfileBytes > qualificationIdleProfileMaximum || receipt.ProfileRate != 512*1024 || receipt.StatsAfter.TotalAlloc < receipt.StatsBefore.TotalAlloc {
			t.Fatalf("inexact receipt: %+v", receipt)
		}
		profile, err := os.ReadFile(filepath.Join(directory, stage+".pprof"))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(profile)
		if hex.EncodeToString(sum[:]) != receipt.ProfileSHA256 {
			t.Fatal("profile hash mismatch")
		}
	}
	if err := worker.join(); err != nil {
		t.Fatal(err)
	}
	if err := qualificationIdleCapture(directory, "server", "before"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("capture overwrote existing profile: %v", err)
	}
	var bounded qualificationIdleBoundedWriter
	if _, err := bounded.Write(make([]byte, qualificationIdleProfileMaximum+1)); err == nil {
		t.Fatal("oversized profile accepted")
	}
	if err := qualificationIdleWriteJSON(filepath.Join(directory, "oversized.json"), fmt.Sprintf("%070000d", 0)); err == nil {
		t.Fatal("oversized scalar accepted")
	}
	if err := os.WriteFile(filepath.Join(control, "bad.request"), []byte("wrong\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := qualificationIdleFlag(filepath.Join(control, "bad.request"), "before"); err == nil {
		t.Fatal("malformed control accepted")
	}
	cancelled, stop := context.WithCancel(t.Context())
	stop()
	other, out := t.TempDir(), t.TempDir()
	os.Chmod(other, 0700)
	os.Chmod(out, 0700)
	incomplete, err := newQualificationIdleProfileWorker(cancelled, other, out, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if err := incomplete.join(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled worker was accepted: %v", err)
	}
}
