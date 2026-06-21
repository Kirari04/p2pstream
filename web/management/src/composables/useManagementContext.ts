import { computed, inject } from "vue";
import {
  dashboardKey,
  environmentsKey,
  isBusyKey,
  logoutKey,
  publicProxyConfigKey,
  runManagementActionKey,
  selectedEnvironmentIdKey,
  setProxyRunningKey,
} from "@/composables/managementContextKeys";
export type { ManagementActionRunner } from "@/composables/managementContextKeys";

export function useManagementContext() {
  const dashboard = inject(dashboardKey, computed(() => null));
  const publicProxyConfig = inject(publicProxyConfigKey, computed(() => null));
  const isBusy = inject(isBusyKey, computed(() => false));
  const environments = inject(environmentsKey, computed(() => []));
  const selectedEnvironmentId = inject(selectedEnvironmentIdKey, computed(() => "0"));
  const runManagementAction = inject(runManagementActionKey, undefined);
  const setProxyRunning = inject(setProxyRunningKey, undefined);
  const logout = inject(logoutKey, undefined);

  return {
    dashboard,
    publicProxyConfig,
    isBusy,
    environments,
    selectedEnvironmentId,
    runManagementAction,
    setProxyRunning,
    logout,
  };
}
