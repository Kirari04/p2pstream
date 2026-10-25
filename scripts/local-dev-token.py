#!/usr/bin/env python3
"""Ensure a stable, private local agent credential without printing its value."""
import os
from pathlib import Path
import re
import secrets
import stat
import sys


def require_private(fd, kind):
    info = os.fstat(fd)
    if info.st_uid != os.getuid() or info.st_mode & 0o077 or not kind(info.st_mode):
        raise ValueError("local credential storage must be owned by you and private")


def ensure_token():
    config_dir = Path(os.environ.get("CONFIG_DIR") or "p2pstream-data")
    config_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    storage_dir = config_dir / ".local-dev"
    try:
        os.mkdir(storage_dir, 0o700)
    except FileExistsError:
        pass
    directory = os.open(storage_dir, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        require_private(directory, stat.S_ISDIR)
        temporary = f".agent-token-{secrets.token_hex(16)}"
        fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600, dir_fd=directory)
        try:
            with os.fdopen(fd, "w") as output:
                output.write(secrets.token_hex(32) + "\n")
                output.flush()
                os.fsync(output.fileno())
            # Publish only the complete file, and never replace a concurrent winner.
            try:
                os.link(temporary, "agent-token", src_dir_fd=directory, dst_dir_fd=directory,
                        follow_symlinks=False)
            except FileExistsError:
                pass
        finally:
            os.unlink(temporary, dir_fd=directory)
        fd = os.open("agent-token", os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
        with os.fdopen(fd, "rb") as source:
            require_private(source.fileno(), stat.S_ISREG)
            if not re.fullmatch(rb"[0-9a-f]{64}\n", source.read(66)):
                raise ValueError("invalid saved local agent token; restore it or set AGENT_TOKEN explicitly")
    finally:
        os.close(directory)


if __name__ == "__main__":
    try:
        ensure_token()
    except (OSError, ValueError) as error:
        # OSError includes paths, never file contents; validation errors are fixed text.
        print(f"Cannot prepare CONFIG_DIR/.local-dev/agent-token: {error}", file=sys.stderr)
        sys.exit(1)
