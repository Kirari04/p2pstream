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

const localOrigin = typeof window === "undefined" ? "http://localhost" : window.location.origin;

export const localManagementClient = createManagementClient(localOrigin);

export function createRoutedManagementClient(
  resolveClient: () => ManagementClient,
  resolveBlockedReason: () => string,
): ManagementClient {
  return new Proxy({} as ManagementClient, {
    get(_target, prop) {
      const blockedReason = resolveBlockedReason();
      if (blockedReason) {
        if (isStreamingManagementMethod(prop)) {
          return async function* blockedStream() {
            throw new Error(blockedReason);
          };
        }
        return () => Promise.reject(new Error(blockedReason));
      }

      const client = resolveClient();
      const value = Reflect.get(client, prop, client);
      if (typeof value !== "function") return value;
      return value.bind(client);
    },
  });
}

function isStreamingManagementMethod(prop: string | symbol): boolean {
  if (typeof prop !== "string") return false;
  const method = (AgentManagementService.method as Record<string, { methodKind?: string } | undefined>)[prop];
  return method?.methodKind === "server_streaming" ||
    method?.methodKind === "client_streaming" ||
    method?.methodKind === "bidi_streaming";
}
