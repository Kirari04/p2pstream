import { computed, inject } from "vue";
import type { ComputedRef } from "vue";
import type {
  GetDashboardResponse,
  GetPublicProxyConfigResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

export type ManagementActionRunner = (action: () => Promise<void>, successMessage?: string) => Promise<boolean>;

export function useManagementContext() {
  const dashboard = inject<ComputedRef<GetDashboardResponse | null>>("dashboard", computed(() => null));
  const publicProxyConfig = inject<ComputedRef<GetPublicProxyConfigResponse | null>>("publicProxyConfig", computed(() => null));
  const isBusy = inject<ComputedRef<boolean>>("isBusy", computed(() => false));
  const runManagementAction = inject<ManagementActionRunner | undefined>("runManagementAction", undefined);
  const setProxyRunning = inject<((shouldRun: boolean) => Promise<void>) | undefined>("setProxyRunning", undefined);
  const logout = inject<(() => void) | undefined>("logout", undefined);

  return {
    dashboard,
    publicProxyConfig,
    isBusy,
    runManagementAction,
    setProxyRunning,
    logout,
  };
}
