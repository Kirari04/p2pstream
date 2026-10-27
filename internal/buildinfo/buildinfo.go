// SPDX-License-Identifier: AGPL-3.0-or-later

package buildinfo

import "strings"

const DefaultRepository = "Kirari04/p2pstream"

var (
	Version    = "dev"
	Commit     = ""
	Repository = DefaultRepository
	Channel    = "development"
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
