export const serverUpdatePhases = ["accepted", "pulling", "preparing", "stopping", "backing_up", "deploying", "validating", "committing"] as const;

export function serverUpdateTerminal(phase: string): boolean {
  return ["succeeded", "rolled_back", "failed"].includes(phase);
}

export function serverUpdatePhaseLabel(phase: string): string {
  return ({
    accepted: "Update queued", pulling: "Downloading verified image", preparing: "Preparing server",
    stopping: "Stopping server", backing_up: "Backing up data", deploying: "Replacing container",
    validating: "Checking health and agent reconnections", committing: "Finishing update",
    rolling_back: "Restoring the previous version", succeeded: "Update complete",
    rolled_back: "Previous version restored", failed: "Update stopped", recovery_required: "Host recovery required",
  } as Record<string, string>)[phase] ?? "Checking update status";
}

export interface PendingServerUpdate { instanceId: string; operationId: string; planToken: string }

export function parsePendingServerUpdate(value: string | null): PendingServerUpdate | null {
  try {
    if (!value || value.length > 100_000) return null;
    const parsed = JSON.parse(value);
    if (typeof parsed.instanceId !== "string" || typeof parsed.operationId !== "string" || typeof parsed.planToken !== "string" ||
        !/^[0-9a-f-]{36}$/.test(parsed.instanceId) || !/^[0-9a-f-]{36}$/.test(parsed.operationId) || !parsed.planToken) return null;
    return { instanceId: parsed.instanceId, operationId: parsed.operationId, planToken: parsed.planToken };
  } catch { return null; }
}
