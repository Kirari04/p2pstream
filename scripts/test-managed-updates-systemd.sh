#!/usr/bin/env bash
set -Eeuo pipefail

# Full managed-update lifecycle in an isolated systemd VM. Never provisions the
# workstation. A caller-supplied VM must be disposable and have no agent install.
repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_dir"
[[ "$(uname -sm)" == "Linux x86_64" ]] || { echo 'This harness currently requires a Linux amd64 build host.' >&2; exit 1; }
command -v multipass >/dev/null
command -v go >/dev/null
mkdir -p "${repo_dir}/tmp"
# The Multipass snap cannot read the host's private /tmp namespace.
build_dir="$(mktemp -d "${repo_dir}/tmp/systemd-build.XXXXXX")"
output_dir="${P2PSTREAM_SYSTEMD_OUTPUT_DIR:-${repo_dir}/tmp/managed-updates-systemd}"
mkdir -p "$output_dir"
vm_name="${P2PSTREAM_SYSTEMD_VM_NAME:-p2pstream-update-review-$(date +%s)-$$}"
[[ "$vm_name" == p2pstream-update-review* ]] || { echo 'Test VM name must start with p2pstream-update-review.' >&2; exit 1; }
created_vm=false
cleanup() {
  if [[ "$created_vm" == true ]]; then
    multipass delete --purge "$vm_name" >/dev/null 2>&1 || true
  fi
  rm -rf "$build_dir"
}
trap cleanup EXIT

printf 'Building real binaries and integration harness...\n'
for spec in 'v1.0.0 d' 'v1.1.0 a' 'v1.2.0 b'; do
  read -r version commit_digit <<<"$spec"
  commit="$(printf '%040d' 0 | tr 0 "$commit_digit")"
  go build -ldflags "-X p2pstream/internal/buildinfo.Version=${version} -X p2pstream/internal/buildinfo.Commit=${commit}" -o "${build_dir}/p2pstream-${version}" .
done
go test -c ./internal/server -o "${build_dir}/server.test"

# Exercise actual older-worker serialization, not a hand-built approximation.
legacy_ref="${P2PSTREAM_SYSTEMD_LEGACY_REF:-v0.1.53-staging.84}"
mkdir "${build_dir}/legacy"
git archive "$legacy_ref" | tar -x -C "${build_dir}/legacy"
git rev-parse "${legacy_ref}^{commit}" >"${output_dir}/legacy-commit.txt"
(cd "${build_dir}/legacy" && go build -ldflags '-X p2pstream/internal/buildinfo.Version=v1.0.0 -X p2pstream/internal/buildinfo.Commit=dddddddddddddddddddddddddddddddddddddddd' -o "${build_dir}/p2pstream-legacy-worker" .)
cp scripts/install-agent.sh scripts/uninstall-agent.sh "$build_dir/"
tar -czf "${build_dir}/fixture.tar.gz" -C "$build_dir" server.test p2pstream-v1.0.0 p2pstream-v1.1.0 p2pstream-v1.2.0 p2pstream-legacy-worker install-agent.sh uninstall-agent.sh

if [[ -z "${P2PSTREAM_SYSTEMD_VM_NAME:-}" ]]; then
  multipass launch 24.04 --name "$vm_name" --cpus 2 --memory 4G --disk 12G
  created_vm=true
fi
multipass info "$vm_name" --format json >"${output_dir}/vm.json"
multipass transfer "${build_dir}/fixture.tar.gz" "${vm_name}:/tmp/p2pstream-review-fixture.tar.gz"
multipass exec "$vm_name" -- sudo bash -c 'set -e; mkdir -p /opt/p2pstream-systemd-fixture; tar -xzf /tmp/p2pstream-review-fixture.tar.gz -C /opt/p2pstream-systemd-fixture; printf "disposable-p2pstream-update-test\n" >/run/p2pstream-systemd-test-vm'
printf 'Running installer, real services, legacy reports and repeated rollout in %s...\n' "$vm_name"
result=0
multipass exec "$vm_name" -- sudo env P2PSTREAM_SYSTEMD_INTEGRATION=1 P2PSTREAM_SYSTEMD_FIXTURE_DIR=/opt/p2pstream-systemd-fixture /opt/p2pstream-systemd-fixture/server.test -test.run '^TestManagedUpdatesSystemdLifecycle$' -test.v -test.timeout 10m >"${output_dir}/test.log" 2>&1 || result=$?
multipass exec "$vm_name" -- sudo cat /opt/p2pstream-systemd-fixture/systemd-journal.log >"${output_dir}/systemd-journal.log" 2>/dev/null || true
cat "${output_dir}/test.log"
printf 'Logs: %s\n' "$output_dir"
exit "$result"
