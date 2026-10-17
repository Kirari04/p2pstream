from pathlib import Path
import hashlib
root=Path(__file__).resolve().parent;old=root.parent/'wan-terminal-attribution-20261017';p=root/'runner-source/integration/generated/qualify-proxy-agent-growth-wan-below-tls.sh'
s=p.read_text();needle='      P2PSTREAM_QUALIFICATION_ROLE=agent-external \\\n';assert s.count(needle)==2
s=s.replace(needle,'      P2PSTREAM_QUALIFICATION_DATA_CONTROLLER="$P2PSTREAM_QUALIFICATION_DATA_CONTROLLER" \\\n      P2PSTREAM_QUALIFICATION_CONTROLLER_NETNS="$agent_namespace_identity" \\\n      P2PSTREAM_QUALIFICATION_CONTROLLER_PARENT_NETNS="$(readlink /proc/self/ns/net)" \\\n'+needle)
s=s.replace('set -Eeuo pipefail','set -Eeuo pipefail\n[[ ${P2PSTREAM_QUALIFICATION_DATA_CONTROLLER-} == cubic || ${P2PSTREAM_QUALIFICATION_DATA_CONTROLLER-} == reno ]] || { echo "exact diagnostic controller required" >&2; exit 125; }',1);p.write_text(s)
p=root/'runner-source/integration/run-wan-below-tls-arm.sh';s=p.read_text();s=s.replace('expected_terminal_build_receipt_sha256=ad0dbfb3adafab686a4fc84f75effaee57819f2694ae644ada901a192604ad62','expected_terminal_build_receipt_sha256=BLOCKED_PENDING_REVIEWED_CONTROLLER_BUILD_RECEIPT',1)
s=s.replace('a672ab078d424a59d795c522a40f96da815b8aaa55af6f21fce4eed07a63b1ad',hashlib.sha256((root/'runner-source/integration/generated/qualify-proxy-agent-growth-wan-below-tls.sh').read_bytes()).hexdigest())
# These old terminal proof inputs remain historical. The compiled controller
# tuple verifier below separately binds new source, tests, plan and binaries.
s=s.replace('terminal_build_plan="$integration_dir/../../build-runner-review/COMMANDS.json"',f'terminal_build_plan="{old}/build-runner-review/COMMANDS.json"')
s=s.replace('terminal_build_source_inventory="$integration_dir/../../build-runner-review/BUILD-SOURCE-SHA256SUMS"',f'terminal_build_source_inventory="{old}/build-runner-review/BUILD-SOURCE-SHA256SUMS"')
p.write_text(s)
