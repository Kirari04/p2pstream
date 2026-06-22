import { computed, onScopeDispose, ref, watch, type Ref } from "vue";
import {
  createManagementClient,
  createRoutedManagementClient,
  localManagementClient,
  type ManagementClient,
} from "@/api/managementClient";
import { messageFromError } from "@/lib/errors";
import {
  EnvironmentTrustState,
  type Environment,
  type GetDashboardResponse,
  type GetPublicProxyConfigResponse,
  type User,
} from "@/gen/proto/p2pstream/v1/management_pb";

interface DashboardRefreshOptions {
  currentUser: Ref<User | null>;
  // Session-owned refs shared with dashboard refresh so global request state stays centralized.
  error: Ref<string | null>;
  isBusy: Ref<boolean>;
  isLoading: Ref<boolean>;
}

export function useDashboardRefresh({ currentUser, error, isBusy, isLoading }: DashboardRefreshOptions) {
  const dashboard = ref<GetDashboardResponse | null>(null);
  const publicProxyConfig = ref<GetPublicProxyConfigResponse | null>(null);
  const environments = ref<Environment[]>([]);
  const selectedEnvironmentId = ref(loadSelectedEnvironmentId());
  const isRefreshing = ref(false);
  const pendingDashboardReload = ref(false);
  const refreshTimer = ref<number | null>(null);
  const remoteManagementClients = new Map<string, ManagementClient>();

  const environmentOptions = computed(() => [
    { id: "0", name: "Local", enabled: true, trustState: EnvironmentTrustState.TRUSTED },
    ...environments.value.map((environment) => ({
      id: environment.id.toString(),
      name: environment.name,
      enabled: environment.enabled,
      trustState: environment.trustState,
    })),
  ]);
  const environmentSelectOptions = computed(() => environmentOptions.value.map((environment) => ({
    label: `${environment.name}${environment.enabled ? "" : " (disabled)"}`,
    value: environment.id,
    disabled: environment.id !== "0" && (environment.trustState !== EnvironmentTrustState.TRUSTED || !environment.enabled),
  })));
  const selectedRemoteEnvironment = computed(() => {
    if (selectedEnvironmentId.value === "0") return null;
    return environments.value.find((environment) => environment.id.toString() === selectedEnvironmentId.value) ?? null;
  });
  const selectedEnvironmentLabel = computed(() => {
    if (selectedEnvironmentId.value === "0") return "Local";
    return selectedRemoteEnvironment.value?.name ?? "Unavailable environment";
  });
  const selectedEnvironmentBlocked = computed(() => {
    if (selectedEnvironmentId.value !== "0" && !selectedRemoteEnvironment.value) {
      return "Selected environment is no longer available.";
    }
    const environment = selectedRemoteEnvironment.value;
    if (!environment) return "";
    if (!environment.enabled) return "Environment is disabled.";
    if (environment.trustState !== EnvironmentTrustState.TRUSTED) return "Environment certificate must be trusted before management requests can run.";
    return "";
  });
  const selectedManagementClient = computed(() => {
    const environment = selectedRemoteEnvironment.value;
    if (!environment) return localManagementClient;
    const baseUrl = remoteEnvironmentBaseUrl(environment.id);
    let client = remoteManagementClients.get(baseUrl);
    if (!client) {
      client = createManagementClient(baseUrl);
      remoteManagementClients.set(baseUrl, client);
    }
    return client;
  });
  const managementClient = createRoutedManagementClient(
    () => selectedManagementClient.value,
    () => selectedEnvironmentBlocked.value,
  );

  function loadSelectedEnvironmentId(): string {
    try {
      return window.localStorage.getItem("p2pstream:selected-environment") || "0";
    } catch {
      return "0";
    }
  }

  function persistSelectedEnvironmentId() {
    try {
      window.localStorage.setItem("p2pstream:selected-environment", selectedEnvironmentId.value);
    } catch {
      // Ignore private browsing/storage failures.
    }
  }

  function remoteEnvironmentBaseUrl(environmentId: bigint | string): string {
    return `${window.location.origin}/environments/${environmentId.toString()}`;
  }

  function pruneRemoteManagementClients() {
    const activeBaseUrls = new Set(environments.value.map((environment) => remoteEnvironmentBaseUrl(environment.id)));
    for (const baseUrl of remoteManagementClients.keys()) {
      if (!activeBaseUrls.has(baseUrl)) {
        remoteManagementClients.delete(baseUrl);
      }
    }
  }

  async function loadEnvironments() {
    if (!currentUser.value) {
      environments.value = [];
      remoteManagementClients.clear();
      selectedEnvironmentId.value = "0";
      return;
    }
    const resp = await localManagementClient.listEnvironments({});
    environments.value = resp.environments;
    pruneRemoteManagementClients();
    if (selectedEnvironmentId.value !== "0" && !environments.value.some((environment) => environment.id.toString() === selectedEnvironmentId.value)) {
      selectedEnvironmentId.value = "0";
    }
  }

  function syncSelectedEnvironmentSelection() {
    persistSelectedEnvironmentId();
  }

  function clearDashboardState() {
    dashboard.value = null;
    publicProxyConfig.value = null;
  }

  function clearAuthenticatedData() {
    stopAutoRefresh();
    clearDashboardState();
    environments.value = [];
    remoteManagementClients.clear();
    selectedEnvironmentId.value = "0";
    syncSelectedEnvironmentSelection();
  }

  async function loadDashboard() {
    if (isRefreshing.value) {
      pendingDashboardReload.value = true;
      return;
    }
    isRefreshing.value = true;
    error.value = null;
    const loadEnvironmentId = selectedEnvironmentId.value;
    const loadBlockedReason = selectedEnvironmentBlocked.value;
    if (loadBlockedReason) {
      clearDashboardState();
      pendingDashboardReload.value = false;
      isRefreshing.value = false;
      return;
    }
    const loadClient = selectedManagementClient.value;
    try {
      const [dashboardResp, publicProxyResp] = await Promise.all([
        loadClient.getDashboard({}),
        loadClient.getPublicProxyConfig({}),
      ]);
      if (loadEnvironmentId !== selectedEnvironmentId.value || loadBlockedReason !== selectedEnvironmentBlocked.value) {
        pendingDashboardReload.value = true;
        return;
      }
      dashboard.value = dashboardResp;
      publicProxyConfig.value = publicProxyResp;
    } catch (err) {
      if (loadEnvironmentId === selectedEnvironmentId.value && loadBlockedReason === selectedEnvironmentBlocked.value) {
        error.value = messageFromError(err);
      } else {
        pendingDashboardReload.value = true;
      }
    } finally {
      isRefreshing.value = false;
      if (pendingDashboardReload.value && currentUser.value) {
        pendingDashboardReload.value = false;
        void loadDashboard();
      }
    }
  }

  function startAutoRefresh() {
    stopAutoRefresh();
    if (!currentUser.value) return;
    refreshTimer.value = window.setInterval(() => {
      if (!currentUser.value || selectedEnvironmentBlocked.value || isBusy.value || isRefreshing.value) return;
      void loadDashboard();
    }, 5000);
  }

  function stopAutoRefresh() {
    if (refreshTimer.value !== null) {
      window.clearInterval(refreshTimer.value);
      refreshTimer.value = null;
    }
  }

  onScopeDispose(stopAutoRefresh);

  async function loadAuthenticatedData() {
    await loadEnvironments();
    syncSelectedEnvironmentSelection();
    await loadDashboard();
    startAutoRefresh();
  }

  watch([selectedEnvironmentId, selectedEnvironmentBlocked], () => {
    syncSelectedEnvironmentSelection();
    if (!currentUser.value) return;
    clearDashboardState();
    if (selectedEnvironmentBlocked.value) return;
    if (isLoading.value) {
      pendingDashboardReload.value = true;
      return;
    }
    void loadDashboard();
  });

  return {
    dashboard,
    publicProxyConfig,
    environments,
    selectedEnvironmentId,
    selectedEnvironmentLabel,
    selectedEnvironmentBlocked,
    environmentSelectOptions,
    managementClient,
    isRefreshing,
    loadEnvironments,
    loadDashboard,
    loadAuthenticatedData,
    clearDashboardState,
    clearAuthenticatedData,
    startAutoRefresh,
    stopAutoRefresh,
  };
}
