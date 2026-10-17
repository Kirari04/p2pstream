import os,subprocess,time
pgids = {int(value) for value in subprocess.run(["ps", "-e", "-o", "pgid="], check=True, capture_output=True, text=True, timeout=20).stdout.split()}
time.sleep(.4)
print(sorted(pgids))
