import { expect, type APIRequestContext, type APIResponse } from "@playwright/test";

const servicePath = "/p2pstream.v1.AgentManagementService/";

export async function connectRPC<T>(
  request: APIRequestContext,
  baseURL: string,
  method: string,
  payload: Record<string, unknown>,
): Promise<T> {
  const response = await connectRPCResponse(request, baseURL, method, payload);
  return await response.json() as T;
}

export async function connectRPCResponse(
  request: APIRequestContext,
  baseURL: string,
  method: string,
  payload: Record<string, unknown>,
): Promise<APIResponse> {
  const response = await request.post(new URL(servicePath + method, baseURL).toString(), {
    data: payload,
    headers: {
      "Content-Type": "application/json",
    },
  });
  if (!response.ok()) {
    const text = await response.text();
    expect(response.ok(), `${method} failed with ${response.status()}: ${text}`).toBeTruthy();
  }
  return response;
}
