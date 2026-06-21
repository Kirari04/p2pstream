import { computed, ref, watch, type Ref } from "vue";
import { localManagementClient, managementClient, setActiveManagementClientBase } from "@/api/managementClient";
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
  })));
  const selectedRemoteEnvironment = computed(() => {
    if (selectedEnvironmentId.value === "0") return null;
    return environments.value.find((environment) => environment.id.toString() === selectedEnvironmentId.value) ?? null;
  });
  const selectedEnvironmentLabel = computed(() => selectedRemoteEnvironment.value?.name ?? "Local");
  const selectedEnvironmentBlocked = computed(() => {
    const environment = selectedRemoteEnvironment.value;
    if (!environment) return "";
    if (!environment.enabled) return "Environment is disabled.";
    if (environment.trustState !== EnvironmentTrustState.TRUSTED) return "Environment certificate must be trusted before management requests can run.";
    return "";
  });

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

  async function loadEnvironments() {
    if (!currentUser.value) {
      environments.value = [];
      selectedEnvironmentId.value = "0";
      return;
    }
    const resp = await localManagementClient.listEnvironments({});
    environments.value = resp.environments;
    if (selectedEnvironmentId.value !== "0" && !environments.value.some((environment) => environment.id.toString() === selectedEnvironmentId.value && environment.enabled)) {
      selectedEnvironmentId.value = "0";
    }
  }

  function syncSelectedEnvironmentClient() {
    const environment = selectedRemoteEnvironment.value;
    if (!environment) {
      setActiveManagementClientBase(window.location.origin);
    } else {
      setActiveManagementClientBase(`${window.location.origin}/environments/${environment.id.toString()}`);
    }
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
    selectedEnvironmentId.value = "0";
    syncSelectedEnvironmentClient();
  }

  async function loadDashboard() {
    if (isRefreshing.value) {
      pendingDashboardReload.value = true;
      return;
    }
    isRefreshing.value = true;
    error.value = null;
    const loadEnvironmentId = selectedEnvironmentId.value;
    try {
      syncSelectedEnvironmentClient();
      const [dashboardResp, publicProxyResp] = await Promise.all([
        managementClient.getDashboard({}),
        managementClient.getPublicProxyConfig({}),
      ]);
      if (loadEnvironmentId !== selectedEnvironmentId.value) {
        pendingDashboardReload.value = true;
        return;
      }
      dashboard.value = dashboardResp;
      publicProxyConfig.value = publicProxyResp;
    } catch (err) {
      if (loadEnvironmentId === selectedEnvironmentId.value) {
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
      if (!currentUser.value || isBusy.value || isRefreshing.value) return;
      void loadDashboard();
    }, 5000);
  }

  function stopAutoRefresh() {
    if (refreshTimer.value !== null) {
      window.clearInterval(refreshTimer.value);
      refreshTimer.value = null;
    }
  }

  async function loadAuthenticatedData() {
    await loadEnvironments();
    syncSelectedEnvironmentClient();
    await loadDashboard();
    startAutoRefresh();
  }

  watch(selectedEnvironmentId, () => {
    syncSelectedEnvironmentClient();
    if (!currentUser.value) return;
    clearDashboardState();
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
