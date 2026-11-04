//go:build linux

package sysmetrics

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const idleSamplerCostOptIn = "P2PSTREAM_IDLE_SAMPLER_COST"

var idleSamplerCostMountSink []cgroupMount
var idleSamplerCostPathSink string
var idleSamplerCostSnapshotSink AdaptiveMemorySnapshot

// A failed/stale sample can be substantially cheaper than a real refresh. Keep
// this guard in every measured iteration so no failure-path timing is accepted.
func idleSamplerCostSnapshotError(snapshot AdaptiveMemorySnapshot) error {
	switch {
	case snapshot.SampleError != "":
		return fmt.Errorf("system sample failed: %s", snapshot.SampleError)
	case !snapshot.Usage.Valid() || snapshot.Usage.Source == "":
		return errors.New("invalid memory sample")
	case snapshot.FDUsed < 0 || snapshot.FDLimit <= 0:
		return errors.New("invalid descriptor sample")
	case snapshot.Generation == 0 || snapshot.SampledAt.IsZero() || !snapshot.LastGoodSampleAt.Equal(snapshot.SampledAt):
		return errors.New("missing or stale successful sample boundary")
	default:
		return nil
	}
}

// Valid soft/critical pressure is not a sensor error. This fixed baseline
// separately accepts only normal-band environments; never change thresholds or
// retry a valid pressure observation to manufacture a healthy measurement.
func idleSamplerCostEnvironmentError(snapshot AdaptiveMemorySnapshot) error {
	if snapshot.Level != MemoryPressureHealthy || snapshot.RejectNew || snapshot.AdmissionLimit <= 256 {
		return fmt.Errorf("measurement environment outside healthy-band condition: level=%s rejected=%t admission=%d",
			snapshot.Level, snapshot.RejectNew, snapshot.AdmissionLimit)
	}
	return nil
}

func requireIdleSamplerCostOptIn(tb testing.TB) {
	tb.Helper()
	if os.Getenv(idleSamplerCostOptIn) != "1" {
		tb.Skip("real system sampler cost requires explicit " + idleSamplerCostOptIn + "=1")
	}
}

func TestIdleSamplerCostSnapshotValidation(t *testing.T) {
	now := time.Unix(100, 0)
	good := AdaptiveMemorySnapshot{
		Generation: 1, Usage: MemoryUsage{UsedBytes: 1, LimitBytes: 100, Source: "fixture"},
		FDUsed: 10, FDLimit: 1000, SampledAt: now, LastGoodSampleAt: now,
		Level: MemoryPressureHealthy, AdmissionLimit: 1000,
	}
	if err := idleSamplerCostSnapshotError(good); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*AdaptiveMemorySnapshot)
	}{
		{"sensor-error-with-valid-stale-data", func(s *AdaptiveMemorySnapshot) { s.SampleError = "unavailable" }},
		{"invalid-memory", func(s *AdaptiveMemorySnapshot) { s.Usage.LimitBytes = 0 }},
		{"missing-source", func(s *AdaptiveMemorySnapshot) { s.Usage.Source = "" }},
		{"invalid-fd", func(s *AdaptiveMemorySnapshot) { s.FDLimit = 0 }},
		{"missing-generation", func(s *AdaptiveMemorySnapshot) { s.Generation = 0 }},
		{"missing-time", func(s *AdaptiveMemorySnapshot) { s.SampledAt = time.Time{} }},
		{"stale-last-good", func(s *AdaptiveMemorySnapshot) { s.LastGoodSampleAt = now.Add(-time.Second) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := good
			tc.edit(&changed)
			if idleSamplerCostSnapshotError(changed) == nil {
				t.Fatal("invalid measurement sample accepted")
			}
		})
	}
	for _, level := range []MemoryPressureLevel{MemoryPressureSoft, MemoryPressureCritical} {
		pressured := good
		pressured.Level = level
		if err := idleSamplerCostSnapshotError(pressured); err != nil {
			t.Fatal("valid pressure was mislabeled as a sampler error:", err)
		}
		if idleSamplerCostEnvironmentError(pressured) == nil {
			t.Fatal("valid pressure should reject this fixed normal-band environment")
		}
	}

}

func TestIdleSamplerCostRealSystemHealth(t *testing.T) {
	requireIdleSamplerCostOptIn(t)
	controller := MustNewAdaptiveMemoryController(DefaultAdaptiveMemoryConfig(), nil)
	for range 3 {
		snapshot := controller.ForceRefresh(65_536, 256)
		if err := idleSamplerCostSnapshotError(snapshot); err != nil {
			t.Fatal(err)
		}
		if err := idleSamplerCostEnvironmentError(snapshot); err != nil {
			t.Fatal(err)
		}
		t.Logf("healthy refresh generation=%d source=%s memory=%d/%d fd=%d/%d",
			snapshot.Generation, snapshot.Usage.Source, snapshot.Usage.UsedBytes, snapshot.Usage.LimitBytes,
			snapshot.FDUsed, snapshot.FDLimit)
	}
}

type idleSamplerCostMountFixture struct {
	name string
	data string
	want []cgroupMount
}

