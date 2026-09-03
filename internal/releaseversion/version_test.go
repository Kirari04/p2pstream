package releaseversion

import "testing"

func TestChannelVersionValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		version, channel string
		want             bool
	}{
		{"v1.2.3", ChannelStable, true},
		{"v1.2.3-staging.17", ChannelStaging, true},
		{"v1.2.3-rc.1", ChannelStaging, true},
		{"v1.2.3", ChannelStaging, false},
		{"v1.2.3-staging.17", ChannelStable, false},
		{"v1.2", ChannelStable, false},
		{"v01.2.3", ChannelStable, false},
		{"v1.2.3-01", ChannelStaging, false},
		{"v1.2.3+local", ChannelStable, false},
		{"staging", ChannelStaging, false},
	}
	for _, test := range tests {
		if got := ValidForChannel(test.version, test.channel); got != test.want {
			t.Errorf("ValidForChannel(%q, %q) = %v, want %v", test.version, test.channel, got, test.want)
		}
	}
}

func TestCompareUsesSemVerPrereleaseOrdering(t *testing.T) {
	t.Parallel()
	ordered := []string{"v1.2.3-staging.1", "v1.2.3-staging.2", "v1.2.3", "v1.2.4-staging.1"}
	for index := 1; index < len(ordered); index++ {
		if Compare(ordered[index-1], ordered[index]) >= 0 {
			t.Fatalf("Compare(%q, %q) did not preserve SemVer order", ordered[index-1], ordered[index])
		}
	}
}
