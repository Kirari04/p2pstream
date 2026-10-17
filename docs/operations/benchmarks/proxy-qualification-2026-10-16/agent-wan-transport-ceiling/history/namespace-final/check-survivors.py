import json
import os
from pathlib import Path

ancestors = set()
pid = os.getpid()
while pid > 1 and pid not in ancestors:
    ancestors.add(pid)
    try:
        fields = Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()
        pid = int(fields[1])
    except (FileNotFoundError, PermissionError, ValueError, IndexError):
        break

survivors = []
for entry in Path("/proc").iterdir():
    if not entry.name.isdigit() or int(entry.name) in ancestors:
        continue
    try:
        values = [part.decode(errors="replace") for part in
                  (entry / "cmdline").read_bytes().split(b"\0") if part]
    except (FileNotFoundError, PermissionError):
        continue
    if not values:
        continue
    executable = Path(values[0]).name
    matched = executable == "unshare" and values[1:] == ["--net", "sleep", "120"]
    matched |= executable in {"topology-harness.sh", "topology-disappearance-negative.sh"}
    matched |= executable == "python3" and len(values) > 1 and Path(values[1]).name == "launch-owned-pidns.py"
    if matched:
        survivors.append({"pid": int(entry.name), "argv": values})

result = {"survivors": survivors, "status": "pass" if not survivors else "fail"}
Path("survivor-check.json").write_text(json.dumps(result, indent=2, sort_keys=True) + "\n")
if survivors:
    raise SystemExit("owned topology survivor found")
