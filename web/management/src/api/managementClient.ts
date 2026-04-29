import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { AgentManagementService } from "../gen/proto/p2pstream/v1/management_pb";

const transport = createConnectTransport({
  baseUrl: window.location.origin,
  fetch: (input, init) => fetch(input, { ...init, credentials: "same-origin" }),
});

export const managementClient = createClient(AgentManagementService, transport);
