package sysmetrics

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"p2pstream/internal/tunnel"
)

type MemoryPressureLevel uint8

const (
	MemoryPressureUnknown MemoryPressureLevel = iota
	MemoryPressureHealthy
	MemoryPressureSoft
	MemoryPressureCritical
)

func (l MemoryPressureLevel) String() string {
	switch l {
	case MemoryPressureHealthy:
		return "healthy"
	case MemoryPressureSoft:
		return "soft"
	case MemoryPressureCritical:
		return "critical"
	default:
		return "unknown"
	}
}

type MemoryUsage struct {
	UsedBytes  int64
	LimitBytes int64
	Source     string
}

func (u MemoryUsage) Valid() bool {
	return u.UsedBytes >= 0 && u.LimitBytes > 0
}

func (u MemoryUsage) Percent() float64 {
	if !u.Valid() {
		return 0
	}
	return float64(u.UsedBytes) * 100 / float64(u.LimitBytes)
}

type MemoryUsageSampler interface {
	SampleMemoryUsage() (MemoryUsage, error)
}

// MemoryUsageSetSampler exposes every independently enforceable memory
// constraint (for example a service leaf cgroup and its parent slice). The
// controller must honor the smallest absolute headroom, not merely the highest
// percentage, because differently-sized nested limits can disagree.
type MemoryUsageSetSampler interface {
	SampleMemoryUsages() ([]MemoryUsage, error)
}

type FileDescriptorUsage struct {
	Used  int64
	Limit int64
}

func (u FileDescriptorUsage) Valid() bool {
	return u.Used >= 0 && u.Limit > 0
}

func (u FileDescriptorUsage) Percent() float64 {
	if !u.Valid() {
		return 0
	}
	return float64(u.Used) * 100 / float64(u.Limit)
}

type FileDescriptorUsageSampler interface {
	SampleFileDescriptorUsage() (FileDescriptorUsage, error)
}

type MemoryUsageSamplerFunc func() (MemoryUsage, error)

func (f MemoryUsageSamplerFunc) SampleMemoryUsage() (MemoryUsage, error) {
	return f()
}

type AdaptiveMemoryConfig struct {
	SoftLimitPercent           int64
	HardLimitPercent           int64
	RecoveryPercent            int64
	SampleInterval             time.Duration
	EstimatedBytesPerAdmission int64
	MaxSampleStaleness         time.Duration
}

func DefaultAdaptiveMemoryConfig() AdaptiveMemoryConfig {
	return AdaptiveMemoryConfig{
		SoftLimitPercent: 80,
		HardLimitPercent: 90,
		RecoveryPercent:  75,
		SampleInterval:   100 * time.Millisecond,
		// Adaptive Yamux sessions cap their receive window to this value. The
		// controller conservatively reserves the full charge for every live
		// stream until its admission lease is released.
		EstimatedBytesPerAdmission: tunnel.DefaultAdaptiveStreamChargeBytes,
		MaxSampleStaleness:         time.Second,
	}
}

func (c AdaptiveMemoryConfig) Validate() error {
	switch {
	case c.RecoveryPercent < 1 || c.RecoveryPercent >= c.SoftLimitPercent:
		return errors.New("memory recovery percentage must be positive and below the soft limit")
	case c.SoftLimitPercent < 1 || c.SoftLimitPercent >= c.HardLimitPercent:
		return errors.New("memory soft-limit percentage must be positive and below the hard limit")
	case c.HardLimitPercent > 99:
		return errors.New("memory hard-limit percentage must be at most 99")
	case c.SampleInterval <= 0:
		return errors.New("memory sample interval must be positive")
	case c.EstimatedBytesPerAdmission < tunnel.MinimumAdaptiveStreamChargeBytes:
		return fmt.Errorf("estimated bytes per admission must be at least %d bytes", tunnel.MinimumAdaptiveStreamChargeBytes)
	case c.MaxSampleStaleness <= 0:
		return errors.New("maximum sample staleness must be positive")
	default:
		return nil
	}
}

