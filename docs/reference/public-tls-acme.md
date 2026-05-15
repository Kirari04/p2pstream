# Public TLS and ACME Reference

Public TLS is configured per HTTPS listener.

## Certificate mappings

Mappings include:

- listener,
- hostname pattern,
- source,
- certificate/key material or ACME settings,
- status and renewal timestamps.

Hostname patterns support exact names and wildcards such as `*.example.com`.

## Manual certificates

Manual mode accepts either:

- uploaded PEM certificate and key,
- filesystem paths to certificate and key.
- a GUI-generated self-signed certificate with a selected validity period.

Uploaded material is written under the configured certs directory.
Generated self-signed certificates are written to the same managed location and are intended for internal or test deployments, not trusted public clients.
The management UI shows certificate validity when metadata is stored or the certificate file can be parsed.

## ACME certificates

ACME source supports:

| Setting | Values |
| --- | --- |
| Challenge | HTTP-01, TLS-ALPN-01, DNS-01 |
| CA | Let's Encrypt production or staging |
| DNS provider | Cloudflare for DNS-01 |

ACME certificates renew when missing, expired, or within 30 days of expiry. Failed renewals are retried after a delay.

## Challenge requirements

| Challenge | Requirement |
| --- | --- |
| HTTP-01 | Public HTTP listener receives the challenge path. |
| TLS-ALPN-01 | Public HTTPS listener receives the ALPN challenge. |
| DNS-01 | Enabled Cloudflare DNS credential. Required for wildcards. |

ACME hostnames must be public fully-qualified DNS names.

## Statuses

| Status | Meaning |
| --- | --- |
| Pending | Waiting for initial issuance. |
| Renewing | Issuance or renewal is running. |
| Ready | Certificate material is available. |
| Error | Last issuance attempt failed. Check `last_error`. |
