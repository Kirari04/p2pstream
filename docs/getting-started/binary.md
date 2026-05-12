# Release Binary

Use the binary install when you want p2pstream managed directly by the host instead of Docker.

## Install

Download the Linux archive for your architecture from the GitHub release page:

```bash
curl -fLO https://github.com/Kirari04/p2pstream/releases/download/vX.Y.Z/p2pstream_vX.Y.Z_linux_amd64.tar.gz
curl -fLO https://github.com/Kirari04/p2pstream/releases/download/vX.Y.Z/checksums.txt
sha256sum -c checksums.txt --ignore-missing
tar -xzf p2pstream_vX.Y.Z_linux_amd64.tar.gz
sudo install -m 0755 p2pstream /usr/local/bin/p2pstream
```

Use `linux_arm64` on ARM hosts.

## Run the server

```bash
sudo install -d -m 0700 /var/lib/p2pstream
sudo env CONFIG_DIR=/var/lib/p2pstream \
  MANAGEMENT_PUBLIC_URL=https://proxy.example.com:8081 \
  /usr/local/bin/p2pstream server
```

The management UI listens on `https://host:8081` by default. The public HTTP and HTTPS listeners use ports configured in the management UI.

## Persistent service

For production, run p2pstream with systemd and a dedicated data directory. See [Systemd](../operations/systemd).

## Agent binary

The same binary includes the agent command:

```bash
p2pstream agent \
  --management-url https://proxy.example.com:8081 \
  --management-ca-file /etc/p2pstream/management-ca.pem \
  --agent-id agent-... \
  --agent-token ...
```

Most selfhosters should use the one-line installer from the Agent Setup dialog instead of building this command by hand.