type AdaptiveMemorySnapshot struct {
	Generation         uint64
	Usage              MemoryUsage
	Level              MemoryPressureLevel
	AdmissionLimit     int
	Maximum            int
	RejectNew          bool
	SampledAt          time.Time
	NextSampleAt       time.Time
	SampleError        string
	HeadroomToHardByte int64
	ReservedStreamByte int64
	StreamChargeByte   int64
	FDUsed             int64
	FDLimit            int64
	FDHeadroomToHard   int64
	ReservedStreamFD   int64
	PressureReason     string
	LastGoodSampleAt   time.Time
}

// AdaptiveMemoryController turns actual resource pressure into a temporary
// admission limit. It has no fixed operator-chosen stream ceiling: every live
// stream instead owns a conservative lifetime reservation equal to
// EstimatedBytesPerAdmission, and each sample derives a ceiling from the
// smallest real memory/FD headroom. Existing work is never canceled; pressure
// stops new admissions and drains naturally.
type AdaptiveMemoryController struct {
	mu        sync.Mutex
	config    AdaptiveMemoryConfig
	sampler   MemoryUsageSampler
	fdSampler FileDescriptorUsageSampler
	now       func() time.Time

	level        MemoryPressureLevel
	snapshot     AdaptiveMemorySnapshot
	nextSampleAt time.Time
	generation   uint64
	lastGood     AdaptiveMemorySnapshot
}

func NewAdaptiveMemoryController(config AdaptiveMemoryConfig, sampler MemoryUsageSampler) (*AdaptiveMemoryController, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if sampler == nil {
		sampler = NewSystemMemoryUsageSampler()
	}
	controller := &AdaptiveMemoryController{
		config:  config,
		sampler: sampler,
		now:     time.Now,
		level:   MemoryPressureUnknown,
	}
	if fdSampler, ok := sampler.(FileDescriptorUsageSampler); ok {
		controller.fdSampler = fdSampler
	}
	return controller, nil
}

func MustNewAdaptiveMemoryController(config AdaptiveMemoryConfig, sampler MemoryUsageSampler) *AdaptiveMemoryController {
	controller, err := NewAdaptiveMemoryController(config, sampler)
	if err != nil {
		panic(err)
	}
	return controller
}

func (c *AdaptiveMemoryController) Snapshot(maximum, inUse int) AdaptiveMemorySnapshot {
	return c.snapshotAt(maximum, inUse, false)
}

func (c *AdaptiveMemoryController) ForceRefresh(maximum, inUse int) AdaptiveMemorySnapshot {
	return c.snapshotAt(maximum, inUse, true)
}

