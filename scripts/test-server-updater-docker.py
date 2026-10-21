#!/usr/bin/env python3
"""Guest-side test driver. Run ONLY in the disposable VM created by the wrapper.

Uses a VM-local registry named ghcr.io; never changes a developer's Docker host.
The Go harness executes the production enrollment, engine, Compose and snapshot
code. This script adds a real TLS-connected agent and an HTTP forwarding route.
"""
import argparse
import base64
import http.cookiejar
import json
import os
from pathlib import Path
import socket
import ssl
import subprocess
import time
import urllib.request


def command(*args, **kwargs):
    return subprocess.check_output(args, text=True, **kwargs).strip()


def docker(*args):
    return command("docker", *args)


def docker_test(*args):
    # Keep Go assertion output visible when a disposable test helper exits.
    # Ordinary Docker inspection output remains captured by docker().
    subprocess.run(["docker", *args], check=True)


def wait_for(fn, seconds=90):
    deadline = time.monotonic() + seconds
    while True:
        try:
            value = fn()
            if value:
                return value
        except (OSError, ValueError, subprocess.CalledProcessError):
            pass
        if time.monotonic() >= deadline:
            raise TimeoutError("fixture did not become ready")
        time.sleep(1)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("scenario", choices=["success", "rollback", "retry-recovery", "kill-validating", "reboot-committing"])
    parser.add_argument("--resume", action="store_true")
    args = parser.parse_args()
    if os.geteuid() != 0 or socket.gethostbyname("ghcr.io") != "127.0.0.1" or not Path("/review/ISOLATED_TEST_VM").exists():
        raise SystemExit("Requires the disposable review VM and its local-only registry")
    state = Path("/review") / args.scenario
    project = "review-" + args.scenario
    canonical = Path(str(state) + "-deployment") / "compose.json"
    executor = project + "-executor"
    images = json.loads(Path("/review/images.json").read_text())
    helper = images["helper"]
    compose_args = ["compose", "--project-name", project, "--project-directory", str(state)]
    cookies = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.HTTPSHandler(context=ssl._create_unverified_context()), urllib.request.HTTPCookieProcessor(cookies))

    def rpc(name, body):
        req = urllib.request.Request("https://127.0.0.1:18081/p2pstream.v1.AgentManagementService/" + name,
            data=json.dumps(body).encode(), headers={"Content-Type": "application/json", "Origin": "https://127.0.0.1:18081"})
        with opener.open(req, timeout=10) as response:
            return json.load(response)

    def compose(file, *extra):
        return docker(*compose_args, "-f", str(file), *extra)

    if not args.resume:
        state.mkdir(mode=0o700)
        model = {
            "name": project,
            "services": {"p2pstream": {
                "image": images["baseline"], "restart": "unless-stopped", "read_only": True,
                "environment": {"CONFIG_DIR": "/data", "MANAGEMENT_PORT": "8081",
                    "MANAGEMENT_SETUP_TOKEN": "review-setup-token-0123456789abcdef",
                    "BOOTSTRAP_AGENT_ID": "review-agent", "BOOTSTRAP_AGENT_NAME": "Review Agent",
                    "BOOTSTRAP_AGENT_TOKEN": "review-agent-token-0123456789abcdef",
                    "MANAGEMENT_TLS_EXTRA_HOSTS": "p2pstream"},
                "volumes": [{"type": "volume", "source": "data", "target": "/data"}],
                "ports": [{"target": 8081, "published": "18081", "host_ip": "127.0.0.1", "protocol": "tcp"},
                          {"target": 8080, "published": "18080", "host_ip": "127.0.0.1", "protocol": "tcp"}],
            }}, "volumes": {"data": {"name": project + "-data"}}}
        original = state / "original.json"
        original.write_text(json.dumps(model))
        compose(original, "up", "-d")
        baseline_container = compose(original, "ps", "-q", "p2pstream")
        installation = wait_for(lambda: docker("exec", baseline_container, "/app/p2pstream", "server-installation-identity"))
        (state / "model.json").write_text(compose(original, "config", "--format", "json"))
        env = ["-e", "REVIEW_STATE=" + str(state), "-e", "REVIEW_SCENARIO=" + args.scenario,
               "-e", "REVIEW_BASELINE=" + images["baseline"], "-e", "REVIEW_HELPER=" + helper,
               "-e", "REVIEW_INSTALLATION=" + installation]
        docker_test("run", "--rm", "-v", "/review:/review", *env, helper, "/app/server-update-test", "-test.v", "-test.run=^TestDockerReviewEnroll$")
        def runtime_action(action):
            return docker("run", "--rm", "-v", "/review:/review", "-v", "/var/run/docker.sock:/var/run/docker.sock",
                "-v", project + "-data:/server-data", helper, "/app/p2pstream", "server-updater", action,
                "--config", str(state / "config.json"))

        runtime_action("runtime-block")
        compose(canonical, "stop", "p2pstream")
        compose(canonical, "up", "--no-start", "--no-deps", "--no-build", "--pull", "never", "p2pstream")
        runtime_action("runtime-allow")
        docker("start", compose(canonical, "ps", "--all", "-q", "p2pstream"))
        wait_for(lambda: docker("exec", compose(canonical, "ps", "-q", "p2pstream"), "/app/p2pstream", "server-health") or True)
        runtime_action("record-runtime")
        # This facade exercises the ordinary default-file Compose entry point.
        (state / "compose.yaml").write_text(json.dumps({"name": project, "include": [str(canonical)]}))

        wait_for(lambda: rpc("GetSetupState", {}) or True)
        rpc("SetupAdmin", {"username": "reviewadmin", "password": "Review-only-password-123!", "setupToken": "review-setup-token-0123456789abcdef"})
        rpc("Login", {"username": "reviewadmin", "password": "Review-only-password-123!"})
        listener = rpc("CreatePublicListener", {"name": "review-public", "bindAddress": "0.0.0.0", "port": "8080", "protocol": "PUBLIC_LISTENER_PROTOCOL_HTTP", "enabled": True})["listener"]
        site = rpc("CreatePublicSite", {"name": "review-public", "enabled": True, "defaultSite": True,
            "listenerBindings": [{"listenerId": listener["id"], "behavior": "PUBLIC_SITE_LISTENER_BEHAVIOR_SERVE"}]})["site"]
        rpc("CreatePublicRoute", {"siteId": site["id"], "priority": "1000", "pathPrefix": "/", "isDefault": True,
            "enabled": True, "action": "PUBLIC_ROUTE_ACTION_FORWARD", "targetLoadBalancing": "PUBLIC_ROUTE_TARGET_LOAD_BALANCING_ROUND_ROBIN",
            "targets": [{"name": "agent upstream", "enabled": True, "weight": "100", "targetType": "PUBLIC_ROUTE_TARGET_TYPE_PROXY",
                "url": "http://upstream:9000", "transport": "PUBLIC_ROUTE_TARGET_TRANSPORT_AGENT",
                "agentSelector": {"matchLabels": {"p2pstream.io/agent-id": "review-agent"}},
                "agentLoadBalancing": "PUBLIC_ROUTE_TARGET_LOAD_BALANCING_ROUND_ROBIN"}]})
        rpc("PublishPublicSite", {"id": site["id"]})
        container = compose(canonical, "ps", "-q", "p2pstream")
        ca = docker("exec", container, "cat", "/data/certs/management/ca.crt.pem")
        (state / "initial-ca.pem").write_text(ca)
        docker("run", "-d", "--name", project + "-upstream", "--network", project + "_default", "--network-alias", "upstream", "--restart", "unless-stopped", "p2pstream-review:upstream")
        docker("run", "-d", "--name", project + "-agent", "--network", project + "_default", "--restart", "unless-stopped",
            "-e", "MANAGEMENT_URL=https://p2pstream:8081", "-e", "MANAGEMENT_CA_PEM_BASE64=" + base64.b64encode(ca.encode()).decode(),
            "-e", "AGENT_ID=review-agent", "-e", "AGENT_TOKEN=review-agent-token-0123456789abcdef", "-e", "AGENT_ALLOW_TARGETS=upstream:9000",
            images["baseline"], "/app/p2pstream", "agent")
        wait_for(lambda: urllib.request.urlopen("http://127.0.0.1:18080/", timeout=5).read() == b"smoke upstream ok\n")
        c = json.loads((state / "config.json").read_text())
        assert c["deployment_protocol"] == 2 and c["deployment_file"] == str(canonical)
        docker_test("run", "--rm", "--label", "p2pstream.server-update.executor=" + c["instance_id"],
            "-v", "/review:/review", "-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", project + "-data:/server-data",
            "-e", "REVIEW_CONFIG=" + str(state / "config.json"), helper,
            "/app/server-update-test", "-test.v", "-test.run=^TestDockerReviewNativeGuard$")
        wait_for(lambda: urllib.request.urlopen("http://127.0.0.1:18080/", timeout=5).read() == b"smoke upstream ok\n")
        docker("run", "-d", "--name", executor, "--restart", "unless-stopped" if args.scenario == "reboot-committing" else "no", "--read-only", "--tmpfs", "/tmp", "--cap-drop", "ALL",
            "--cap-add", "CHOWN", "--cap-add", "DAC_OVERRIDE", "--cap-add", "FOWNER", "--cap-add", "NET_BIND_SERVICE",
            "--label", "p2pstream.server-update.executor=" + c["instance_id"],
            "-v", "/review:/review", "-v", "/var/run/docker.sock:/var/run/docker.sock", "-v", project + "-data:/server-data",
            "-e", "REVIEW_CONFIG=" + str(state / "config.json"), "-e", "REVIEW_SCENARIO=" + args.scenario,
            "-e", "REVIEW_EXECUTOR=" + executor,
            "-e", "REVIEW_CANDIDATE=" + images["candidate"], helper)

    if args.scenario in ("kill-validating", "reboot-committing") and not args.resume:
        wait_for(lambda: (state / "checkpoint").exists(), 180)
        c = compose(canonical, "ps", "-q", "p2pstream")
        policy = docker("inspect", "-f", "{{.HostConfig.RestartPolicy.Name}}", c)
        assert policy == "no", policy
        print("CHECKPOINT " + (state / "checkpoint").read_text(), flush=True)
        if args.scenario == "reboot-committing":
            return
        docker("kill", executor)
        docker("stop", c)
        docker("start", executor)

    try:
        wait_for(lambda: (state / "passed.json").exists() or (state / "failed").exists() or docker("inspect", "-f", "{{.State.Running}}", executor) == "false", 600)
        if (state / "failed").exists() or not (state / "passed.json").exists() or docker("inspect", "-f", "{{.RestartCount}}", executor) != "0":
            raise AssertionError(docker("logs", executor))
        wait_for(lambda: urllib.request.urlopen("http://127.0.0.1:18080/", timeout=5).read() == b"smoke upstream ok\n")
        container = compose(canonical, "ps", "-q", "p2pstream")
        info = json.loads(docker("inspect", container))[0]
        assert info["HostConfig"]["RestartPolicy"]["Name"] == "unless-stopped"
        assert info["HostConfig"]["ReadonlyRootfs"]
        assert not any(m["Destination"] == "/var/run/docker.sock" for m in info["Mounts"])
        assert docker("exec", container, "cat", "/data/certs/management/ca.crt.pem") == (state / "initial-ca.pem").read_text()
        (state / "executor.log").write_text(docker("logs", executor))
        # The current canonical image, including an update committed since
        # enrollment, must survive an ordinary default Compose recreation.
        expected_image = json.loads(canonical.read_text())["services"]["p2pstream"]["image"]
        compose_versions = {"managed": docker("compose", "version", "--short"), "native": docker("--config", "/review/native-docker", "compose", "version", "--short")}
        assert compose_versions["managed"].lstrip("v") != compose_versions["native"].lstrip("v"), compose_versions
        native = subprocess.run(["docker", "--config", "/review/native-docker", "compose", "up", "-d", "--force-recreate"], cwd=state, capture_output=True, text=True, check=True)
        assert "orphan" not in native.stderr.lower(), native.stderr
        wait_for(lambda: urllib.request.urlopen("http://127.0.0.1:18080/", timeout=5).read() == b"smoke upstream ok\n")
        recreated = compose(canonical, "ps", "-q", "p2pstream")
        recreated_info = json.loads(docker("inspect", recreated))[0]
        managed_hash = compose(canonical, "config", "--hash", "p2pstream").split()[1]
        native_hash = recreated_info["Config"]["Labels"]["com.docker.compose.config-hash"]
        assert recreated_info["Config"]["Image"] == expected_image
        assert recreated_info["Config"]["Labels"]["p2pstream.server-update.instance"] == json.loads((state / "config.json").read_text())["instance_id"]
        assert any(m["Destination"] == "/run/p2pstream-server-update" and not m["RW"] for m in recreated_info["Mounts"])
        docker_test("run", "--rm", "-v", "/review:/review", "-v", "/var/run/docker.sock:/var/run/docker.sock",
            "-e", "REVIEW_CONFIG=" + str(state / "config.json"), helper,
            "/app/server-update-test", "-test.v", "-test.run=^TestDockerReviewRuntimeReceipt$")
        receipt_check = json.loads((state / "native-runtime-receipt.json").read_text())
        assert receipt_check["runtime_receipt_verified_directly"] and receipt_check["native_hash"] == native_hash
        # Exercise the shipped serve command, its ownership lock, and the
        # actual non-root manager -> authenticated private-socket boundary.
        compose(canonical, "up", "-d", "p2pstream-server-updater")
        wait_for(lambda: (state / "control/control.sock").exists())
        config = json.loads((state / "config.json").read_text())
        expected = json.loads((state / "passed.json").read_text())
        (state / "control/maintenance").write_text("maintenance bootstrap regression")
        rpc("GetSetupState", {})
        rpc("Login", {"username": "reviewadmin", "password": "Review-only-password-123!"})
        rpc("GetCurrentUser", {})
        rpc("ListEnvironments", {})
        actual = rpc("GetServerUpdateOperation", {"instanceId": config["instance_id"], "operationId": expected["id"]})["operation"]
        assert actual["phase"] == expected["phase"]
        try:
            rpc("CreatePublicListener", {"name": "must-be-blocked"})
            raise AssertionError("maintenance admitted ordinary mutation")
        except urllib.error.HTTPError as error:
            assert error.code == 503
        (state / "control/maintenance").unlink()
        updater = compose(canonical, "ps", "-q", "p2pstream-server-updater")
        checked = json.loads(docker("exec", updater, "/app/p2pstream", "server-updater", "check", "--config", str(state / "config.json")))
        assert checked["instance_id"] == config["instance_id"] and checked["ready"]
        duplicate = subprocess.run(["docker", "exec", updater, "/app/p2pstream", "server-updater", "recover", "--config", str(state / "config.json")], capture_output=True, text=True)
        assert duplicate.returncode != 0 and "another executor owns" in duplicate.stdout + duplicate.stderr
        (state / "production-command-checks.json").write_text(json.dumps({"operation": actual, "maintenance_bootstrap": "passed", "duplicate_executor_refused": True, "deployment_protocol": 2, "native_recreation_preserved_image": expected_image, "compose_versions": compose_versions, "managed_hash": managed_hash, "native_hash": native_hash, "receipt_fallback_required": native_hash != managed_hash}))
        print("PASS " + args.scenario + ": agent route, TLS authority, snapshot, journal, restart policy, read-only runtime, production UDS/auth/lock", flush=True)
    finally:
        docker("update", "--restart=no", executor)
        docker("stop", executor)
        compose(canonical, "stop", "p2pstream", "p2pstream-server-updater")
        docker("stop", project + "-agent", project + "-upstream")


if __name__ == "__main__":
    main()
