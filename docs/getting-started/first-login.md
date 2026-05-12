# First Login

When p2pstream starts with an empty user table, the management UI enters setup mode.

## Setup window

The first admin user can be created for 5 minutes after server start. If no user exists and the window expires, restart the server to open setup again.

This is intentionally short so an unattended public management port does not stay in setup mode indefinitely.

## Username rules

Admin usernames must be:

- 3 to 64 characters,
- lowercase letters, numbers, underscores, or hyphens,
- stored lowercase.

Examples:

```text
admin
homelab-admin
ops_1
```

## Password rules

Passwords must be at least 12 characters. Use a password manager and store the admin credential with your other infrastructure secrets.

## Sessions

Login sessions are stored in SQLite and expire after 7 days. The session cookie is:

- HTTP-only,
- SameSite Lax,
- marked Secure when `ENV=production` or `MANAGEMENT_COOKIE_SECURE=true`.

## If you lock yourself out

p2pstream does not currently include a password reset CLI. For a self-hosted deployment, keep backups of `/data` and store the admin password securely. If this is a new empty installation and no user was created, restart the server during the setup window.
