// SPDX-License-Identifier: AGPL-3.0-or-later

package buildinfo

import (
	"net/url"
	"strings"
)

const (
	DefaultRepository = "Kirari04/p2pstream"
	License           = "AGPL-3.0-or-later"
)

var (
	Version    = "dev"
	Commit     = ""
	Repository = DefaultRepository
)

func RepositorySlug() string {
	repository := strings.TrimSpace(Repository)
	if repository == "" {
		repository = DefaultRepository
	}

	repository = strings.TrimSuffix(repository, ".git")
	repository = strings.TrimPrefix(repository, "https://github.com/")
	repository = strings.TrimPrefix(repository, "http://github.com/")
	repository = strings.TrimPrefix(repository, "git@github.com:")
	repository = strings.Trim(repository, "/")
	if repository == "" {
		return DefaultRepository
	}
	return repository
}

func RepositoryURL() string {
	return "https://github.com/" + RepositorySlug()
}

func SourceURL() string {
	base := RepositoryURL()
	ref := strings.TrimSpace(Version)
	if ref == "" || ref == "dev" || ref == "nightly" {
		ref = strings.TrimSpace(Commit)
	}
	if ref == "" {
		return base
	}
	return base + "/tree/" + url.PathEscape(ref)
}

func SourceReference() string {
	ref := strings.TrimSpace(Version)
	if ref == "" || ref == "dev" || ref == "nightly" {
		ref = strings.TrimSpace(Commit)
	}
	if ref == "" {
		return "repository default branch"
	}
	return ref
}
