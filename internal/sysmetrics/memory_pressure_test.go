package sysmetrics

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"p2pstream/internal/tunnel"
)

type mutableMemorySampler struct {
	usage MemoryUsage
	err   error
}

type memorySetSampler struct {
	usages []MemoryUsage
	fd     FileDescriptorUsage
	err    error
}

func (s *memorySetSampler) SampleMemoryUsage() (MemoryUsage, error) {
	if len(s.usages) == 0 {
		return MemoryUsage{}, s.err
	}
	return s.usages[0], s.err
}

func (s *memorySetSampler) SampleMemoryUsages() ([]MemoryUsage, error) {
	return append([]MemoryUsage(nil), s.usages...), s.err
}

func (s *memorySetSampler) SampleFileDescriptorUsage() (FileDescriptorUsage, error) {
	return s.fd, s.err
}

func (s *mutableMemorySampler) SampleMemoryUsage() (MemoryUsage, error) {
	return s.usage, s.err
}

func TestAdaptiveMemoryControllerUsesLiveHeadroomWithLifetimeReservations(t *testing.T) {
	sampler := &mutableMemorySampler{usage: MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "test"}}
	controller := MustNewAdaptiveMemoryController(DefaultAdaptiveMemoryConfig(), sampler)
	now := time.Unix(100, 0)
	controller.now = func() time.Time { return now }

	healthy := controller.ForceRefresh(2048, 125)
	if healthy.Level != MemoryPressureHealthy || healthy.AdmissionLimit <= 125 || healthy.AdmissionLimit >= 2048 || healthy.RejectNew {
		t.Fatalf("healthy snapshot = %+v, want a large live burst allowance below the protocol guard", healthy)
	}

	sampler.usage.UsedBytes = 410 << 20
	now = now.Add(time.Second)
	soft := controller.ForceRefresh(2048, 125)
	if soft.Level != MemoryPressureSoft || soft.AdmissionLimit != 125 || soft.RejectNew {
		t.Fatalf("soft snapshot = %+v, want existing work preserved with no unreserved headroom", soft)
	}

	sampler.usage.UsedBytes = 461 << 20
	now = now.Add(time.Second)
	critical := controller.ForceRefresh(2048, 125)
	if critical.Level != MemoryPressureCritical || critical.AdmissionLimit != 125 || !critical.RejectNew {
		t.Fatalf("critical snapshot = %+v, want all new admission rejected", critical)
	}
}

func TestAdaptiveMemoryControllerHysteresisAndRecovery(t *testing.T) {
	sampler := &mutableMemorySampler{usage: MemoryUsage{UsedBytes: 91, LimitBytes: 100, Source: "test"}}
	config := DefaultAdaptiveMemoryConfig()
	const mebibyte = int64(1 << 20)
	sampler.usage = MemoryUsage{UsedBytes: 91 * mebibyte, LimitBytes: 100 * mebibyte, Source: "test"}
	config.EstimatedBytesPerAdmission = mebibyte
	controller := MustNewAdaptiveMemoryController(config, sampler)
	now := time.Unix(200, 0)
	controller.now = func() time.Time { return now }

	if got := controller.ForceRefresh(1000, 20).Level; got != MemoryPressureCritical {
		t.Fatalf("initial level = %s, want critical", got)
	}
	sampler.usage.UsedBytes = 79 * mebibyte
	now = now.Add(time.Second)
	if got := controller.ForceRefresh(1000, 20).Level; got != MemoryPressureSoft {
		t.Fatalf("level at 79%% = %s, want hysteretic soft", got)
	}
	sampler.usage.UsedBytes = 74 * mebibyte
	now = now.Add(time.Second)
	if got := controller.ForceRefresh(1000, 20).Level; got != MemoryPressureHealthy {
		t.Fatalf("level at 74%% = %s, want recovered healthy", got)
	}
}

