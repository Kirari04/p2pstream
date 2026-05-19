# First Login

Create the first admin account during the setup window, then use the management console to configure the proxy.

## Use This When

Use this on a new installation, after restoring an empty database, or when you need to recover a forgotten management password.

## Prerequisites

- The server is running.
- No management users exist yet for first setup.
- You can reach the management URL, usually `https://your-server:8081`.

## Steps

1. Open the management URL in a browser.
2. On **Setup Admin**, create the primary administrator account.
3. Use a username with 3 to 64 lowercase letters, numbers, underscores, or hyphens.
4. Use a password with at least 12 characters.
5. Log in and open **Overview**, then **Proxy** when you are ready to create listeners, backends, and routes.

The setup window is available for 5 minutes after server start when the user table is empty. If the window expires before any user exists, restart the server:

```bash
docker compose restart p2pstream
```

## Runtime Rules

| Area | Behavior |
| --- | --- |
| Usernames | Normalized to lowercase and limited to lowercase letters, numbers, underscores, and hyphens. |
| Passwords | Minimum length is 12 characters. |
| Sessions | Stored in SQLite and expire after 7 days. |
| Cookie security | Session cookie is HTTP-only, SameSite Lax, and Secure when management TLS is enabled, `ENV=production`, or `MANAGEMENT_COOKIE_SECURE=true`. |

## Verification

After login, the **Overview** dashboard should load with live proxy status, request totals, traffic trends, and the main navigation for **Overview**, **Traffic**, **Proxy**, **Agents**, **Traffic Policy**, **Templates**, **TLS**, and **Settings**.

<figure class="doc-screenshot">
  <img src="../assets/new/dashboard_overview.png" alt="p2pstream Overview dashboard showing proxy status, request totals, traffic trend, hotspots, and configuration summary">
  <figcaption>The Overview page is the first post-login health check. It confirms the selected environment, public proxy state, recent traffic, problem signals, and quick access to the configuration areas.</figcaption>
</figure>

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| Setup window expired and no users exist | Restart the server and create the first admin within 5 minutes. |
| Forgot an existing password | Reset the user against the same SQLite database. |
| Reset command uses the wrong database | Run it in the container or pass the same `CONFIG_DIR` or `--database-url` used by the server. |

Docker Compose recovery:

```bash
docker compose exec p2pstream p2pstream users reset-password admin
```

Binary/systemd recovery:

```bash
CONFIG_DIR=/var/lib/p2pstream p2pstream users reset-password admin
```

The reset command updates the user password and revokes active sessions for that user.

## Next Steps

- [Publish a service](../guides/publish-a-service)
- [Security hardening](../operations/security-hardening)
- [Environments reference](../reference/environments)
- [CLI reference](../reference/cli)
