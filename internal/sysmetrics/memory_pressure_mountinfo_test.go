package sysmetrics

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestParseCgroupMountsPreservesMalformedAndEscapedFields(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		want       []cgroupMount
	}{
		{"empty", "", []cgroupMount{}},
		{"ignored-malformed", "missing separator\n1 2 3 4 - cgroup2 source rw\n1 2 3 / /cg - cgroup2 source\n1 2 3 / /not-cgroup - cgroup2-other source rw\n", []cgroupMount{}},
		{"escaped-v2-optional-fields", "1 2 3 /tenant\\040root /cg\\134tree rw shared:1 master:2 - cgroup2 none rw\n",
			[]cgroupMount{{filesystem: "cgroup2", root: "/tenant root", mountpoint: `/cg\tree`, controllers: "rw"}}},
		{"v1-controller-field-and-whitespace", "\t1  2  3 /legacy /memory\\011mount  rw - \tcgroup memory rw,cpu,memory\r\n",
			[]cgroupMount{{filesystem: "cgroup", root: "/legacy", mountpoint: "/memory\tmount", controllers: "rw,cpu,memory"}}},
		{"nonrecursive-path-and-following-valid-line", "1 2 3 /\\134040 /cg rw - cgroup2 none rw\nwrong - other source rw\n4 5 6 / /v1 rw - cgroup memory rw,memory\n",
			[]cgroupMount{
				{filesystem: "cgroup2", root: `/\040`, mountpoint: "/cg", controllers: "rw"},
				{filesystem: "cgroup", root: "/", mountpoint: "/v1", controllers: "rw,memory"},
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseCgroupMounts(tc.data); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestParseCgroupMountsMatchesHistoricalParser(t *testing.T) {
	// Cross product exercises both malformed halves independently, exact
	// separator recognition, optional fields, both filesystems and escapes.
	left := []string{"", "1 2 3 4", "1 2 3 / /cg", "1 2 3 /a\\040b /cg\\134tree rw shared:1", "1\t2 3 / /cg rw"}
	right := []string{"", "cgroup", "cgroup2 source", "cgroup2 source rw", "cgroup memory rw,memory", "tmpfs source rw", "cgroup2x source rw"}
	for _, before := range left {
		for _, after := range right {
			for _, separator := range []string{" - ", " -", "-", "\t-\t"} {
				for _, ending := range []string{"\n", "\r\n"} {
					data := before + separator + after + ending + "9 1 2 / /sentinel rw - cgroup2 none rw\n"
					got, want := parseCgroupMounts(data), historicalParseCgroupMounts(data)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("parse %q: got %#v, want %#v", data, got, want)
					}
				}
			}
		}
	}
}

func TestUnescapeMountInfoPathPreservesSinglePass(t *testing.T) {
	for _, tc := range mountInfoEscapeCases() {
		t.Run(tc.name, func(t *testing.T) {
			if got := unescapeMountInfoPath(tc.value); got != tc.want {
				t.Fatalf("decode %q: got %q, want %q", tc.value, got, tc.want)
			}
			if got := historicalUnescapeMountInfoPath(tc.value); got != tc.want {
				t.Fatalf("historical fixture mismatch: got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMountInfoSharedUnescaperConcurrent(t *testing.T) {
	const workers, rounds = 16, 32
	start := make(chan struct{})
	errors := make(chan error, workers)
	cases := mountInfoEscapeCases()
	data := "1 2 3 /a\\040b /cg\\134tree rw - cgroup2 none rw\n"
	wantMounts := []cgroupMount{{filesystem: "cgroup2", root: "/a b", mountpoint: `/cg\tree`, controllers: "rw"}}
	var joined sync.WaitGroup
	for range workers {
		joined.Go(func() {
			<-start
			for range rounds {
				for _, tc := range cases {
					if got := unescapeMountInfoPath(tc.value); got != tc.want {
						errors <- fmt.Errorf("concurrent decode %q: got %q, want %q", tc.value, got, tc.want)
						return
					}
				}
				if got := parseCgroupMounts(data); !reflect.DeepEqual(got, wantMounts) {
					errors <- fmt.Errorf("concurrent mounts: got %#v, want %#v", got, wantMounts)
					return
				}
			}
		})
	}
	close(start)
	joined.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func mountInfoEscapeCases() []struct{ name, value, want string } {
	return []struct{ name, value, want string }{
		{"empty", "", ""},
		{"plain", "/sys/fs/cgroup", "/sys/fs/cgroup"},
		{"all-rules", `a\040b\011c\012d\134e`, "a b\tc\nd\\e"},
		{"adjacent", `\040\040\011`, "  \t"},
		{"nonrecursive-space", `\134040`, `\040`},
		{"nonrecursive-backslash", `\134134040`, `\134040`},
		{"unknown-and-truncated", `\777\0\04\`, `\777\0\04\`},
		{"literal-slash-before-escape", `\\040`, "\\ "},
		{"unicode-untouched", "café/路径", "café/路径"},
	}
}

// Retain the committed 360 parser independently: changing candidate filtering
// or shared replacement rules must not also change the differential oracle.
func historicalParseCgroupMounts(data string) []cgroupMount {
	mounts := make([]cgroupMount, 0, 2)
	for _, line := range strings.Split(data, "\n") {
		before, after, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		left, right := strings.Fields(before), strings.Fields(after)
		if len(left) < 5 || len(right) < 3 || (right[0] != "cgroup" && right[0] != "cgroup2") {
			continue
		}
		mounts = append(mounts, cgroupMount{
			filesystem: right[0], root: historicalUnescapeMountInfoPath(left[3]),
			mountpoint: historicalUnescapeMountInfoPath(left[4]), controllers: right[2],
		})
	}
	return mounts
}

func historicalUnescapeMountInfoPath(value string) string {
	return strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`).Replace(value)
}
