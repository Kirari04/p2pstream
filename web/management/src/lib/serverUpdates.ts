export const serverUpdatePhases = ["accepted", "pulling", "preparing", "stopping", "backing_up", "deploying", "validating", "committing"] as const;

export function serverUpdateTerminal(phase: string): boolean {
  // Recovery still owns the host operation and must continue blocking a new one.
  return ["succeeded", "rolled_back", "failed"].includes(phase);
}

export function serverUpdatePaused(phase: string): boolean {
  return serverUpdateTerminal(phase) || phase === "recovery_required";
}

export const serverUpdateStages = [
  { phase: "accepted", label: "Queued", detail: "The host updater accepted this request. It continues independently of this page." },
  { phase: "pulling", label: "Download images", detail: "The updater downloads these images before it stops the current server. Download time depends on image size, cached layers, and the host’s connection." },
  { phase: "preparing", label: "Prepare server", detail: "Checking the deployment and pausing management changes before the restart." },
  { phase: "stopping", label: "Stop server", detail: "Stopping the current container. Management, public requests, and agent tunnels are interrupted during replacement." },
  { phase: "backing_up", label: "Back up data", detail: "Saving the stopped server’s data so the previous version can be restored if validation fails." },
  { phase: "deploying", label: "Replace container", detail: "Starting the selected release with the saved deployment configuration." },
  { phase: "validating", label: "Validate replacement", detail: "Checking the replacement’s readiness, sustained stability, and required agent reconnections. The page can reconnect before these checks finish." },
  { phase: "committing", label: "Finalize update", detail: "Recording the accepted release and finishing the host update. Wait for the confirmed outcome below." },
] as const;

export function serverUpdatePhaseLabel(phase: string): string {
  return ({
    accepted: "Update queued", pulling: "Downloading verified images", preparing: "Preparing server",
    stopping: "Stopping server", backing_up: "Backing up data", deploying: "Replacing container",
    validating: "Validating the replacement", committing: "Finishing update",
    rolling_back: "Restoring the previous version", succeeded: "Update complete",
    rolled_back: "Update failed · previous version restored", failed: "Update stopped before activation", recovery_required: "Host recovery required",
  } as Record<string, string>)[phase] ?? "Waiting for a recognized update stage";
}

export function serverUpdateExplanation(phase: string): string {
  return serverUpdateStages.find((stage) => stage.phase === phase)?.detail ?? ({
    rolling_back: "The update could not complete. The host updater is restoring the previous version and its saved data. Wait for the recovery outcome before taking further action.",
    succeeded: "The host updater accepted the replacement after its validation checks. This operation is complete.",
    rolled_back: "The selected release was not retained. The host updater restored the previous version and completed its recovery checks.",
    failed: "The update ended before the replacement was activated. Review the host’s failure details, resolve the cause, then preview the release again.",
    recovery_required: "Automatic recovery could not finish. Further updates are blocked until an operator recovers this deployment on the Docker host.",
  } as Record<string, string>)[phase] ?? "The host reported an unfamiliar stage. Keep this operation open and refresh its status; a new update remains blocked.";
}

export function serverUpdateTone(phase: string): "success" | "warning" | "error" | "info" {
  if (phase === "succeeded") return "success";
  if (phase === "recovery_required" || phase === "failed") return "error";
  if (phase === "rolling_back" || phase === "rolled_back") return "warning";
  return "info";
}

export function serverUpdateDuration(milliseconds: number): string {
  if (!Number.isFinite(milliseconds) || milliseconds < 0) return "Unavailable";
  const seconds = Math.floor(milliseconds / 1000);
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ${String(seconds % 60).padStart(2, "0")}s`;
  return `${Math.floor(minutes / 60)}h ${String(minutes % 60).padStart(2, "0")}m`;
}

export function serverUpdateElapsed(operation: { phase: string; startedAtUnixMillis: bigint; updatedAtUnixMillis: bigint }, now: number) {
  const start = Number(operation.startedAtUnixMillis);
  const updated = Number(operation.updatedAtUnixMillis);
  const end = serverUpdatePaused(operation.phase) ? updated : now;
  return {
    total: start > 0 && end >= start ? serverUpdateDuration(end - start) : "Unavailable",
    stage: updated > 0 && now >= updated && !serverUpdatePaused(operation.phase) ? serverUpdateDuration(now - updated) : "Unavailable",
  };
}

export function serverUpdateDiagnostics(environment: string, instanceId: string, operation: {
  id: string; phase: string; detail: string; previousVersion: string; targetVersion: string; startedAtUnixMillis: bigint; updatedAtUnixMillis: bigint;
}): string {
  // Explicit allowlist: never include preview tokens, browser retry records, or setup commands.
  return JSON.stringify({ environment, instanceId, operationId: operation.id, phase: operation.phase,
    previousVersion: operation.previousVersion, targetVersion: operation.targetVersion, detail: operation.detail,
    startedAtUnixMillis: operation.startedAtUnixMillis.toString(), updatedAtUnixMillis: operation.updatedAtUnixMillis.toString(),
  }, null, 2);
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