func idleSamplerCostMountFixtures() []idleSamplerCostMountFixture {
	var fixtures []idleSamplerCostMountFixture
	for _, count := range []int{0, 32, 256} {
		var data strings.Builder
		for index := range count {
			fmt.Fprintf(&data, "%d 1 0:1 / /ordinary/mount-%d rw,nosuid shared:1 - tmpfs tmpfs rw,size=1024k\n", 100+index, index)
		}
		data.WriteString("40 1 0:40 /tenant\\040root /sys/fs/cgroup\\040unified rw shared:2 - cgroup2 cgroup rw\n")
		data.WriteString("41 1 0:41 /legacy\\134root /sys/fs/memory\\011tab rw - cgroup memory rw,memory\n")
		fixtures = append(fixtures, idleSamplerCostMountFixture{
			name: fmt.Sprintf("mixed-irrelevant-%d", count), data: data.String(),
			want: []cgroupMount{
				{filesystem: "cgroup2", root: "/tenant root", mountpoint: "/sys/fs/cgroup unified", controllers: "rw"},
				{filesystem: "cgroup", root: `/legacy\root`, mountpoint: "/sys/fs/memory\ttab", controllers: "rw,memory"},
			},
		})
	}
	fixtures = append(fixtures, idleSamplerCostMountFixture{
		name: "irrelevant-only-256", data: strings.Repeat("30 1 0:1 / /ordinary rw shared:1 - tmpfs tmpfs rw\n", 256),
		want: []cgroupMount{},
	})
	return fixtures
}

func TestIdleSamplerCostSyntheticFixtures(t *testing.T) {
	for _, fixture := range idleSamplerCostMountFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			if got := parseCgroupMounts(fixture.data); !reflect.DeepEqual(got, fixture.want) {
				t.Fatalf("parsed mounts differ: got %#v, want %#v", got, fixture.want)
			}
		})
	}
	for _, fixture := range idleSamplerCostPathFixtures() {
		if got := unescapeMountInfoPath(fixture.value); got != fixture.want {
			t.Fatalf("unescape %q: got %q, want %q", fixture.value, got, fixture.want)
		}
	}
}

func BenchmarkIdleSamplerCostSystemRefreshHealthy(b *testing.B) {
	requireIdleSamplerCostOptIn(b)
	controller := MustNewAdaptiveMemoryController(DefaultAdaptiveMemoryConfig(), nil)
	snapshot := controller.ForceRefresh(65_536, 256)
	if err := idleSamplerCostSnapshotError(snapshot); err != nil {
		b.Fatal(err)
	}
	if err := idleSamplerCostEnvironmentError(snapshot); err != nil {
		b.Fatal(err)
	}
	// Export only shape, not raw mountinfo paths, for interpretation of this host.
	mountinfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		b.Fatal(err)
	}
	b.Logf("mountinfo bytes=%d lines=%d cgroup-mounts=%d; preflight source=%s memory=%d/%d fd=%d/%d",
		len(mountinfo), len(strings.Split(strings.TrimSpace(string(mountinfo)), "\n")), len(parseCgroupMounts(string(mountinfo))),
		snapshot.Usage.Source, snapshot.Usage.UsedBytes, snapshot.Usage.LimitBytes, snapshot.FDUsed, snapshot.FDLimit)
	generation := snapshot.Generation
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		snapshot = controller.ForceRefresh(65_536, 256)
		if err := idleSamplerCostSnapshotError(snapshot); err != nil {
			b.Fatal(err)
		}
		if err := idleSamplerCostEnvironmentError(snapshot); err != nil {
			b.Fatal(err)
		}
		if snapshot.Generation != generation+1 {
			b.Fatal("refresh did not advance generation exactly once")
		}
		generation = snapshot.Generation
	}
	b.StopTimer()
	idleSamplerCostSnapshotSink = snapshot
	b.ReportMetric(0, "sample-errors/op")
}

func BenchmarkIdleSamplerCostMountInfo(b *testing.B) {
	for _, fixture := range idleSamplerCostMountFixtures() {
		b.Run(fixture.name, func(b *testing.B) {
			if got := parseCgroupMounts(fixture.data); !reflect.DeepEqual(got, fixture.want) {
				b.Fatalf("invalid synthetic mount fixture: got %#v", got)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(fixture.data)))
			b.ResetTimer()
			for range b.N {
				idleSamplerCostMountSink = parseCgroupMounts(fixture.data)
			}
		})
	}
}

func idleSamplerCostPathFixtures() []struct{ name, value, want string } {
	return []struct{ name, value, want string }{
		{"plain", "/sys/fs/cgroup/user.slice", "/sys/fs/cgroup/user.slice"},
		{"escaped", `a\040b\011c\012d\134e`, "a b\tc\nd\\e"},
		{"nonrecursive-escape", `a\134040\777`, `a\040\777`},
	}
}

func BenchmarkIdleSamplerCostUnescapePath(b *testing.B) {
	for _, fixture := range idleSamplerCostPathFixtures() {
		b.Run(fixture.name, func(b *testing.B) {
			if got := unescapeMountInfoPath(fixture.value); got != fixture.want {
				b.Fatalf("invalid synthetic unescape fixture: %q", got)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				idleSamplerCostPathSink = unescapeMountInfoPath(fixture.value)
			}
		})
	}
}