func (c *AdaptiveMemoryController) snapshotAt(maximum, inUse int, force bool) AdaptiveMemorySnapshot {
	if maximum < 1 {
		maximum = 1
	}
	if inUse < 0 {
		inUse = 0
	}
	if inUse > maximum {
		inUse = maximum
	}
	if c == nil {
		return AdaptiveMemorySnapshot{
			Level:          MemoryPressureUnknown,
			AdmissionLimit: maximum,
			Maximum:        maximum,
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if !force && !c.snapshot.SampledAt.IsZero() && now.Before(c.nextSampleAt) && c.snapshot.Maximum == maximum {
		return c.snapshot
	}

	usages, err := sampleMemoryUsages(c.sampler)
	fdUsage := FileDescriptorUsage{}
	if err == nil && c.fdSampler != nil {
		fdUsage, err = c.fdSampler.SampleFileDescriptorUsage()
		if err == nil && !fdUsage.Valid() {
			err = errors.New("file descriptor usage is unavailable")
		}
	}
	c.generation++
	snapshot := AdaptiveMemorySnapshot{
		Generation:       c.generation,
		Maximum:          maximum,
		SampledAt:        now,
		NextSampleAt:     now.Add(c.config.SampleInterval),
		StreamChargeByte: c.config.EstimatedBytesPerAdmission,
	}
	if err == nil {
		for _, usage := range usages {
			if !usage.Valid() {
				err = errors.New("memory usage constraint is invalid")
				break
			}
		}
	}
	if err != nil || len(usages) == 0 {
		if err == nil {
			err = errors.New("memory usage is unavailable")
		}
		snapshot.SampleError = err.Error()
		if !c.lastGood.SampledAt.IsZero() && c.lastGood.Maximum == maximum && now.Sub(c.lastGood.SampledAt) <= c.config.MaxSampleStaleness {
			snapshot = c.lastGood
			snapshot.Generation = c.generation
			snapshot.SampledAt = now
			snapshot.NextSampleAt = now.Add(c.config.SampleInterval)
			snapshot.SampleError = err.Error()
			// Retain last-good resource values for diagnosis, but do not treat a
			// stale healthy sample as fresh admission credit. A missing cgroup or
			// FD signal can coincide with a changed limit/hierarchy.
			c.level = MemoryPressureUnknown
			snapshot.Level = c.level
			snapshot.PressureReason = "sensor"
			snapshot.AdmissionLimit = inUse
			snapshot.RejectNew = true
		} else {
			c.level = MemoryPressureUnknown
			snapshot.Level = c.level
			snapshot.PressureReason = "sensor"
			// Unknown memory or descriptor state is not evidence of spare
			// capacity. Preserve existing work but fail closed for new streams
			// until a complete resource sample succeeds.
			snapshot.AdmissionLimit = inUse
			snapshot.RejectNew = true
		}
		c.snapshot = snapshot
		c.nextSampleAt = snapshot.NextSampleAt
		return snapshot
	}

	selected := usages[0]
	memoryPercent := selected.Percent()
	memoryHeadroom := int64(math.MaxInt64)
	for _, usage := range usages {
		if usage.Percent() > memoryPercent {
			selected = usage
			memoryPercent = usage.Percent()
		}
		hardBytes := percentageOf(usage.LimitBytes, c.config.HardLimitPercent)
		headroom := hardBytes - usage.UsedBytes
		if headroom < 0 {
			headroom = 0
		}
		if headroom < memoryHeadroom {
			memoryHeadroom = headroom
		}
	}
	snapshot.Usage = selected
	snapshot.HeadroomToHardByte = memoryHeadroom
	snapshot.FDUsed = fdUsage.Used
	snapshot.FDLimit = fdUsage.Limit
	pressurePercent := memoryPercent
	snapshot.PressureReason = "memory"
	if fdUsage.Valid() {
		hardFDs := percentageOf(fdUsage.Limit, c.config.HardLimitPercent)
		snapshot.FDHeadroomToHard = hardFDs - fdUsage.Used
		if snapshot.FDHeadroomToHard < 0 {
			snapshot.FDHeadroomToHard = 0
		}
		if fdUsage.Percent() > pressurePercent {
			pressurePercent = fdUsage.Percent()
			snapshot.PressureReason = "file_descriptors"
		}
	}
	switch {
	case pressurePercent >= float64(c.config.HardLimitPercent):
		c.level = MemoryPressureCritical
	case pressurePercent >= float64(c.config.SoftLimitPercent):
		c.level = MemoryPressureSoft
	case c.level != MemoryPressureUnknown && c.level != MemoryPressureHealthy && pressurePercent >= float64(c.config.RecoveryPercent):
		// Hysteresis prevents repeated pool/admission oscillation around the
		// soft watermark. A previously critical controller becomes soft as
		// soon as it leaves the critical range, but does not become healthy
		// until it crosses the recovery watermark.
		c.level = MemoryPressureSoft
	default:
		c.level = MemoryPressureHealthy
	}
	snapshot.Level = c.level
	snapshot.LastGoodSampleAt = now
	// Memory usage only accounts for the portion of a stream window that has
	// become resident so far. Reserve the full bounded receive window for every
	// live stream as well. This intentionally double-counts resident stream
	// bytes: without protocol-level buffer accounting, that conservatism is the
	// invariant that prevents low-resident streams from accumulating fresh
	// burst credit on every sample and expanding together later.
	snapshot.ReservedStreamByte = saturatingMultiply(int64(inUse), c.config.EstimatedBytesPerAdmission)
	unreservedMemoryHeadroom := snapshot.HeadroomToHardByte - snapshot.ReservedStreamByte
	if unreservedMemoryHeadroom < 0 {
		unreservedMemoryHeadroom = 0
	}
	additional := unreservedMemoryHeadroom / c.config.EstimatedBytesPerAdmission
	if fdUsage.Valid() {
		// A default Go TCP dial may temporarily hold two sockets while Happy
		// Eyeballs races address families. Reserve both descriptors for every
		// admitted stream so synchronized slow dual-stack dials cannot exhaust
		// RLIMIT_NOFILE between samples.
		const descriptorsPerAdmission = int64(2)
		snapshot.ReservedStreamFD = saturatingMultiply(int64(inUse), descriptorsPerAdmission)
		unreservedFDHeadroom := snapshot.FDHeadroomToHard - snapshot.ReservedStreamFD
		if unreservedFDHeadroom < 0 {
			unreservedFDHeadroom = 0
		}
		fdAdditional := unreservedFDHeadroom / descriptorsPerAdmission
		if fdAdditional < additional {
			additional = fdAdditional
		}
	}
	if additional > int64(maximum-inUse) {
		additional = int64(maximum - inUse)
	}
	if additional < 0 {
		additional = 0
	}
	snapshot.AdmissionLimit = inUse + int(additional)
	switch c.level {
	case MemoryPressureCritical:
		snapshot.AdmissionLimit = inUse
		snapshot.RejectNew = true
	}
	c.snapshot = snapshot
	c.lastGood = snapshot
	c.nextSampleAt = snapshot.NextSampleAt
	return snapshot
}

func sampleMemoryUsages(sampler MemoryUsageSampler) ([]MemoryUsage, error) {
	if setSampler, ok := sampler.(MemoryUsageSetSampler); ok {
		return setSampler.SampleMemoryUsages()
	}
	usage, err := sampler.SampleMemoryUsage()
	if err != nil {
		return nil, err
	}
	return []MemoryUsage{usage}, nil
}

func percentageOf(value, percent int64) int64 {
	return (value/100)*percent + (value%100)*percent/100
}

func saturatingMultiply(left, right int64) int64 {
	if left <= 0 || right <= 0 {
		return 0
	}
	if left > math.MaxInt64/right {
		return math.MaxInt64
	}
	return left * right
}

type systemMemoryUsageSampler struct {
	cgroupPath     string
	mountInfoPath  string
	memInfoPath    string
	fdPath         string
	getRlimit      func(int, *unix.Rlimit) error
	resolveCgroups func(string, string) []memoryCgroupPath
	constraintMu   sync.Mutex
	finiteCgroups  map[string]struct{}
}

func NewSystemMemoryUsageSampler() MemoryUsageSampler {
	return &systemMemoryUsageSampler{
		cgroupPath:    "/proc/self/cgroup",
		mountInfoPath: "/proc/self/mountinfo",
		memInfoPath:   "/proc/meminfo",
		fdPath:        "/proc/self/fd",
		getRlimit:     unix.Getrlimit,
	}
}

func (s *systemMemoryUsageSampler) SampleMemoryUsage() (MemoryUsage, error) {
	candidates, err := s.SampleMemoryUsages()
	if err != nil {
		return MemoryUsage{}, err
	}
	selected := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.Percent() > selected.Percent() {
			selected = candidate
		}
	}
	return selected, nil
}

func (s *systemMemoryUsageSampler) SampleMemoryUsages() ([]MemoryUsage, error) {
	candidates := make([]MemoryUsage, 0, 4)
	hasFiniteCgroup := false
	var paths []memoryCgroupPath
	if s.resolveCgroups != nil {
		paths = s.resolveCgroups(s.cgroupPath, s.mountInfoPath)
	} else {
		var err error
		paths, err = resolveCurrentProcessMemoryCgroupPaths(s.cgroupPath, s.mountInfoPath)
		if err != nil {
			return nil, err
		}
	}
	s.constraintMu.Lock()
	defer s.constraintMu.Unlock()
	currentFiniteCgroups := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		constraintKey := path.source + ":" + path.directory
		usage, finite, err := readMemoryUsagePair(
			path.source,
			filepath.Join(path.directory, path.usageFile),
			filepath.Join(path.directory, path.limitFile),
		)
		if err != nil {
			_, previouslyFinite := s.finiteCgroups[constraintKey]
			if path.required || previouslyFinite || !optionalCgroupInterfaceAbsent(path) {
				return nil, fmt.Errorf("read %s memory constraint %s: %w", path.source, path.directory, err)
			}
			continue
		}
		if finite {
			candidates = append(candidates, usage)
			hasFiniteCgroup = true
			currentFiniteCgroups[constraintKey] = struct{}{}
		}
	}
	s.finiteCgroups = currentFiniteCgroups
	if limit := debug.SetMemoryLimit(-1); finiteMemoryLimit(limit) {
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		managed := mem.Sys - mem.HeapReleased
		used := int64(managed)
		if managed > math.MaxInt64 {
			used = math.MaxInt64
		}
		candidates = append(candidates, MemoryUsage{UsedBytes: used, LimitBytes: limit, Source: "go"})
	}
	if !hasFiniteCgroup {
		if usage, ok := readHostMemoryUsage(s.memInfoPath); ok {
			candidates = append(candidates, usage)
		}
	}
	if len(candidates) == 0 {
		return nil, errors.New("no finite cgroup, Go, or host memory limit is available")
	}
	return candidates, nil
}

