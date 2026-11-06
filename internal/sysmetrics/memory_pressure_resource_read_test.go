package sysmetrics

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// These tests and benchmarks use symbols shared with the immutable baseline,
// so identical fixtures can be overlaid on both measured source variants.
func TestMemoryMetricReadPreservesWholeInput(t *testing.T) {
	for _, tc := range []struct{ name, data string }{
		{"zero", "0\n"}, {"positive", "42"}, {"plus", "+42\n"}, {"negative-zero", "-0"},
		{"max-int", strconv.FormatInt(math.MaxInt64, 10)}, {"unlimited", "mAx\n"},
		{"unicode-trim", "\u2003\u00a0+42\u2028\t"}, {"unicode-max", "\u2003MAX\u00a0"},
		{"empty", ""}, {"only-space", "\u2003\n\t"}, {"negative", "-1"},
		{"overflow", "9223372036854775808"}, {"embedded-space", "4 2"},
		{"garbage-after-chunk", "42" + strings.Repeat(" ", 4094) + "x"},
		{"long-leading-space", strings.Repeat(" ", 70<<10) + "+42\n"},
		{"long-trailing-space", "42" + strings.Repeat("\u2003", 24<<10)},
		{"long-trailing-garbage", "max" + strings.Repeat(" ", 70<<10) + "!"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "memory.current")
			if err := os.WriteFile(path, []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			got, unlimited, err := readMemoryMetricFile(path)
			want, wantUnlimited, wantErr := resourceReadHistoricalMetric(path)
			if got != want || unlimited != wantUnlimited || resourceReadErrorText(err) != resourceReadErrorText(wantErr) {
				t.Fatalf("whole-input parity: value=%d/%d unlimited=%v/%v error lengths=%d/%d", got, want, unlimited, wantUnlimited, len(resourceReadErrorText(err)), len(resourceReadErrorText(wantErr)))
			}
		})
	}
}

func TestMemoryMetricReadRefreshAndErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.max")
	for _, data := range []string{"42\n", "max\n", "+84", "not-a-number"} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		got, unlimited, err := readMemoryMetricFile(path)
		want, wantUnlimited, wantErr := resourceReadHistoricalMetric(path)
		if got != want || unlimited != wantUnlimited || resourceReadErrorText(err) != resourceReadErrorText(wantErr) {
			t.Fatal("file rewrite was not sampled with historical semantics")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readMemoryMetricFile(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing metric: %v", err)
	}
	if _, _, err := readMemoryMetricFile(filepath.Dir(path)); err == nil {
		t.Fatal("directory read error was ignored")
	}
}

func TestResourceDiscoveryPreservesLongMountAndFreshHierarchy(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "memory root\\tree")
	leaf := filepath.Join(root, "service")
	if err := os.MkdirAll(leaf, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path, data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	membership, mounts, meminfo := filepath.Join(dir, "cgroup"), filepath.Join(dir, "mountinfo"), filepath.Join(dir, "meminfo")
	write(membership, "0::/service") // No final newline is required.
	escaped := strings.NewReplacer(`\`, `\134`, " ", `\040`).Replace(root)
	// Exceeds Scanner's usual64KiB token cap; the existing whole-file parser
	// accepts optional fields and must not silently lose the mounted leaf.
	write(mounts, "1 0 0:1 / "+escaped+" rw "+strings.Repeat("shared:1 ", 8192)+"- cgroup2 none rw")
	write(filepath.Join(root, "memory.current"), "64")
	write(filepath.Join(root, "memory.max"), "200")
	write(filepath.Join(leaf, "memory.current"), "42")
	write(filepath.Join(leaf, "memory.max"), "100")
	write(meminfo, "MemTotal: 2000 kB\nMemAvailable: 1000 kB\n")
	sampler := systemMemoryUsageSampler{cgroupPath: membership, mountInfoPath: mounts, memInfoPath: meminfo}
	for _, current := range []int64{42, 84} {
		write(filepath.Join(leaf, "memory.current"), strconv.FormatInt(current, 10))
		usages, err := sampler.SampleMemoryUsages()
		if err != nil {
			t.Fatal(err)
		}
		var cgroups []MemoryUsage
		for _, usage := range usages {
			if usage.Source == "cgroup_v2" {
				cgroups = append(cgroups, usage)
			}
		}
		want := []MemoryUsage{{UsedBytes: current, LimitBytes: 100, Source: "cgroup_v2"}, {UsedBytes: 64, LimitBytes: 200, Source: "cgroup_v2"}}
		if !reflect.DeepEqual(cgroups, want) {
			t.Fatalf("fresh nested constraints=%+v want=%+v", cgroups, want)
		}
	}
	if err := os.Remove(membership); err != nil {
		t.Fatal(err)
	}
	if _, err := sampler.SampleMemoryUsages(); err == nil {
		t.Fatal("missing fresh membership was ignored")
	}
}

func resourceReadErrorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Fixed pre-change oracle; data comes from an independently opened real file.
func resourceReadHistoricalMetric(path string) (int64, bool, error) {
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

func BenchmarkResourceMetricRead(b *testing.B) {
	for _, fixture := range []struct{ name, data string }{
		{"tiny", "123456789\n"}, {"unlimited", "max\n"},
		{"chunk-boundary", strings.Repeat(" ", 4094) + "42"},
		{"long-whitespace", strings.Repeat(" ", 70<<10) + "42\n"},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "memory.current")
			if err := os.WriteFile(path, []byte(fixture.data), 0o600); err != nil {
				b.Fatal(err)
			}
			want, wantUnlimited, err := resourceReadHistoricalMetric(path)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				got, unlimited, err := readMemoryMetricFile(path)
				if err != nil || got != want || unlimited != wantUnlimited {
					b.Fatal("metric benchmark failed semantic guard")
				}
			}
		})
	}
}
