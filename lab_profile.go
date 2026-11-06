//go:build labprofile

// lab_profile.go is deliberately excluded from the normal build. It provides
// a finite, file-only profiler for controlled throughput experiments in owned
// lab environments. It does not register an HTTP handler or expose pprof.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	labProfileEnabledEnv       = "P2PSTREAM_LABPROFILE"
	labProfileSecondsEnv       = "P2PSTREAM_LABPROFILE_SECONDS"
	labProfileDirEnv           = "P2PSTREAM_LABPROFILE_DIR"
	labProfileCPUPathEnv       = "P2PSTREAM_LABPROFILE_CPU"
	labProfileHeapPathEnv      = "P2PSTREAM_LABPROFILE_HEAP"
	labProfileBlockRateEnv     = "P2PSTREAM_LABPROFILE_BLOCK_RATE"
	labProfileMutexFractionEnv = "P2PSTREAM_LABPROFILE_MUTEX_FRACTION"
)

type labProfile struct {
	cpuPath       string
	heapPath      string
	goroutinePath string
	blockPath     string
	mutexPath     string

	cpuFile *os.File
	once    sync.Once
}

func init() {
	if !labProfileTruthy(os.Getenv(labProfileEnabledEnv)) {
		return
	}

	seconds, err := labProfileSeconds()
	if err != nil {
		fmt.Fprintf(os.Stderr, "labprofile disabled: %v\n", err)
		return
	}
	profile, err := newLabProfile()
	if err != nil {
		fmt.Fprintf(os.Stderr, "labprofile disabled: %v\n", err)
		return
	}
	if err := profile.start(); err != nil {
		fmt.Fprintf(os.Stderr, "labprofile disabled: %v\n", err)
		return
	}

	fmt.Fprintf(os.Stderr, "labprofile enabled for %s; cpu=%s heap=%s\n", seconds, profile.cpuPath, profile.heapPath)
	go func() {
		timer := time.NewTimer(seconds)
		defer timer.Stop()
		<-timer.C
		profile.finish()
	}()
}

func labProfileTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func labProfileSeconds() (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(labProfileSecondsEnv))
	if raw == "" {
		raw = "60"
	}
	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seconds <= 0 || seconds > int64((time.Duration(1<<63-1))/time.Second) {
		return 0, fmt.Errorf("%s must be a positive integer number of seconds", labProfileSecondsEnv)
	}
	return time.Duration(seconds) * time.Second, nil
}

func newLabProfile() (*labProfile, error) {
	dir := strings.TrimSpace(os.Getenv(labProfileDirEnv))
	if dir == "" {
		dir = filepath.Join("tmp", "throughput-profiling")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create profile directory %q: %w", dir, err)
	}

	pathFor := func(env, name string) string {
		if value := strings.TrimSpace(os.Getenv(env)); value != "" {
			return value
		}
		return filepath.Join(dir, name)
	}
	return &labProfile{
		cpuPath:       pathFor(labProfileCPUPathEnv, "cpu.pprof"),
		heapPath:      pathFor(labProfileHeapPathEnv, "heap.pprof"),
		goroutinePath: filepath.Join(dir, "goroutine.pprof"),
	}, nil
}

func (p *labProfile) start() error {
	if err := ensureLabProfileParent(p.cpuPath); err != nil {
		return fmt.Errorf("create CPU profile parent: %w", err)
	}
	file, err := os.OpenFile(p.cpuPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open CPU profile %q: %w", p.cpuPath, err)
	}
	if err := pprof.StartCPUProfile(file); err != nil {
		_ = file.Close()
		return fmt.Errorf("start CPU profile: %w", err)
	}
	p.cpuFile = file

	blockRate, err := labProfileNonNegativeInt(labProfileBlockRateEnv)
	if err != nil {
		p.finish()
		return err
	}
	if blockRate > 0 {
		runtime.SetBlockProfileRate(blockRate)
		p.blockPath = profilePathWithEnv(filepath.Join(filepath.Dir(p.cpuPath), "block.pprof"), "P2PSTREAM_LABPROFILE_BLOCK")
	}
	mutexFraction, err := labProfileNonNegativeInt(labProfileMutexFractionEnv)
	if err != nil {
		p.finish()
		return err
	}
	if mutexFraction > 0 {
		runtime.SetMutexProfileFraction(mutexFraction)
		p.mutexPath = profilePathWithEnv(filepath.Join(filepath.Dir(p.cpuPath), "mutex.pprof"), "P2PSTREAM_LABPROFILE_MUTEX")
	}
	return nil
}

func labProfileNonNegativeInt(env string) (int, error) {
	raw := strings.TrimSpace(os.Getenv(env))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", env)
	}
	return int(value), nil
}

func profilePathWithEnv(fallback, env string) string {
	if value := strings.TrimSpace(os.Getenv(env)); value != "" {
		return value
	}
	return fallback
}

func (p *labProfile) finish() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		pprof.StopCPUProfile()
		if p.cpuFile != nil {
			if err := p.cpuFile.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "labprofile: close CPU profile: %v\n", err)
			}
		}
		runtime.SetBlockProfileRate(0)
		// Stop CPU sampling before the forced collection so the heap snapshot
		// describes the workload rather than profiler shutdown itself.
		runtime.GC()
		if err := writeLabProfile("heap", p.heapPath, pprof.Lookup("heap")); err != nil {
			fmt.Fprintf(os.Stderr, "labprofile: %v\n", err)
		}
		if err := writeLabProfile("goroutine", p.goroutinePath, pprof.Lookup("goroutine")); err != nil {
			fmt.Fprintf(os.Stderr, "labprofile: %v\n", err)
		}
		if p.blockPath != "" {
			if err := writeLabProfile("block", p.blockPath, pprof.Lookup("block")); err != nil {
				fmt.Fprintf(os.Stderr, "labprofile: %v\n", err)
			}
		}
		if p.mutexPath != "" {
			if err := writeLabProfile("mutex", p.mutexPath, pprof.Lookup("mutex")); err != nil {
				fmt.Fprintf(os.Stderr, "labprofile: %v\n", err)
			}
			runtime.SetMutexProfileFraction(0)
		}
		fmt.Fprintf(os.Stderr, "labprofile finished; heap=%s\n", p.heapPath)
	})
}

func writeLabProfile(name, path string, profile *pprof.Profile) error {
	if profile == nil {
		return fmt.Errorf("%s profile is unavailable", name)
	}
	if err := ensureLabProfileParent(path); err != nil {
		return fmt.Errorf("create %s profile parent: %w", name, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open %s profile %q: %w", name, path, err)
	}
	defer file.Close()
	if err := profile.WriteTo(file, 0); err != nil {
		return fmt.Errorf("write %s profile %q: %w", name, path, err)
	}
	return nil
}

func ensureLabProfileParent(path string) error {
	parent := filepath.Dir(path)
	if parent == "." || parent == "" {
		return nil
	}
	return os.MkdirAll(parent, 0o700)
}