func optionalCgroupInterfaceAbsent(path memoryCgroupPath) bool {
	if path.required {
		return false
	}
	_, usageErr := os.Stat(filepath.Join(path.directory, path.usageFile))
	_, limitErr := os.Stat(filepath.Join(path.directory, path.limitFile))
	return errors.Is(usageErr, os.ErrNotExist) && errors.Is(limitErr, os.ErrNotExist)
}

func (s *systemMemoryUsageSampler) SampleFileDescriptorUsage() (FileDescriptorUsage, error) {
	directory, err := os.Open(s.fdPath)
	if err != nil {
		return FileDescriptorUsage{}, fmt.Errorf("open process file descriptors: %w", err)
	}
	defer directory.Close()
	var used int64
	var buffer [32 * 1024]byte
	for {
		// ReadDirent plus a count-only parser avoids os.ReadDir's O(n log n)
		// sort and the one Go string allocation per descriptor from Readdirnames.
		// Temporary memory stays fixed even with tens of thousands of sockets.
		n, readErr := unix.ReadDirent(int(directory.Fd()), buffer[:])
		if readErr != nil {
			return FileDescriptorUsage{}, fmt.Errorf("count process file descriptors: %w", readErr)
		}
		if n == 0 {
			break
		}
		count, parseErr := countRawDirectoryEntries(buffer[:n])
		if parseErr != nil {
			return FileDescriptorUsage{}, fmt.Errorf("parse process file descriptors: %w", parseErr)
		}
		used += count
	}
	var limit unix.Rlimit
	getRlimit := s.getRlimit
	if getRlimit == nil {
		getRlimit = unix.Getrlimit
	}
	if err := getRlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		return FileDescriptorUsage{}, fmt.Errorf("read file descriptor limit: %w", err)
	}
	if limit.Cur == 0 {
		return FileDescriptorUsage{}, errors.New("file descriptor limit is zero")
	}
	limitValue := int64(limit.Cur)
	if limit.Cur >= uint64(math.MaxInt64/4) {
		limitValue = math.MaxInt64 / 4
	}
	return FileDescriptorUsage{Used: used, Limit: limitValue}, nil
}

