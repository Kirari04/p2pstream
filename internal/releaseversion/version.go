// Package releaseversion validates and compares the immutable release tags
// accepted by p2pstream's signed update channels.
package releaseversion

import (
	"regexp"
	"strings"

	"golang.org/x/mod/semver"
)

const (
	ChannelStable  = "stable"
	ChannelStaging = "staging"
)

var releasePattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// Valid reports whether version is a canonical three-component SemVer release
// tag without build metadata. Build metadata is excluded because the same
// identity must also be safe in OCI tags and release asset filenames.
func Valid(version string) bool {
	return len(version) <= 96 && releasePattern.MatchString(version) && semver.IsValid(version) && semver.Build(version) == ""
}

func Stable(version string) bool {
	return Valid(version) && semver.Prerelease(version) == ""
}

func Prerelease(version string) bool {
	return Valid(version) && semver.Prerelease(version) != ""
}

// ValidForChannel keeps stable and staging trust domains disjoint: stable
// accepts only final releases, while staging accepts only prereleases.
func ValidForChannel(version, channel string) bool {
	switch strings.TrimSpace(channel) {
	case ChannelStable:
		return Stable(version)
	case ChannelStaging:
		return Prerelease(version)
	default:
		return false
	}
}

func Compare(left, right string) int {
	return semver.Compare(left, right)
}
