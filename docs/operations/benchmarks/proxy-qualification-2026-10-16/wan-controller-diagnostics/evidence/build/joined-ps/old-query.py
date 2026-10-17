import os,subprocess,time
pgids = {int(value) for value in os.popen("ps -e -o pgid=").read().split()}
time.sleep(.4)
print(sorted(pgids))