func countRawDirectoryEntries(buffer []byte) (int64, error) {
	var count int64
	nameOffset := int(unsafe.Offsetof(unix.Dirent{}.Name))
	for offset := 0; offset < len(buffer); {
		if len(buffer)-offset < nameOffset {
			return 0, errors.New("short directory entry")
		}
		entry := (*unix.Dirent)(unsafe.Pointer(&buffer[offset]))
		recordLength := int(entry.Reclen)
		if recordLength < nameOffset || recordLength > len(buffer)-offset {
			return 0, errors.New("invalid directory entry length")
		}
		if entry.Ino != 0 && !rawDirectoryEntryIsDot(entry.Name[:]) {
			count++
		}
		offset += recordLength
	}
	return count, nil
}

func rawDirectoryEntryIsDot(name []int8) bool {
	if len(name) < 2 || name[0] != '.' {
		return false
	}
	if name[1] == 0 {
		return true
	}
	return len(name) >= 3 && name[1] == '.' && name[2] == 0
}

type memoryCgroupPath struct {
	source    string
	directory string
	usageFile string
	limitFile string
	required  bool
}

type processCgroup struct {
	unifiedPath string
	memoryPath  string
}

type cgroupMount struct {
	filesystem  string
	root        string
	mountpoint  string
	controllers string
}