func TestAdaptiveMemoryControllerAnchorsBurstAllowanceUntilNextSample(t *testing.T) {
	const mebibyte = int64(1 << 20)
	sampler := &mutableMemorySampler{usage: MemoryUsage{UsedBytes: 70 * mebibyte, LimitBytes: 100 * mebibyte, Source: "test"}}
	config := DefaultAdaptiveMemoryConfig()
	config.EstimatedBytesPerAdmission = mebibyte
	controller := MustNewAdaptiveMemoryController(config, sampler)
	now := time.Unix(300, 0)
	controller.now = func() time.Time { return now }

	first := controller.ForceRefresh(100, 10)
	if first.AdmissionLimit != 20 {
		t.Fatalf("first admission limit = %d, want 20", first.AdmissionLimit)
	}
	// Increased in-use must not slide the cached ceiling forward and admit an
	// unbounded burst against one stale memory sample.
	second := controller.Snapshot(100, 19)
	if second.AdmissionLimit != first.AdmissionLimit || !second.SampledAt.Equal(first.SampledAt) {
		t.Fatalf("cached snapshot = %+v, want anchored %+v", second, first)
	}
}

func TestAdaptiveMemoryControllerConservesLifetimeStreamReservations(t *testing.T) {
	const mebibyte = int64(1 << 20)
	config := DefaultAdaptiveMemoryConfig()
	config.EstimatedBytesPerAdmission = mebibyte
	sampler := &mutableMemorySampler{usage: MemoryUsage{
		UsedBytes:  40 * mebibyte,
		LimitBytes: 100 * mebibyte,
		Source:     "test",
	}}
	controller := MustNewAdaptiveMemoryController(config, sampler)
	snapshot := controller.ForceRefresh(1_000, 20)

	// The 90 MiB hard watermark leaves 50 MiB of measured headroom. Twenty
	// existing streams retain 20 MiB of lifetime reservations, so only thirty
	// more may be admitted. The old per-sample burst calculation allowed 70.
	if snapshot.AdmissionLimit != 50 || snapshot.ReservedStreamByte != 20*mebibyte {
		t.Fatalf("lifetime-reserved snapshot = %+v, want limit 50 and 20 MiB reserved", snapshot)
	}
	hardBytes := int64(90 * mebibyte)
	worstCase := snapshot.Usage.UsedBytes + int64(snapshot.AdmissionLimit)*config.EstimatedBytesPerAdmission
	if worstCase > hardBytes {
		t.Fatalf("worst-case stream exposure = %d, hard watermark = %d", worstCase, hardBytes)
	}
}

func TestAdaptiveMemoryConfigRejectsChargeBelowYamuxInitialWindow(t *testing.T) {
	config := DefaultAdaptiveMemoryConfig()
	config.EstimatedBytesPerAdmission = tunnel.MinimumAdaptiveStreamChargeBytes - 1
	if err := config.Validate(); err == nil {
		t.Fatal("sub-initial-window adaptive charge was accepted")
	}
}

func TestAdaptiveMemoryControllerUsesDegradedCapWhenNoUsableSignalExists(t *testing.T) {
	sampler := &mutableMemorySampler{err: errors.New("unsupported")}
	controller := MustNewAdaptiveMemoryController(DefaultAdaptiveMemoryConfig(), sampler)
	snapshot := controller.ForceRefresh(2048, 50)
	if snapshot.Level != MemoryPressureUnknown || snapshot.AdmissionLimit != 50 || !snapshot.RejectNew || snapshot.SampleError == "" || snapshot.PressureReason != "sensor" {
		t.Fatalf("unknown snapshot = %+v, want observable fail-closed state preserving existing work", snapshot)
	}
}

