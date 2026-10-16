import ctypes
import os
import signal
import sys

if len(sys.argv) < 3:
    raise SystemExit("usage: launch-owned-pidns.py PARENT_PID COMMAND [ARG...]")
expected_parent = int(sys.argv[1])
if os.getppid() != expected_parent:
    raise SystemExit("wrapper parent changed before PR_SET_PDEATHSIG")
libc = ctypes.CDLL(None, use_errno=True)
if libc.prctl(1, signal.SIGKILL, 0, 0, 0) != 0:
    error = ctypes.get_errno()
    raise OSError(error, os.strerror(error))
if os.getppid() != expected_parent:
    raise SystemExit("wrapper parent changed while installing PR_SET_PDEATHSIG")
os.execvp(sys.argv[2], sys.argv[2:])