func currentProcessMemoryCgroupPaths(cgroupPath, mountInfoPath string) []memoryCgroupPath {
	paths, err := resolveCurrentProcessMemoryCgroupPaths(cgroupPath, mountInfoPath)
	if err != nil {
		return nil
	}
	return paths
}

func resolveCurrentProcessMemoryCgroupPaths(cgroupPath, mountInfoPath string) ([]memoryCgroupPath, error) {
	cgroupData, err := os.ReadFile(cgroupPath)
	if err != nil {
		return nil, fmt.Errorf("read process cgroup membership: %w", err)
	}
	mountInfoData, err := os.ReadFile(mountInfoPath)
	if err != nil {
		return nil, fmt.Errorf("read cgroup mount information: %w", err)
	}
	process := parseProcessCgroup(string(cgroupData))
	if process.unifiedPath == "" && process.memoryPath == "" {
		return nil, errors.New("process memory cgroup membership is unavailable")
	}
	mounts := parseCgroupMounts(string(mountInfoData))
	paths := make([]memoryCgroupPath, 0, 2)
	for _, mount := range mounts {
		switch {
		case mount.filesystem == "cgroup2" && process.unifiedPath != "":
			if directory, ok := resolveCgroupDirectory(mount, process.unifiedPath); ok {
				paths = appendMemoryCgroupAncestors(paths, "cgroup_v2", directory, mount.mountpoint, "memory.current", "memory.max")
			}
		case mount.filesystem == "cgroup" && process.memoryPath != "" && controllerListContains(mount.controllers, "memory"):
			if directory, ok := resolveCgroupDirectory(mount, process.memoryPath); ok {
				paths = appendMemoryCgroupAncestors(paths, "cgroup_v1", directory, mount.mountpoint, "memory.usage_in_bytes", "memory.limit_in_bytes")
			}
		}
	}
	if len(paths) == 0 {
		return nil, errors.New("process memory cgroup mount is unavailable")
	}
	return paths, nil
}

func appendMemoryCgroupAncestors(paths []memoryCgroupPath, source, directory, mountpoint, usageFile, limitFile string) []memoryCgroupPath {
	directory = filepath.Clean(directory)
	mountpoint = filepath.Clean(mountpoint)
	for {
		// Every resolved membership/ancestor must be readable on the first
		// sample as well as later samples. The cgroup-v2 mount root is the one
		// exception: hybrid hosts can expose controllers there without the
		// memory.current/memory.max interface files. If that root is finite once,
		// finiteCgroups makes later disappearance fatal too.
		required := directory != mountpoint || source == "cgroup_v1"
		paths = append(paths, memoryCgroupPath{
			source: source, directory: directory, usageFile: usageFile, limitFile: limitFile, required: required,
		})
		if directory == mountpoint {
			return paths
		}
		parent := filepath.Dir(directory)
		if parent == directory || (parent != mountpoint && !strings.HasPrefix(parent, mountpoint+string(filepath.Separator))) {
			return paths
		}
		directory = parent
	}
}

func readProcessCgroup(path string) processCgroup {
	data, err := os.ReadFile(path)
	if err != nil {
		return processCgroup{}
	}
	return parseProcessCgroup(string(data))
}

func parseProcessCgroup(data string) processCgroup {
	var result processCgroup
	for _, line := range strings.Split(data, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[0] == "0" && parts[1] == "" {
			result.unifiedPath = filepath.Clean("/" + strings.TrimPrefix(parts[2], "/"))
		}
		if controllerListContains(parts[1], "memory") {
			result.memoryPath = filepath.Clean("/" + strings.TrimPrefix(parts[2], "/"))
		}
	}
	return result
}

func readCgroupMounts(path string) []cgroupMount {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return parseCgroupMounts(string(data))
}