func TestAdaptiveMemoryControllerRetainsLastGoodTelemetryButFailsClosed(t *testing.T) {
	sampler := &mutableMemorySampler{usage: MemoryUsage{UsedBytes: 91, LimitBytes: 100, Source: "test"}}
	controller := MustNewAdaptiveMemoryController(DefaultAdaptiveMemoryConfig(), sampler)
	now := time.Unix(400, 0)
	controller.now = func() time.Time { return now }
	critical := controller.ForceRefresh(2048, 12)
	if !critical.RejectNew {
		t.Fatalf("critical snapshot = %+v", critical)
	}
	sampler.err = errors.New("transient")
	now = now.Add(500 * time.Millisecond)
	stale := controller.ForceRefresh(2048, 12)
	if stale.Level != MemoryPressureUnknown || stale.AdmissionLimit != 12 || !stale.RejectNew || stale.SampleError == "" || stale.LastGoodSampleAt != critical.LastGoodSampleAt || !stale.Usage.Valid() {
		t.Fatalf("retained snapshot = %+v, want last-good telemetry with fail-closed admission", stale)
	}
	now = now.Add(2 * time.Second)
	degraded := controller.ForceRefresh(2048, 0)
	if degraded.Level != MemoryPressureUnknown || degraded.AdmissionLimit != 0 || !degraded.RejectNew {
		t.Fatalf("expired snapshot = %+v, want fail-closed degraded fallback", degraded)
	}
}

func TestSystemMemoryUsageSamplerFailsClosedOnFirstMalformedOptionalMountRoot(t *testing.T) {
	dir := t.TempDir()
	mountpoint := filepath.Join(dir, "cgroup")
	leaf := filepath.Join(mountpoint, "service")
	if err := os.MkdirAll(leaf, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(leaf, "memory.current"), "10485760")
	write(filepath.Join(leaf, "memory.max"), "1073741824")
	write(filepath.Join(mountpoint, "memory.current"), "10485760")
	write(filepath.Join(mountpoint, "memory.max"), "not-a-number")
	cgroupFile := filepath.Join(dir, "self.cgroup")
	mountInfoFile := filepath.Join(dir, "mountinfo")
	write(cgroupFile, "0::/service")
	write(mountInfoFile, "29 23 0:26 / "+mountpoint+" rw - cgroup2 cgroup rw")

	sampler := systemMemoryUsageSampler{
		cgroupPath: cgroupFile, mountInfoPath: mountInfoFile, memInfoPath: filepath.Join(dir, "missing"),
	}
	if _, err := sampler.SampleMemoryUsages(); err == nil {
		t.Fatal("malformed first-sample mount-root constraint was silently skipped")
	}
}

func TestAdaptiveMemoryControllerUsesMinimumNestedHeadroom(t *testing.T) {
	sampler := &memorySetSampler{
		usages: []MemoryUsage{
			{UsedBytes: 80 << 20, LimitBytes: 100 << 20, Source: "leaf"},
			{UsedBytes: 89 << 30, LimitBytes: 100 << 30, Source: "parent"},
		},
		fd: FileDescriptorUsage{Used: 10, Limit: 1 << 20},
	}
	controller := MustNewAdaptiveMemoryController(DefaultAdaptiveMemoryConfig(), sampler)
	snapshot := controller.ForceRefresh(65536, 0)
	wantHeadroom := int64(10 << 20)
	if snapshot.HeadroomToHardByte != wantHeadroom || snapshot.AdmissionLimit != int(wantHeadroom/tunnel.DefaultAdaptiveStreamChargeBytes) {
		t.Fatalf("nested snapshot = %+v, want leaf headroom %d", snapshot, wantHeadroom)
	}
}

func TestAdaptiveMemoryControllerEnforcesFileDescriptorHeadroom(t *testing.T) {
	sampler := &memorySetSampler{
		usages: []MemoryUsage{{UsedBytes: 1, LimitBytes: 1 << 30, Source: "memory"}},
		fd:     FileDescriptorUsage{Used: 85, Limit: 100},
	}
	controller := MustNewAdaptiveMemoryController(DefaultAdaptiveMemoryConfig(), sampler)
	soft := controller.ForceRefresh(65536, 10)
	if soft.Level != MemoryPressureSoft || soft.PressureReason != "file_descriptors" || soft.FDHeadroomToHard != 5 || soft.AdmissionLimit != 10 || soft.ReservedStreamFD != 20 {
		t.Fatalf("fd soft snapshot = %+v", soft)
	}
	sampler.fd.Used = 90
	critical := controller.ForceRefresh(65536, 10)
	if critical.Level != MemoryPressureCritical || !critical.RejectNew || critical.AdmissionLimit != 10 {
		t.Fatalf("fd critical snapshot = %+v", critical)
	}
}

