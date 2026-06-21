import type { ComputedRef, InjectionKey } from "vue";
import type { ManagementClient } from "@/api/managementClient";
import type {
  Environment,
  GetDashboardResponse,
  GetPublicProxyConfigResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

export type ManagementActionRunner = (action: () => Promise<void>, successMessage?: string) => Promise<boolean>;

export const dashboardKey = Symbol("dashboard") as InjectionKey<ComputedRef<GetDashboardResponse | null>>;
export const publicProxyConfigKey = Symbol("publicProxyConfig") as InjectionKey<ComputedRef<GetPublicProxyConfigResponse | null>>;
export const isBusyKey = Symbol("isBusy") as InjectionKey<ComputedRef<boolean>>;
export const managementClientKey = Symbol("managementClient") as InjectionKey<ManagementClient>;
export const environmentsKey = Symbol("environments") as InjectionKey<ComputedRef<Environment[]>>;
export const selectedEnvironmentIdKey = Symbol("selectedEnvironmentId") as InjectionKey<ComputedRef<string>>;
export const selectedEnvironmentLabelKey = Symbol("selectedEnvironmentLabel") as InjectionKey<ComputedRef<string>>;
export const selectedEnvironmentBlockedKey = Symbol("selectedEnvironmentBlocked") as InjectionKey<ComputedRef<string>>;
export const reloadEnvironmentsKey = Symbol("reloadEnvironments") as InjectionKey<() => Promise<void>>;
export const runManagementActionKey = Symbol("runManagementAction") as InjectionKey<ManagementActionRunner>;
export const setProxyRunningKey = Symbol("setProxyRunning") as InjectionKey<(shouldRun: boolean) => Promise<void>>;
export const logoutKey = Symbol("logout") as InjectionKey<() => void>;
