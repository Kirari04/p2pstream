<script setup lang="ts">
import { RefreshCw as RefreshIcon } from "@lucide/vue";
import { NAlert, NButton, NCard, NForm, NFormItem, NInput, NSelect, NSkeleton, useMessage, useNotification } from "naive-ui";
import { computed, onBeforeUnmount, onMounted, ref, provide, watch } from "vue";
import { useRoute } from "vue-router";
import { localManagementClient, managementClient, setActiveManagementClientBase } from "@/api/managementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import ThemeToggle from "@/components/ui/ThemeToggle.vue";
import { BUSY_REASON } from "@/lib/disabledReasons";
import {
  EnvironmentTrustState,
  type Environment,
  type GetDashboardResponse,
  type GetPublicProxyConfigResponse,
  type GetSetupStateResponse,
  type User,
} from "@/gen/proto/p2pstream/v1/management_pb";

const message = useMessage();
const notification = useNotification();
const route = useRoute();

const setupState = ref<GetSetupStateResponse | null>(null);
const currentUser = ref<User | null>(null);
const dashboard = ref<GetDashboardResponse | null>(null);
const publicProxyConfig = ref<GetPublicProxyConfigResponse | null>(null);
const environments = ref<Environment[]>([]);
const selectedEnvironmentId = ref(loadSelectedEnvironmentId());
const isLoading = ref(true);
const isBusy = ref(false);
const isRefreshing = ref(false);
const pendingDashboardReload = ref(false);
const isLogoutConfirmOpen = ref(false);
const refreshTimer = ref<number | null>(null);
const error = ref<string | null>(null);

const tabs = [
  { path: "/overview", label: "Overview" },
  { path: "/diagnostics", label: "Diagnostics" },
  { path: "/traffic", label: "Traffic" },
  { path: "/proxy", label: "Proxy" },
  { path: "/agent", label: "Agents" },
  { path: "/policies", label: "Traffic Policy" },
  { path: "/templates", label: "Templates" },
  { path: "/tls", label: "TLS" },
  { path: "/settings", label: "Settings" },
];

const sourceOfferHref = "/.well-known/p2pstream/source";
const sourceOfferTitle = computed(() => {
  const repository = import.meta.env.VITE_RELEASE_REPOSITORY?.trim();
  const ref = import.meta.env.VITE_RELEASE_REF?.trim();
  if (repository && ref) return `View source for ${repository}@${ref}`;
  return "View source and license";
});

const setupForm = ref({ username: "admin", password: "" });
const setupToken = ref("");
const loginForm = ref({ username: "admin", password: "" });
const refreshDisabledReason = computed(() => {
  if (isRefreshing.value) return "Dashboard refresh is already running.";
  if (isBusy.value) return BUSY_REASON;
  return "";
});
const busyDisabledReason = computed(() => isBusy.value ? BUSY_REASON : "");
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
const canShowRouteContent = computed(() => Boolean(dashboard.value) || route.path.startsWith("/settings"));

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

watch(selectedEnvironmentId, () => {
  syncSelectedEnvironmentClient();
  if (!currentUser.value) return;
  dashboard.value = null;
  publicProxyConfig.value = null;
  if (isLoading.value) {
    pendingDashboardReload.value = true;
    return;
  }
  void loadDashboard();
});

// Provide state to views
provide('dashboard', computed(() => dashboard.value));
provide('publicProxyConfig', computed(() => publicProxyConfig.value));
provide('isBusy', computed(() => isBusy.value));
provide('managementClient', managementClient);
provide('environments', computed(() => environments.value));
provide('selectedEnvironmentId', computed(() => selectedEnvironmentId.value));
provide('selectedEnvironmentLabel', selectedEnvironmentLabel);
provide('selectedEnvironmentBlocked', selectedEnvironmentBlocked);
provide('reloadEnvironments', loadEnvironments);

