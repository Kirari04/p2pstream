#!/usr/bin/env python3
import os
import signal
import sys

mask = {int(value) for value in sys.argv[1].split(',') if value}
signal.pthread_sigmask(signal.SIG_SETMASK, mask)
os.execve(sys.argv[2], sys.argv[2:], os.environ)
