import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { AgentManagementService } from "../gen/proto/p2pstream/v1/management_pb";

export function createManagementClient(baseUrl: string) {
  const transport = createConnectTransport({
    baseUrl,
    fetch: (input, init) => fetch(input, { ...init, credentials: "same-origin" }),
  });
  return createClient(AgentManagementService, transport);
}

export type ManagementClient = ReturnType<typeof createManagementClient>;

export const localManagementClient = createManagementClient(window.location.origin);

let activeManagementClient: ManagementClient = localManagementClient;

export function setActiveManagementClientBase(baseUrl: string) {
  activeManagementClient = createManagementClient(baseUrl);
}

export const managementClient = new Proxy({} as ManagementClient, {
  get(_target, prop, receiver) {
    return Reflect.get(activeManagementClient, prop, receiver);
  },
});
