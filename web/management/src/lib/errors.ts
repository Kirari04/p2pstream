export function messageFromError(err: unknown): string {
  return err instanceof Error ? err.message : "Request failed";
}
