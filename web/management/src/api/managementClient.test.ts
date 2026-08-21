import { describe, expect, test } from "bun:test";
import { createRoutedManagementClient, type ManagementClient } from "./managementClient";

function fakeClient(label: string, calls: string[]): ManagementClient {
  return {
    getDashboard: async () => {
      calls.push(label);
      return { label };
    },
  } as unknown as ManagementClient;
}

describe("createRoutedManagementClient", () => {
  test("delegates calls to the current app-scoped client", async () => {
    const calls: string[] = [];
    const first = fakeClient("first", calls);
    const second = fakeClient("second", calls);
    let current = first;
    const client = createRoutedManagementClient(() => current, () => "");

    expect(await client.getDashboard({})).toEqual({ label: "first" });
    current = second;
    expect(await client.getDashboard({})).toEqual({ label: "second" });
    expect(calls).toEqual(["first", "second"]);
  });

  test("rejects blocked unary calls before resolving or invoking a client", async () => {
    let resolved = false;
    const client = createRoutedManagementClient(
      () => {
        resolved = true;
        return fakeClient("blocked", []);
      },
      () => "Environment is disabled.",
    );

    await expect(client.getDashboard({})).rejects.toThrow("Environment is disabled.");
    expect(resolved).toBe(false);
  });

  test("returns a blocked stream as an async iterable", async () => {
    let resolved = false;
    const client = createRoutedManagementClient(
      () => {
        resolved = true;
        return fakeClient("blocked", []);
      },
      () => "Environment is disabled.",
    );

    const stream = client.streamTrafficTraceEvents({});
    await expect(stream[Symbol.asyncIterator]().next()).rejects.toThrow("Environment is disabled.");
    expect(resolved).toBe(false);
  });
});
