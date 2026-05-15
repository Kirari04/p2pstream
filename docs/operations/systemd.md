# Systemd

Use systemd when running the release binary directly on a Linux host.

## Server directory

```bash
sudo install -d -m 0700 /var/lib/p2pstream
sudo install -d -m 0755 /etc/p2pstream
```

Create `/etc/p2pstream/server.env`:

```ini
CONFIG_DIR=/var/lib/p2pstream
MANAGEMENT_PORT=8081
MANAGEMENT_PUBLIC_URL=https://proxy.example.com:8081
ENV=production
```

## Server unit

Create `/etc/systemd/system/p2pstream.service`:

```ini
[Unit]
Description=p2pstream reverse proxy
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/p2pstream/server.env
ExecStart=/usr/local/bin/p2pstream server
Restart=always
RestartSec=5s
User=root

[Install]
WantedBy=multi-user.target
```

Enable it:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now p2pstream
sudo systemctl status p2pstream
```

Root is required when binding privileged ports such as `80` or `443`. If you only use high ports, run as a dedicated user and adjust ownership of `/var/lib/p2pstream`.

## Agent unit

The generated installer writes:

```text
/etc/p2pstream/agent.env
/etc/systemd/system/p2pstream-agent.service
```

The service uses:

```ini
[Service]
EnvironmentFile=/etc/p2pstream/agent.env
ExecStart=/usr/local/bin/p2pstream agent
Restart=always
RestartSec=5s
User=p2pstream
Group=p2pstream
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
```

Operate it with:

```bash
sudo systemctl status p2pstream-agent
sudo systemctl restart p2pstream-agent
sudo journalctl -u p2pstream-agent -f
```

After rotating an agent token, update `/etc/p2pstream/agent.env` and restart the agent.

## Uninstall agent

Use this only for agents installed with the generated Linux systemd installer. Docker Compose agents should be removed with your Compose workflow instead.

The full-purge uninstall removes the agent service, `/etc/p2pstream`, `/usr/local/bin/p2pstream`, and the `p2pstream` service user and group. Do not run it on a host where those paths or that user are shared with a p2pstream server or another install you want to keep.

Generated command:

```bash
curl -fsSL https://raw.githubusercontent.com/Kirari04/p2pstream/main/scripts/uninstall-agent.sh | sudo env P2PSTREAM_UNINSTALL_CONFIRM=full-purge bash
```

Manual equivalent:

```bash
sudo systemctl disable --now p2pstream-agent || true
sudo rm -f /etc/systemd/system/p2pstream-agent.service
sudo systemctl daemon-reload
sudo systemctl reset-failed p2pstream-agent || true
sudo rm -rf /etc/p2pstream
sudo rm -f /usr/local/bin/p2pstream
sudo userdel p2pstream || true
sudo groupdel p2pstream || true
```

Uninstalling the host service does not remove the management record. After the remote host is removed, delete or disable the agent from the Agents page.

To preview the shell script actions without changing the host:

```bash
curl -fsSL https://raw.githubusercontent.com/Kirari04/p2pstream/main/scripts/uninstall-agent.sh | env P2PSTREAM_UNINSTALL_DRY_RUN=true P2PSTREAM_UNINSTALL_CONFIRM=full-purge bash
```
