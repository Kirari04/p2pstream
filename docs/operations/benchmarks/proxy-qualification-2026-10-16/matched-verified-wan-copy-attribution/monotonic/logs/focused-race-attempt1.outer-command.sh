set -eu
root=tmp/continuation-20261016/wan-copy-monotonic-source
regex='^(TestProxyCopyAttribution|TestProxyResponseRecorderCopyAttribution|TestQualificationWANCopyAttribution)'
go=/home/user/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.6.linux-amd64/bin/go
printf '%q ' env -i HOME=/home/user PATH=/usr/bin:/bin TMPDIR="$PWD/$root/build/test-tmp" GOCACHE="$PWD/$root/build/test-cache" GOENV=off GOTOOLCHAIN=local GOWORK=off GOFLAGS= CGO_ENABLED=1 "$go" test -race ./internal/server -run "$regex" -count=1 -timeout=180s > "$root/logs/focused-race-attempt1.invocation.txt"
printf '\n' >> "$root/logs/focused-race-attempt1.invocation.txt"
set +e
(cd "$root/build/source" && env -i HOME=/home/user PATH=/usr/bin:/bin TMPDIR="$PWD/../test-tmp" GOCACHE="$PWD/../test-cache" GOENV=off GOTOOLCHAIN=local GOWORK=off GOFLAGS= CGO_ENABLED=1 "$go" test -race ./internal/server -run "$regex" -count=1 -timeout=180s) > "$root/logs/focused-race-attempt1.stdout.log" 2> "$root/logs/focused-race-attempt1.stderr.log"
race=$?
set -e
printf '%s\n' "$race" > "$root/logs/focused-race-attempt1.status"
cat "$root/logs/focused-race-attempt1.stdout.log"
cat "$root/logs/focused-race-attempt1.stderr.log"
exit "$race"