async function bootstrap() {
  isLoading.value = true;
  error.value = null;
  stopAutoRefresh();

  try {
    setupState.value = await localManagementClient.getSetupState({});
    if (setupState.value.setupRequired) {
      currentUser.value = null;
      dashboard.value = null;
      publicProxyConfig.value = null;
      return;
    }

    try {
      const userResp = await localManagementClient.getCurrentUser({});
      currentUser.value = userResp.user ?? null;
    } catch {
      currentUser.value = null;
      dashboard.value = null;
      publicProxyConfig.value = null;
      return;
    }

    await loadEnvironments();
    syncSelectedEnvironmentClient();
    await loadDashboard();
    startAutoRefresh();
  } catch (err) {
    error.value = messageFromError(err);
  } finally {
    isLoading.value = false;
  }
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

async function submitSetup() {
  isBusy.value = true;
  error.value = null;
  try {
    await localManagementClient.setupAdmin({
      username: setupForm.value.username,
      password: setupForm.value.password,
      setupToken: setupToken.value,
    });
    await login(setupForm.value.username, setupForm.value.password);
    setupState.value = await localManagementClient.getSetupState({});
    await loadDashboard();
    startAutoRefresh();
  } catch (err) {
    error.value = messageFromError(err);
  } finally {
    isBusy.value = false;
  }
}

async function submitLogin() {
  isBusy.value = true;
  error.value = null;
  try {
    await login(loginForm.value.username, loginForm.value.password);
    await loadDashboard();
    startAutoRefresh();
  } catch (err) {
    error.value = messageFromError(err);
  } finally {
    isBusy.value = false;
  }
}

async function login(username: string, password: string) {
  const loginResp = await localManagementClient.login({ username, password });
  currentUser.value = loginResp.user ?? null;
  await loadEnvironments();
  syncSelectedEnvironmentClient();
}

function requestLogout() {
  if (isBusy.value) return;
  isLogoutConfirmOpen.value = true;
}

function cancelLogout() {
  if (isBusy.value) return;
  isLogoutConfirmOpen.value = false;
}

async function confirmLogout() {
  const didLogout = await logout();
  if (didLogout) {
    isLogoutConfirmOpen.value = false;
  }
}

async function logout(): Promise<boolean> {
  isBusy.value = true;
  error.value = null;
  try {
    await localManagementClient.logout({});
    stopAutoRefresh();
    currentUser.value = null;
    dashboard.value = null;
    publicProxyConfig.value = null;
    environments.value = [];
    selectedEnvironmentId.value = "0";
    syncSelectedEnvironmentClient();
    loginForm.value.password = "";
    return true;
  } catch (err) {
    error.value = messageFromError(err);
    return false;
  } finally {
    isBusy.value = false;
  }
}

async function setProxyRunning(shouldRun: boolean) {
  await runManagementAction(async () => {
    if (shouldRun) {
      await managementClient.startProxy({});
    } else {
      await managementClient.stopProxy({});
    }
  });
}

async function runManagementAction(action: () => Promise<void>, successMessage?: string): Promise<boolean> {
  isBusy.value = true;
  error.value = null;
  try {
    await action();
    await loadDashboard();
    if (successMessage) {
      message.success(successMessage);
    }
    return true;
  } catch (err) {
    error.value = messageFromError(err);
    notification.error({
      title: "Operation failed",
      content: messageFromError(err),
      duration: 5000,
    });
    return false;
  } finally {
    isBusy.value = false;
  }
}

provide('setProxyRunning', setProxyRunning);
provide('runManagementAction', runManagementAction);
provide('logout', requestLogout);

function messageFromError(err: unknown): string {
  return err instanceof Error ? err.message : "Request failed";
}

function setupTokenFromURL(): string {
  const routeToken = stringQueryValue(route.query.setup_token);
  if (routeToken) {
    scrubSetupTokenFromURL();
    return routeToken;
  }
  try {
    const token = new URLSearchParams(window.location.search).get("setup_token")?.trim() ?? "";
    if (token) scrubSetupTokenFromURL();
    return token;
  } catch {
    return "";
  }
}

function scrubSetupTokenFromURL() {
  try {
    const url = new URL(window.location.href);
    if (!url.searchParams.has("setup_token")) return;
    url.searchParams.delete("setup_token");
    window.history.replaceState(window.history.state, "", `${url.pathname}${url.search}${url.hash}`);
  } catch {
    // Ignore browsers or test environments without full history support.
  }
}

function stringQueryValue(value: unknown): string {
  if (Array.isArray(value)) return stringQueryValue(value[0]);
  return typeof value === "string" ? value.trim() : "";
}

onMounted(() => {
  setupToken.value = setupTokenFromURL();
  void bootstrap();
});

onBeforeUnmount(() => {
  stopAutoRefresh();
});
</script>

<template>
  <div class="min-h-screen bg-[var(--app-bg)] text-[var(--app-text)]">
    <header class="sticky top-0 z-50 border-b border-[var(--app-border)] bg-[var(--app-shell)]/95 backdrop-blur-xl">
      <div class="mx-auto max-w-7xl px-4 sm:px-6 lg:px-8">
        <div class="flex h-16 items-center justify-between">
          <div class="flex items-center gap-4">
            <div class="flex items-center gap-2">
              <div class="flex h-8 w-8 items-center justify-center rounded-lg bg-[var(--app-accent)] text-white shadow-sm">
                <div class="h-3.5 w-3.5 rotate-45 rounded-[2px] border-2 border-white"></div>
              </div>
              <span class="text-xl font-semibold tracking-normal">p2pstream</span>
            </div>
            <div class="hidden h-6 w-px bg-[var(--app-border)] sm:block"></div>
            <div v-if="currentUser" class="hidden sm:flex items-center gap-2">
              <span class="text-sm font-medium text-[var(--app-text-muted)]">{{ currentUser.username }}</span>
            </div>
          </div>

          <div class="flex items-center gap-3">
            <label v-if="currentUser" class="hidden items-center gap-2 text-xs font-semibold uppercase tracking-normal text-[var(--app-text-muted)] md:flex">
              Environment
              <NSelect
                v-model:value="selectedEnvironmentId"
                data-testid="environment-select"
                size="small"
                class="w-52"
                :options="environmentSelectOptions"
                :title="`Selected environment: ${selectedEnvironmentLabel}`"
              />
            </label>
            <a
              :href="sourceOfferHref"
              :title="sourceOfferTitle"
              :aria-label="sourceOfferTitle"
              class="inline-flex text-sm font-medium text-[var(--app-text-muted)] transition-colors hover:text-[var(--app-text)]"
              target="_blank"
              rel="noreferrer"
            >
              Source
            </a>
            <ThemeToggle />
            <DisabledHint v-if="currentUser" :disabled="Boolean(refreshDisabledReason)" :reason="refreshDisabledReason">
              <NButton
                secondary
                size="small"
                :loading="isRefreshing"
                :disabled="Boolean(refreshDisabledReason)"
                aria-label="Refresh dashboard"
                title="Refresh dashboard"
                @click="loadDashboard"
              >
                <template #icon>
                  <RefreshIcon class="h-3.5 w-3.5" />
                </template>
              </NButton>
            </DisabledHint>
            <DisabledHint v-if="currentUser" :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
              <NButton
                secondary
                size="small"
                :disabled="Boolean(busyDisabledReason)"
                @click="requestLogout"
              >
                Log out
              </NButton>
            </DisabledHint>
          </div>
        </div>
      </div>

      <div v-if="currentUser && !isLoading && !setupState?.setupRequired" class="mx-auto max-w-7xl px-4 sm:px-6 lg:px-8">
        <nav class="flex gap-2 overflow-x-auto no-scrollbar pb-2">
          <router-link
            v-for="tab in tabs"
            :key="tab.path"
            :to="tab.path"
            class="rounded-full px-3 py-1.5 text-sm font-medium transition-colors"
            active-class="bg-[var(--app-accent-soft)] text-[var(--app-accent)]"
          >
            {{ tab.label }}
          </router-link>
        </nav>
      </div>
    </header>

    <main class="mx-auto max-w-7xl px-4 py-8 sm:px-6 lg:px-8">
      <NAlert v-if="error" type="error" class="mb-6" :bordered="false">
        {{ error }}
      </NAlert>
      <NAlert v-if="selectedEnvironmentBlocked" type="warning" class="mb-6" :bordered="false">
        {{ selectedEnvironmentBlocked }}
      </NAlert>

      <div v-if="isLoading" class="grid gap-6 sm:grid-cols-2 lg:grid-cols-4">
        <div v-for="i in 4" :key="i" class="app-card p-6">
          <NSkeleton text width="40%" height="0.75rem" class="mb-3" />
          <NSkeleton text width="70%" height="1.5rem" />
        </div>
      </div>

      <div v-else-if="setupState?.setupRequired && setupState.setupAvailable" class="max-w-md mx-auto py-12">
        <NCard :bordered="false" class="shadow-sm">
          <h2 class="mb-2 text-2xl font-semibold tracking-normal">Setup Admin</h2>
          <p class="mb-6 text-sm text-[var(--app-text-muted)]">Create the primary administrator account.</p>
          <form class="grid gap-4" @submit.prevent="submitSetup">
            <NForm :model="setupForm">
              <NFormItem label="Username" path="username">
                <NInput v-model:value="setupForm.username" autocomplete="username" />
              </NFormItem>
              <NFormItem label="Password" path="password">
                <NInput v-model:value="setupForm.password" type="password" autocomplete="new-password" minlength="12" />
              </NFormItem>
            </NForm>
            <NButton type="primary" attr-type="submit" class="mt-4 w-full" :loading="isBusy">
              Complete Setup
            </NButton>
          </form>
        </NCard>
      </div>

      <div v-else-if="setupState?.setupRequired" class="max-w-xl mx-auto py-12">
        <NCard :bordered="false" class="shadow-sm">
          <div class="mb-3 inline-flex rounded-full border border-red-200 bg-red-50 px-2.5 py-1 text-xs font-semibold text-red-600 dark:border-red-900/50 dark:bg-red-500/10 dark:text-red-300">
            Setup locked
          </div>
          <h2 class="mb-2 text-2xl font-semibold tracking-normal">Restart required</h2>
          <p class="break-words text-sm leading-6 text-[var(--app-text-muted)]">
            {{ setupState.setupUnavailableReason || "Setup window expired; restart the server to retry setup." }}
          </p>
        </NCard>
      </div>

      <div v-else-if="!currentUser && !isLoading" class="max-w-md mx-auto py-12">
        <NCard :bordered="false" class="shadow-sm">
          <h2 class="mb-2 text-2xl font-semibold tracking-normal">Login</h2>
          <p class="mb-6 text-sm text-[var(--app-text-muted)]">Enter your credentials to access the dashboard.</p>
          <form class="grid gap-4" @submit.prevent="submitLogin">
            <NForm :model="loginForm">
              <NFormItem label="Username" path="username">
                <NInput v-model:value="loginForm.username" autocomplete="username" />
              </NFormItem>
              <NFormItem label="Password" path="password">
                <NInput v-model:value="loginForm.password" type="password" autocomplete="current-password" />
              </NFormItem>
            </NForm>
            <NButton type="primary" attr-type="submit" class="mt-4 w-full" :loading="isBusy">
              Continue
            </NButton>
          </form>
        </NCard>
      </div>

      <router-view v-else-if="canShowRouteContent"></router-view>
    </main>

    <div
      v-if="isLogoutConfirmOpen && currentUser"
      class="fixed inset-0 z-[80] flex items-center justify-center bg-black/40 px-4 backdrop-blur-sm"
      role="presentation"
      @click.self="cancelLogout"
    >
      <section
        class="w-full max-w-md rounded-xl border border-[var(--app-border)] bg-[var(--app-panel)] p-6 shadow-2xl shadow-black/10"
        role="dialog"
        aria-modal="true"
        aria-labelledby="logout-confirm-title"
        aria-describedby="logout-confirm-description"
      >
        <div class="mb-5">
          <div class="mb-3 inline-flex rounded-full border border-[var(--app-border)] px-2.5 py-1 text-xs font-semibold text-[var(--app-text-muted)]">
            Session
          </div>
          <h2 id="logout-confirm-title" class="mb-2 text-xl font-semibold tracking-normal text-[var(--app-text)]">
            Log out of p2pstream?
          </h2>
          <p id="logout-confirm-description" class="text-sm leading-6 text-[var(--app-text-muted)]">
            Your current session will end and dashboard data will be cleared from this browser view.
          </p>
        </div>

        <div class="flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
          <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
            <NButton
              secondary
              :disabled="Boolean(busyDisabledReason)"
              @click="cancelLogout"
            >
              Stay logged in
            </NButton>
          </DisabledHint>
          <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
            <NButton
              type="error"
              :loading="isBusy"
              :disabled="Boolean(busyDisabledReason)"
              @click="confirmLogout"
            >
              Log out
            </NButton>
          </DisabledHint>
        </div>
      </section>
    </div>
  </div>
</template>

<style>
nav a:not(.router-link-active) {
  color: var(--app-text-muted);
}

nav a:not(.router-link-active):hover {
  background: var(--app-panel-muted);
  color: var(--app-text);
}
</style>
