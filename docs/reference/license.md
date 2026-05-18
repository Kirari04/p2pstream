# License

p2pstream is licensed under the GNU Affero General Public License version 3 or
later (`AGPL-3.0-or-later`).

The license applies to the repository as a whole, including the server, agent,
management UI, scripts, documentation, and generated code unless a file states a
different compatible license.

## Corresponding Source

Official release binaries, Docker images, and GitHub release assets are built
from the matching GitHub release tag. Release assets include a source archive,
checksums, and legal notices.

The management listener also exposes a source offer:

```text
/.well-known/p2pstream/source
```

That endpoint is unauthenticated and remains available when the browser
management UI is disabled.

## Runtime Legal Files

Docker images place legal files under:

```text
/app/legal
```

Release binary archives include:

```text
LICENSE
NOTICE
SOURCE.txt
third-party/
```

## Network Deployments

If you modify p2pstream and provide network access to that modified version,
AGPL section 13 requires that users interacting with it remotely have an
opportunity to receive the corresponding source for your modified version.

This page is only a project summary. The complete license text is in the root
`LICENSE` file.
