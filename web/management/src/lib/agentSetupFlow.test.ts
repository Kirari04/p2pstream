import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import type { ManagementClient } from "@/api/managementClient";
import { GetAgentUpdateOverviewResponseSchema, type GetAgentUpdateOverviewResponse } from "@/gen/proto/p2pstream/v1/management_pb";
import { prepareExistingAgentSetup } from "./agentSetupFlow";

const agent = { id: 1n, publicId: "agent-a", name: "Agent A" };
function overview() {
  return create(GetAgentUpdateOverviewResponseSchema, {
    trustedTargets: [{ version: "v1.2.3-staging.84" }],
    managementAuthority: { keyId: "a".repeat(64), publicKey: new Uint8Array(32), epoch: 1n },
    agents: [{ agentId: 1n, agentPublicId: agent.publicId, tunnelVersion: "v1.2.2", tunnelCommit: "b".repeat(40) }],
  });
}
function clientFor(value: GetAgentUpdateOverviewResponse) {
  const calls: string[] = [];
  const client = {
    getAgentUpdateOverview: async () => { calls.push("overview"); return value; },
    generateAgentUpdaterEnrollmentToken: async (request: {agentId: bigint}) => {
      expect(request.agentId).toBe(agent.id);
      calls.push("enrollment");
      return { token: "updater-token", pinnedRepository: "Example/p2pstream", expiresAtUnixMillis: 123n, managementAuthority: value.managementAuthority };
    },
    rotateAgentToken: async () => { throw new Error("Unexpected tunnel token rotation"); },
  } as unknown as ManagementClient;
  return { client, calls };
}

describe("shared agent setup preparation", () => {
  test("reinstall includes update enrollment without rotating the tunnel token", async () => {
    const { client, calls } = clientFor(overview());
    const result = await prepareExistingAgentSetup(client, agent, "reinstall");
    expect(result.managed?.updaterEnrollmentToken).toBe("updater-token");
    expect(result.managed?.updaterPinnedRepository).toBe("Example/p2pstream");
    expect(result.version).toBe("v1.2.3-staging.84");
    expect(calls).toEqual(["overview", "enrollment"]);
  });

  test("enrollment uses the live tunnel identity reported by the selected environment", async () => {
    const { client } = clientFor(overview());
    const result = await prepareExistingAgentSetup(client, agent, "enable-updates");
    expect(result.liveVersion).toBe("v1.2.2");
    expect(result.liveCommit).toBe("b".repeat(40));
  });

  test("a campaign in progress prevents reinstall enrollment", async () => {
    const value = overview();
    value.agents[0]!.activeAssignmentId = 4n;
    const { client, calls } = clientFor(value);
    await expect(prepareExistingAgentSetup(client, agent, "reinstall")).rejects.toThrow("campaign");
    expect(calls).toEqual(["overview"]);
  });

  test("an unknown tunnel build cannot enable updates", async () => {
    const value = overview();
    value.agents[0]!.tunnelCommit = "";
    const { client, calls } = clientFor(value);
    await expect(prepareExistingAgentSetup(client, agent, "enable-updates")).rejects.toThrow("Connect this agent");
    expect(calls).toEqual(["overview"]);
  });

  test("unmanaged repair remains available when update authority is unavailable", async () => {
    const value = overview();
    value.managementAuthority = undefined;
    const { client, calls } = clientFor(value);
    const result = await prepareExistingAgentSetup(client, agent, "reinstall");
    expect(result.managed).toBeNull();
    expect(result.notice).toContain("Managed updates are unavailable");
    expect(calls).toEqual(["overview"]);
    value.agents[0]!.updaterEnrolled = true;
    await expect(prepareExistingAgentSetup(client, agent, "reinstall")).rejects.toThrow("No trusted update release");
  });

  test("closing setup before preparation completes does not issue an enrollment token", async () => {
    const { client, calls } = clientFor(overview());
    await expect(prepareExistingAgentSetup(client, agent, "reinstall", () => false)).rejects.toThrow("Setup was closed");
    expect(calls).toEqual(["overview"]);
  });

  test("stale agent identity from another environment is rejected before token issuance", async () => {
    const value = overview();
    value.agents[0]!.agentPublicId = "another-environments-agent";
    const { client, calls } = clientFor(value);
    await expect(prepareExistingAgentSetup(client, agent, "reinstall")).rejects.toThrow("selected environment");
    expect(calls).toEqual(["overview"]);
  });

  test("switching environments while loading never issues a token on the new environment", async () => {
    const original = clientFor(overview());
    const other = clientFor(overview());
    let current = original.client;
    const routed = new Proxy({} as ManagementClient, { get: (_target, key) => Reflect.get(current, key) });
    const preparing = prepareExistingAgentSetup(routed, agent, "reinstall");
    current = other.client;
    await preparing;
    expect(original.calls).toEqual(["overview", "enrollment"]);
    expect(other.calls).toEqual([]);
  });
});