func TestCurrentProcessMemoryCgroupPathsResolvesLeafV2Cgroup(t *testing.T) {
	dir := t.TempDir()
	cgroupFile := filepath.Join(dir, "cgroup")
	mountInfoFile := filepath.Join(dir, "mountinfo")
	if err := os.WriteFile(cgroupFile, []byte("0::/system.slice/p2pstream-agent.service\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mountInfoFile, []byte("29 23 0:26 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := currentProcessMemoryCgroupPaths(cgroupFile, mountInfoFile)
	if len(paths) != 3 || paths[0].directory != "/sys/fs/cgroup/system.slice/p2pstream-agent.service" || paths[0].source != "cgroup_v2" || paths[2].directory != "/sys/fs/cgroup" {
		t.Fatalf("resolved paths = %+v", paths)
	}
}

func TestCurrentProcessMemoryCgroupPathsResolvesMountedV1Root(t *testing.T) {
	dir := t.TempDir()
	cgroupFile := filepath.Join(dir, "cgroup")
	mountInfoFile := filepath.Join(dir, "mountinfo")
	if err := os.WriteFile(cgroupFile, []byte("5:cpu,memory:/tenant/agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mountInfoFile, []byte("31 23 0:28 /tenant /sys/fs/cgroup/memory rw - cgroup cgroup rw,memory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := currentProcessMemoryCgroupPaths(cgroupFile, mountInfoFile)
	if len(paths) != 2 || paths[0].directory != "/sys/fs/cgroup/memory/agent" || paths[0].source != "cgroup_v1" || paths[1].directory != "/sys/fs/cgroup/memory" {
		t.Fatalf("resolved paths = %+v", paths)
	}
}

func TestSystemMemoryUsageSamplerReadsEveryNestedCgroupConstraint(t *testing.T) {
	dir := t.TempDir()
	mountpoint := filepath.Join(dir, "cgroup")
	leaf := filepath.Join(mountpoint, "system.slice", "p2pstream-agent.service")
	if err := os.MkdirAll(leaf, 0o700); err != nil {
		t.Fatal(err)
	}
	writeMetric := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeMetric(filepath.Join(leaf, "memory.current"), "83886080")
	writeMetric(filepath.Join(leaf, "memory.max"), "104857600")
	writeMetric(filepath.Join(filepath.Dir(leaf), "memory.current"), "95563022336")
	writeMetric(filepath.Join(filepath.Dir(leaf), "memory.max"), "107374182400")
	writeMetric(filepath.Join(mountpoint, "memory.current"), "96636764160")
	writeMetric(filepath.Join(mountpoint, "memory.max"), "107374182400")
	cgroupFile := filepath.Join(dir, "self.cgroup")
	mountInfoFile := filepath.Join(dir, "mountinfo")
	writeMetric(cgroupFile, "0::/system.slice/p2pstream-agent.service")
	writeMetric(mountInfoFile, "29 23 0:26 / "+mountpoint+" rw - cgroup2 cgroup rw")

	sampler := systemMemoryUsageSampler{
		cgroupPath: cgroupFile, mountInfoPath: mountInfoFile, memInfoPath: filepath.Join(dir, "missing"),
	}
	usages, err := sampler.SampleMemoryUsages()
	if err != nil {
		t.Fatal(err)
	}
	cgroups := make([]MemoryUsage, 0, 3)
	for _, usage := range usages {
		if usage.Source == "cgroup_v2" {
			cgroups = append(cgroups, usage)
		}
	}
	if len(cgroups) != 3 || cgroups[0].LimitBytes != 100<<20 || cgroups[1].LimitBytes != 100<<30 {
		t.Fatalf("nested cgroup usages = %+v", usages)
	}
}

func TestSystemMemoryUsageSamplerFailsClosedWhenRestrictiveLeafDisappears(t *testing.T) {
	dir := t.TempDir()
	mountpoint := filepath.Join(dir, "cgroup")
	leaf := filepath.Join(mountpoint, "system.slice", "p2pstream-agent.service")
	if err := os.MkdirAll(leaf, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(leaf, "memory.current"), "83886080")
	write(filepath.Join(leaf, "memory.max"), "104857600")
	write(filepath.Join(filepath.Dir(leaf), "memory.current"), "104857600")
	write(filepath.Join(filepath.Dir(leaf), "memory.max"), "1073741824")
	write(filepath.Join(mountpoint, "memory.current"), "104857600")
	write(filepath.Join(mountpoint, "memory.max"), "1073741824")
	cgroupFile := filepath.Join(dir, "self.cgroup")
	mountInfoFile := filepath.Join(dir, "mountinfo")
	write(cgroupFile, "0::/system.slice/p2pstream-agent.service")
	write(mountInfoFile, "29 23 0:26 / "+mountpoint+" rw - cgroup2 cgroup rw")

	sampler := systemMemoryUsageSampler{
		cgroupPath: cgroupFile, mountInfoPath: mountInfoFile, memInfoPath: filepath.Join(dir, "missing"),
	}
	if _, err := sampler.SampleMemoryUsages(); err != nil {
		t.Fatalf("initial sample: %v", err)
	}
	if err := os.Remove(filepath.Join(leaf, "memory.max")); err != nil {
		t.Fatal(err)
	}
	if _, err := sampler.SampleMemoryUsages(); err == nil {
		t.Fatal("missing restrictive leaf was silently replaced by a looser ancestor")
	}
}

func TestSystemMemoryUsageSamplerFailsClosedOnFirstMalformedAncestor(t *testing.T) {
	dir := t.TempDir()
	mountpoint := filepath.Join(dir, "cgroup")
	leaf := filepath.Join(mountpoint, "service")
	if err := os.MkdirAll(leaf, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(leaf, "memory.current"), "10485760")
	write(filepath.Join(leaf, "memory.max"), "1073741824")
	write(filepath.Join(mountpoint, "memory.current"), "10485760")
	write(filepath.Join(mountpoint, "memory.max"), "not-a-number")
	cgroupFile := filepath.Join(dir, "self.cgroup")
	mountInfoFile := filepath.Join(dir, "mountinfo")
	write(cgroupFile, "0::/service")
	write(mountInfoFile, "29 23 0:26 / "+mountpoint+" rw - cgroup2 cgroup rw")

	sampler := systemMemoryUsageSampler{
		cgroupPath: cgroupFile, mountInfoPath: mountInfoFile, memInfoPath: filepath.Join(dir, "missing"),
	}
	// The malformed path is the synthetic mount root, which v2 legitimately
	// may omit on hybrid hosts. Add a real intermediate ancestor so a malformed
	// first sample cannot be mistaken for an absent root interface.
	parent := filepath.Join(mountpoint, "slice")
	leaf = filepath.Join(parent, "service")
	if err := os.MkdirAll(leaf, 0o700); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(leaf, "memory.current"), "10485760")
	write(filepath.Join(leaf, "memory.max"), "1073741824")
	write(filepath.Join(parent, "memory.current"), "10485760")
	write(filepath.Join(parent, "memory.max"), "not-a-number")
	write(cgroupFile, "0::/slice/service")
	if _, err := sampler.SampleMemoryUsages(); err == nil {
		t.Fatal("malformed first-sample ancestor was silently skipped")
	}
}

func TestSystemMemoryUsageSamplerReadsNestedCgroupV1Constraints(t *testing.T) {
	dir := t.TempDir()
	mountpoint := filepath.Join(dir, "memory")
	leaf := filepath.Join(mountpoint, "agent")
	if err := os.MkdirAll(leaf, 0o700); err != nil {
		t.Fatal(err)
	}
	writeMetric := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeMetric(filepath.Join(leaf, "memory.usage_in_bytes"), "83886080")
	writeMetric(filepath.Join(leaf, "memory.limit_in_bytes"), "104857600")
	writeMetric(filepath.Join(mountpoint, "memory.usage_in_bytes"), "95563022336")
	writeMetric(filepath.Join(mountpoint, "memory.limit_in_bytes"), "107374182400")
	cgroupFile := filepath.Join(dir, "self.cgroup")
	mountInfoFile := filepath.Join(dir, "mountinfo")
	writeMetric(cgroupFile, "5:cpu,memory:/tenant/agent")
	writeMetric(mountInfoFile, "31 23 0:28 /tenant "+mountpoint+" rw - cgroup cgroup rw,memory")

	sampler := systemMemoryUsageSampler{
		cgroupPath: cgroupFile, mountInfoPath: mountInfoFile, memInfoPath: filepath.Join(dir, "missing"),
	}
	usages, err := sampler.SampleMemoryUsages()
	if err != nil {
		t.Fatal(err)
	}
	cgroups := make([]MemoryUsage, 0, 2)
	for _, usage := range usages {
		if usage.Source == "cgroup_v1" {
			cgroups = append(cgroups, usage)
		}
	}
	if len(cgroups) != 2 || cgroups[0].LimitBytes != 100<<20 || cgroups[1].LimitBytes != 100<<30 {
		t.Fatalf("nested cgroup v1 usages = %+v", usages)
	}
}

func TestSystemMemoryUsageSamplerHostAndFileDescriptorFallbacks(t *testing.T) {
	dir := t.TempDir()
	memInfo := filepath.Join(dir, "meminfo")
	if err := os.WriteFile(memInfo, []byte("MemTotal: 1000 kB\nMemAvailable: 250 kB\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fdDir := filepath.Join(dir, "fd")
	if err := os.Mkdir(fdDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0", "1", "2"} {
		if err := os.WriteFile(filepath.Join(fdDir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sampler := systemMemoryUsageSampler{
		memInfoPath:    memInfo,
		fdPath:         fdDir,
		resolveCgroups: func(string, string) []memoryCgroupPath { return nil },
		getRlimit: func(_ int, limit *unix.Rlimit) error {
			limit.Cur = 100
			return nil
		},
	}
	usages, err := sampler.SampleMemoryUsages()
	if err != nil {
		t.Fatal(err)
	}
	var host MemoryUsage
	for _, usage := range usages {
		if usage.Source == "host" {
			host = usage
		}
	}
	if host.UsedBytes != 750*1024 || host.LimitBytes != 1000*1024 {
		t.Fatalf("host usage = %+v", host)
	}
	fd, err := sampler.SampleFileDescriptorUsage()
	if err != nil || fd.Used != 3 || fd.Limit != 100 {
		t.Fatalf("fd usage = %+v err=%v", fd, err)
	}
}

func BenchmarkAdaptiveMemoryControllerCachedSnapshot(b *testing.B) {
	sampler := &mutableMemorySampler{usage: MemoryUsage{UsedBytes: 64 << 20, LimitBytes: 512 << 20, Source: "benchmark"}}
	controller := MustNewAdaptiveMemoryController(DefaultAdaptiveMemoryConfig(), sampler)
	controller.ForceRefresh(2048, 125)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = controller.Snapshot(2048, 125)
	}
}

func BenchmarkAdaptiveMemoryControllerSystemRefresh(b *testing.B) {
	controller := MustNewAdaptiveMemoryController(DefaultAdaptiveMemoryConfig(), nil)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = controller.ForceRefresh(65_536, 256)
	}
}

func BenchmarkSystemFileDescriptorUsageLargeDirectory(b *testing.B) {
	const entries = 10_000
	directory := b.TempDir()
	for index := range entries {
		path := filepath.Join(directory, strconv.Itoa(index))
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			b.Fatal(err)
		}
	}
	sampler := systemMemoryUsageSampler{
		fdPath: directory,
		getRlimit: func(_ int, limit *unix.Rlimit) error {
			limit.Cur = 1_000_000
			return nil
		},
	}
	b.ReportMetric(entries, "fds")
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		usage, err := sampler.SampleFileDescriptorUsage()
		if err != nil || usage.Used != entries {
			b.Fatalf("usage = %+v err=%v", usage, err)
		}
	}
}
