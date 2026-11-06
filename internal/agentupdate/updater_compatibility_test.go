package agentupdate

import (
	"strings"
	"testing"
)

func TestPrereleaseUpdaterFloorRejectsOldRunnerAndAllowsLaterRollouts(t *testing.T) {
	data, _ := testBundle(t, "staging")
	manifest, err := ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Compatibility.Updater = VersionRange{Min: "v0.1.53-staging.88", Max: "v0.1.999"}
	data, err = CanonicalManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		updater string
		wantOK  bool
	}{
		{"v0.1.52", false},
		{"v0.1.53-staging.84", false},
		{"v0.1.53-staging.87", false},
		{"v0.1.53-staging.88", true},
		{"v0.1.53-staging.89", true},
		{"v0.1.53-staging.100", true},
		{"v0.1.53", true},
	} {
		t.Run(test.updater, func(t *testing.T) {
			policy := testPolicy()
			policy.RequiredChannel = "staging"
			policy.UpdaterVersion = test.updater
			_, err := Verify(data, policy)
			if test.wantOK && err != nil {
				t.Fatalf("compatible updater rejected: %v", err)
			}
			if !test.wantOK && (err == nil || !strings.Contains(err.Error(), "updater version is outside")) {
				t.Fatalf("old updater rejection = %v", err)
			}
		})
	}
}

func TestUpdaterCompatibilityPrereleaseBoundsRemainStrict(t *testing.T) {
	data, _ := testBundle(t, "stable")
	base, err := ParseManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, bounds := range []VersionRange{
		{Min: "v0.1.53-staging.089", Max: "v0.1.999"},
		{Min: "v0.1.53-staging.89", Max: "v0.1.53-staging.88"},
		{Min: "v0.1.53+mutable", Max: "v0.1.999"},
		{Min: "v0.1", Max: "v0.1.999"},
	} {
		manifest := base
		manifest.Compatibility.Updater = bounds
		if _, err := CanonicalManifest(manifest); err == nil || !strings.Contains(err.Error(), "updater compatibility") {
			t.Fatalf("bounds %+v accepted: %v", bounds, err)
		}
	}
	base.Compatibility.Server.Min = "v0.1.53-staging.88"
	if _, err := CanonicalManifest(base); err == nil || !strings.Contains(err.Error(), "server compatibility") {
		t.Fatalf("updater range change unexpectedly widened server range validation: %v", err)
	}
}