func parseCgroupMounts(data string) []cgroupMount {
	mounts := make([]cgroupMount, 0, 2)
	for _, line := range strings.Split(data, "\n") {
		before, after, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		left := strings.Fields(before)
		right := strings.Fields(after)
		if len(left) < 5 || len(right) < 3 || (right[0] != "cgroup" && right[0] != "cgroup2") {
			continue
		}
		mounts = append(mounts, cgroupMount{
			filesystem:  right[0],
			root:        unescapeMountInfoPath(left[3]),
			mountpoint:  unescapeMountInfoPath(left[4]),
			controllers: right[2],
		})
	}
	return mounts
}

func resolveCgroupDirectory(mount cgroupMount, processPath string) (string, bool) {
	root := filepath.Clean("/" + strings.TrimPrefix(mount.root, "/"))
	processPath = filepath.Clean("/" + strings.TrimPrefix(processPath, "/"))
	relative, err := filepath.Rel(root, processPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	directory := filepath.Join(mount.mountpoint, relative)
	cleanMount := filepath.Clean(mount.mountpoint)
	cleanDirectory := filepath.Clean(directory)
	if cleanDirectory != cleanMount && !strings.HasPrefix(cleanDirectory, cleanMount+string(filepath.Separator)) {
		return "", false
	}
	return cleanDirectory, true
}

func controllerListContains(list, want string) bool {
	for _, controller := range strings.Split(list, ",") {
		if strings.TrimSpace(controller) == want {
			return true
		}
	}
	return false
}

func unescapeMountInfoPath(value string) string {
	replacer := strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)
	return replacer.Replace(value)
}

func readMemoryUsagePair(source, usagePath, limitPath string) (MemoryUsage, bool, error) {
	used, unlimited, err := readMemoryMetricFile(usagePath)
	if err != nil {
		return MemoryUsage{}, false, err
	}
	if unlimited {
		return MemoryUsage{}, false, errors.New("memory usage cannot be unlimited")
	}
	limit, unlimited, err := readMemoryMetricFile(limitPath)
	if err != nil {
		return MemoryUsage{}, false, err
	}
	if unlimited || !finiteMemoryLimit(limit) {
		return MemoryUsage{}, false, nil
	}
	return MemoryUsage{UsedBytes: used, LimitBytes: limit, Source: source}, true, nil
}

func readMemoryMetricFile(path string) (int64, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false, err
	}
	raw := strings.TrimSpace(string(data))
	if raw == "" {
		return 0, false, errors.New("memory metric is empty")
	}
	if strings.EqualFold(raw, "max") {
		return 0, true, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, false, fmt.Errorf("invalid memory metric %q", raw)
	}
	return value, false, nil
}

func finiteMemoryLimit(limit int64) bool {
	return limit > 0 && limit < math.MaxInt64/2
}

func readHostMemoryUsage(path string) (MemoryUsage, bool) {
	file, err := os.Open(path)
	if err != nil {
		return MemoryUsage{}, false
	}
	defer file.Close()
	var total, available int64
	var sawTotal, sawAvailable bool
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || value < 0 || value > math.MaxInt64/1024 {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = value * 1024
			sawTotal = true
		case "MemAvailable:":
			available = value * 1024
			sawAvailable = true
		}
	}
	if !sawTotal || !sawAvailable || total <= 0 || available < 0 || available > total {
		return MemoryUsage{}, false
	}
	return MemoryUsage{UsedBytes: total - available, LimitBytes: total, Source: "host"}, true
}

func readProcessRSSBytes(path string) (int64, bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] != "VmRSS:" {
			continue
		}
		kilobytes, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || kilobytes < 0 || kilobytes > math.MaxInt64/1024 {
			return 0, false
		}
		return kilobytes * 1024, true
	}
	return 0, false
}

func (s AdaptiveMemorySnapshot) String() string {
	return fmt.Sprintf(
		"level=%s source=%s used=%d limit=%d admission=%d/%d",
		s.Level,
		s.Usage.Source,
		s.Usage.UsedBytes,
		s.Usage.LimitBytes,
		s.AdmissionLimit,
		s.Maximum,
	)
}
