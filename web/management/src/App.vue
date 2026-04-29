<script setup lang="ts">
import BanIcon from "@primevue/icons/ban";
import CheckIcon from "@primevue/icons/check";
import PlusIcon from "@primevue/icons/plus";
import RefreshIcon from "@primevue/icons/refresh";
import TimesIcon from "@primevue/icons/times";
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import { managementClient } from "@/api/managementClient";
import Button from "@/volt/Button.vue";
import Card from "@/volt/Card.vue";
import DangerButton from "@/volt/DangerButton.vue";
import InputText from "@/volt/InputText.vue";
import Message from "@/volt/Message.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import Skeleton from "@/volt/Skeleton.vue";
import Tag from "@/volt/Tag.vue";
import Toolbar from "@/volt/Toolbar.vue";
import {
  ProxyState,
  type DashboardWindowSummary,
  type GetDashboardResponse,
  type GetSetupStateResponse,
  type User,
} from "@/gen/proto/p2pstream/v1/management_pb";

const setupState = ref<GetSetupStateResponse | null>(null);
const currentUser = ref<User | null>(null);
const dashboard = ref<GetDashboardResponse | null>(null);
const isLoading = ref(true);
const isBusy = ref(false);
const isRefreshing = ref(false);
const refreshTimer = ref<number | null>(null);
const error = ref<string | null>(null);

const setupForm = ref({ username: "admin", password: "" });
const loginForm = ref({ username: "admin", password: "" });

const status = computed(() => dashboard.value?.status ?? null);
const trafficWindows = computed(() => dashboard.value?.windows ?? []);
const oneHourWindow = computed(() => windowByLabel("1h"));
const dayWindow = computed(() => windowByLabel("24h"));

const setupExpiresAt = computed(() => formatDate(setupState.value?.setupExpiresAtUnixMillis));
const latestStatsTime = computed(() => {
  const millis = status.value?.latestAgentStats?.reportedAtUnixMillis ?? 0n;
  return millis === 0n ? "No stats reported" : formatDate(millis);
});

const proxyState = computed(() => status.value?.proxy?.state ?? ProxyState.UNSPECIFIED);
const proxyIsRunning = computed(() => proxyState.value === ProxyState.RUNNING || status.value?.proxyRunning === true);
const proxySeverity = computed(() => {
  if (proxyState.value === ProxyState.RUNNING) return "success";
  if (proxyState.value === ProxyState.STARTING || proxyState.value === ProxyState.STOPPING) return "warn";
  return "danger";
});
const agentSeverity = computed(() => (status.value?.agentConnected ? "success" : "danger"));
const proxyError = computed(() => status.value?.proxy?.lastError || status.value?.proxyLastError || "");

function proxyStateLabel(state: ProxyState): string {
  switch (state) {
    case ProxyState.STOPPED:
      return "Stopped";
    case ProxyState.STARTING:
      return "Starting";
    case ProxyState.RUNNING:
      return "Running";
    case ProxyState.STOPPING:
      return "Stopping";
    case ProxyState.ERROR:
      return "Error";
    default:
      return status.value?.proxyRunning ? "Running" : "Unknown";
  }
}

function windowByLabel(label: string): DashboardWindowSummary | undefined {
  return dashboard.value?.windows.find((item) => item.label === label);
}

function proxyErrors(window: DashboardWindowSummary): bigint {
  return window.proxyClientError + window.proxyServerError + window.proxyInternalError;
}

function agentErrors(window: DashboardWindowSummary): bigint {
  return window.agentReqClientError + window.agentReqServerError + window.agentReqInternalError;
}

function bigIntLabel(value: bigint | undefined, fallback = "0"): string {
  if (value === undefined) return fallback;
  return new Intl.NumberFormat().format(Number(value));
}

function formatBytes(value: bigint | undefined): string {
  if (value === undefined) return "0 B";
  const bytes = Number(value);
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${trimDecimal(bytes / 1024)} KB`;
  return `${trimDecimal(bytes / 1024 / 1024)} MB`;
}

function formatDuration(value: bigint | undefined): string {
  if (value === undefined || value === 0n) return "-";
  const millis = Number(value);
  if (millis < 1000) return `${millis} ms`;
  return `${trimDecimal(millis / 1000)} s`;
}

function formatDate(value: bigint | undefined): string {
  if (value === undefined || value === 0n) return "-";
  return new Date(Number(value)).toLocaleString();
}

function trimDecimal(value: number): string {
  return new Intl.NumberFormat(undefined, {
    maximumFractionDigits: 1,
  }).format(value);
}

function messageFromError(err: unknown): string {
  return err instanceof Error ? err.message : "Request failed";
}

async function bootstrap() {
  isLoading.value = true;
  error.value = null;
  stopAutoRefresh();

  try {
    setupState.value = await managementClient.getSetupState({});
    if (setupState.value.setupRequired) {
      currentUser.value = null;
      dashboard.value = null;
      return;
    }

    try {
      const userResp = await managementClient.getCurrentUser({});
      currentUser.value = userResp.user ?? null;
    } catch {
      currentUser.value = null;
      dashboard.value = null;
      return;
    }

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
    return;
  }

  isRefreshing.value = true;
  error.value = null;

  try {
    dashboard.value = await managementClient.getDashboard({});
  } catch (err) {
    error.value = messageFromError(err);
  } finally {
    isRefreshing.value = false;
  }
}

function startAutoRefresh() {
  stopAutoRefresh();
  if (!currentUser.value) {
    return;
  }

  refreshTimer.value = window.setInterval(() => {
    if (!currentUser.value || isBusy.value || isRefreshing.value) {
      return;
    }
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
    await managementClient.setupAdmin({
      username: setupForm.value.username,
      password: setupForm.value.password,
    });
    await login(setupForm.value.username, setupForm.value.password);
    setupState.value = await managementClient.getSetupState({});
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
  const loginResp = await managementClient.login({ username, password });
  currentUser.value = loginResp.user ?? null;
}

async function logout() {
  isBusy.value = true;
  error.value = null;

  try {
    await managementClient.logout({});
    stopAutoRefresh();
    currentUser.value = null;
    dashboard.value = null;
    loginForm.value.password = "";
  } catch (err) {
    error.value = messageFromError(err);
  } finally {
    isBusy.value = false;
  }
}

async function setProxyRunning(shouldRun: boolean) {
  isBusy.value = true;
  error.value = null;

  try {
    if (shouldRun) {
      await managementClient.startProxy({});
    } else {
      await managementClient.stopProxy({});
    }
    await loadDashboard();
  } catch (err) {
    error.value = messageFromError(err);
  } finally {
    isBusy.value = false;
  }
}

onMounted(() => {
  void bootstrap();
});

onBeforeUnmount(() => {
  stopAutoRefresh();
});
</script>

<template>
  <main class="min-h-screen bg-surface-100 px-4 py-6 text-surface-800 sm:px-6 lg:px-8">
    <div class="mx-auto grid w-full max-w-[1120px] gap-4">
      <Toolbar class="border-surface-200 bg-surface-0 shadow-sm">
        <template #start>
          <div class="min-w-0">
            <p class="m-0 text-xs font-bold uppercase text-surface-500">management</p>
            <h1 class="m-0 text-3xl font-semibold leading-none tracking-normal text-surface-950 sm:text-4xl">
              p2pstream
            </h1>
          </div>
        </template>

        <template #end>
          <div class="flex flex-wrap items-center justify-end gap-2">
            <Tag v-if="currentUser" severity="secondary" :value="currentUser.username" />
            <SecondaryButton
              v-if="currentUser"
              label="Refresh"
              size="small"
              type="button"
              class="min-w-24"
              :loading="isRefreshing"
              :disabled="isRefreshing || isBusy"
              @click="loadDashboard"
            >
              <template #icon>
                <RefreshIcon class="h-4 w-4" />
              </template>
            </SecondaryButton>
            <SecondaryButton
              v-if="currentUser"
              label="Log out"
              size="small"
              type="button"
              :disabled="isBusy"
              @click="logout"
            >
              <template #icon>
                <TimesIcon class="h-4 w-4" />
              </template>
            </SecondaryButton>
          </div>
        </template>
      </Toolbar>

      <Message v-if="error" severity="error" :closable="false" class="break-words" aria-live="polite">
        {{ error }}
      </Message>

      <section v-if="isLoading" class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4" aria-live="polite">
        <Card v-for="idx in 8" :key="idx" class="border border-surface-200 shadow-sm">
          <template #content>
            <div class="grid gap-3">
              <Skeleton width="40%" height="0.875rem" />
              <Skeleton width="80%" height="1.75rem" />
            </div>
          </template>
        </Card>
      </section>

      <Card
        v-else-if="setupState?.setupRequired && setupState.setupAvailable"
        class="w-full max-w-[460px] border border-surface-200 shadow-sm"
      >
        <template #title>Setup admin</template>
        <template #subtitle>
          <span class="text-sm text-surface-500">
            {{ setupExpiresAt !== "-" ? `Available until ${setupExpiresAt}` : "First-run setup" }}
          </span>
        </template>
        <template #content>
          <form class="grid gap-4" @submit.prevent="submitSetup">
            <label class="grid gap-1.5 text-sm font-medium text-surface-700">
              Username
              <InputText v-model="setupForm.username" autocomplete="username" required />
            </label>
            <label class="grid gap-1.5 text-sm font-medium text-surface-700">
              Password
              <InputText
                v-model="setupForm.password"
                autocomplete="new-password"
                minlength="12"
                required
                type="password"
              />
            </label>
            <Button label="Create admin" type="submit" :loading="isBusy" :disabled="isBusy">
              <template #icon>
                <CheckIcon class="h-4 w-4" />
              </template>
            </Button>
          </form>
        </template>
      </Card>

      <Message
        v-else-if="setupState?.setupRequired"
        severity="error"
        :closable="false"
        class="max-w-[720px] break-words"
      >
        <strong class="block">Setup locked</strong>
        <span>{{ setupState.setupUnavailableReason }}</span>
      </Message>

      <Card v-else-if="!currentUser" class="w-full max-w-[460px] border border-surface-200 shadow-sm">
        <template #title>Log in</template>
        <template #subtitle>
          <span class="text-sm text-surface-500">Admin session required</span>
        </template>
        <template #content>
          <form class="grid gap-4" @submit.prevent="submitLogin">
            <label class="grid gap-1.5 text-sm font-medium text-surface-700">
              Username
              <InputText v-model="loginForm.username" autocomplete="username" required />
            </label>
            <label class="grid gap-1.5 text-sm font-medium text-surface-700">
              Password
              <InputText v-model="loginForm.password" autocomplete="current-password" required type="password" />
            </label>
            <Button label="Log in" type="submit" :loading="isBusy" :disabled="isBusy">
              <template #icon>
                <CheckIcon class="h-4 w-4" />
              </template>
            </Button>
          </form>
        </template>
      </Card>

      <section v-else-if="dashboard && status" class="grid gap-4">
        <section class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <Card class="min-w-0 border border-surface-200 shadow-sm">
            <template #content>
              <div class="grid min-h-24 gap-2">
                <div class="flex items-center justify-between gap-3">
                  <span class="text-sm text-surface-500">Proxy state</span>
                  <Tag :severity="proxySeverity" :value="proxyStateLabel(proxyState)" class="w-fit" />
                </div>
                <strong class="break-words text-lg font-semibold text-surface-950">
                  {{ proxyStateLabel(proxyState) }}
                </strong>
              </div>
            </template>
          </Card>

          <Card class="min-w-0 border border-surface-200 shadow-sm">
            <template #content>
              <div class="grid min-h-24 gap-2">
                <div class="flex items-center justify-between gap-3">
                  <span class="text-sm text-surface-500">Agent state</span>
                  <Tag
                    :severity="agentSeverity"
                    :value="status.agentConnected ? 'Connected' : 'Disconnected'"
                    class="w-fit"
                  />
                </div>
                <strong class="break-words text-lg font-semibold text-surface-950">
                  {{ status.agentConnected ? "Connected" : "Disconnected" }}
                </strong>
              </div>
            </template>
          </Card>

          <Card class="min-w-0 border border-surface-200 shadow-sm">
            <template #content>
              <div class="grid min-h-24 gap-2">
                <span class="text-sm text-surface-500">Target origin</span>
                <strong class="break-words text-base font-semibold text-surface-950">
                  {{ status.targetOrigin || "Not configured" }}
                </strong>
              </div>
            </template>
          </Card>

          <Card class="min-w-0 border border-surface-200 shadow-sm">
            <template #content>
              <div class="grid min-h-24 gap-2">
                <span class="text-sm text-surface-500">Last agent report</span>
                <strong class="break-words text-base font-semibold text-surface-950">{{ latestStatsTime }}</strong>
              </div>
            </template>
          </Card>
        </section>

        <Card class="border border-surface-200 shadow-sm">
          <template #content>
            <div class="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
              <div class="grid gap-1">
                <div class="flex flex-wrap items-center gap-2">
                  <span class="text-sm font-medium text-surface-500">Proxy controls</span>
                  <Tag :severity="proxySeverity" :value="proxyStateLabel(proxyState)" />
                </div>
                <p v-if="proxyError" class="m-0 break-words text-sm text-red-700">{{ proxyError }}</p>
                <p v-else class="m-0 text-sm text-surface-500">Start or stop the public listener.</p>
              </div>

              <div class="flex flex-wrap items-center gap-2">
                <Button
                  label="Start proxy"
                  type="button"
                  class="min-w-32"
                  :disabled="isBusy || proxyIsRunning"
                  :loading="isBusy && !proxyIsRunning"
                  @click="setProxyRunning(true)"
                >
                  <template #icon>
                    <PlusIcon class="h-4 w-4" />
                  </template>
                </Button>
                <DangerButton
                  label="Stop proxy"
                  type="button"
                  class="min-w-32"
                  :disabled="isBusy || !proxyIsRunning"
                  :loading="isBusy && proxyIsRunning"
                  @click="setProxyRunning(false)"
                >
                  <template #icon>
                    <BanIcon class="h-4 w-4" />
                  </template>
                </DangerButton>
              </div>
            </div>
          </template>
        </Card>

        <section class="grid gap-3">
          <div class="flex flex-wrap items-end justify-between gap-2">
            <div>
              <h2 class="m-0 text-base font-semibold text-surface-950">Traffic windows</h2>
              <p class="m-0 text-sm text-surface-500">
                Aggregate counts only, retained for {{ bigIntLabel(dashboard.retentionDays) }} days.
              </p>
            </div>
            <span class="text-xs text-surface-500">Updated {{ formatDate(dashboard.generatedAtUnixMillis) }}</span>
          </div>

          <Card class="hidden border border-surface-200 shadow-sm lg:block">
            <template #content>
              <table class="w-full table-fixed border-collapse text-left text-sm">
                <thead>
                  <tr class="border-b border-surface-200 text-xs font-semibold uppercase text-surface-500">
                    <th class="w-[9%] py-2 pr-3">Window</th>
                    <th class="w-[12%] py-2 pr-3 text-right">Proxy req</th>
                    <th class="w-[12%] py-2 pr-3 text-right">Proxy ok</th>
                    <th class="w-[12%] py-2 pr-3 text-right">Proxy err</th>
                    <th class="w-[12%] py-2 pr-3 text-right">Avg</th>
                    <th class="w-[12%] py-2 pr-3 text-right">Agent ok</th>
                    <th class="w-[12%] py-2 pr-3 text-right">Agent err</th>
                    <th class="w-[10%] py-2 pr-3 text-right">In</th>
                    <th class="w-[9%] py-2 text-right">Out</th>
                  </tr>
                </thead>
                <tbody>
                  <tr v-for="item in trafficWindows" :key="item.label" class="border-b border-surface-100 last:border-0">
                    <td class="py-2 pr-3 font-semibold text-surface-950">{{ item.label }}</td>
                    <td class="py-2 pr-3 text-right tabular-nums">{{ bigIntLabel(item.proxyRequests) }}</td>
                    <td class="py-2 pr-3 text-right tabular-nums text-green-700">
                      {{ bigIntLabel(item.proxySuccess) }}
                    </td>
                    <td class="py-2 pr-3 text-right tabular-nums text-red-700">
                      {{ bigIntLabel(proxyErrors(item)) }}
                    </td>
                    <td class="py-2 pr-3 text-right tabular-nums">{{ formatDuration(item.proxyAvgDurationMs) }}</td>
                    <td class="py-2 pr-3 text-right tabular-nums text-green-700">
                      {{ bigIntLabel(item.agentReqSuccess) }}
                    </td>
                    <td class="py-2 pr-3 text-right tabular-nums text-red-700">
                      {{ bigIntLabel(agentErrors(item)) }}
                    </td>
                    <td class="py-2 pr-3 text-right tabular-nums">{{ formatBytes(item.agentBytesReceived) }}</td>
                    <td class="py-2 text-right tabular-nums">{{ formatBytes(item.agentBytesSent) }}</td>
                  </tr>
                </tbody>
              </table>
            </template>
          </Card>

          <div class="grid gap-3 lg:hidden">
            <Card v-for="item in trafficWindows" :key="item.label" class="border border-surface-200 shadow-sm">
              <template #content>
                <div class="grid gap-3">
                  <div class="flex items-center justify-between">
                    <strong class="text-base text-surface-950">{{ item.label }}</strong>
                    <span class="text-xs text-surface-500">since {{ formatDate(item.sinceUnixMillis) }}</span>
                  </div>
                  <dl class="grid grid-cols-2 gap-x-4 gap-y-2 text-sm">
                    <dt class="text-surface-500">Proxy requests</dt>
                    <dd class="m-0 text-right font-semibold tabular-nums">{{ bigIntLabel(item.proxyRequests) }}</dd>
                    <dt class="text-surface-500">Proxy success</dt>
                    <dd class="m-0 text-right font-semibold text-green-700 tabular-nums">
                      {{ bigIntLabel(item.proxySuccess) }}
                    </dd>
                    <dt class="text-surface-500">Proxy errors</dt>
                    <dd class="m-0 text-right font-semibold text-red-700 tabular-nums">
                      {{ bigIntLabel(proxyErrors(item)) }}
                    </dd>
                    <dt class="text-surface-500">Avg duration</dt>
                    <dd class="m-0 text-right font-semibold tabular-nums">{{ formatDuration(item.proxyAvgDurationMs) }}</dd>
                    <dt class="text-surface-500">Agent success</dt>
                    <dd class="m-0 text-right font-semibold text-green-700 tabular-nums">
                      {{ bigIntLabel(item.agentReqSuccess) }}
                    </dd>
                    <dt class="text-surface-500">Agent errors</dt>
                    <dd class="m-0 text-right font-semibold text-red-700 tabular-nums">
                      {{ bigIntLabel(agentErrors(item)) }}
                    </dd>
                    <dt class="text-surface-500">Bytes in</dt>
                    <dd class="m-0 text-right font-semibold tabular-nums">{{ formatBytes(item.agentBytesReceived) }}</dd>
                    <dt class="text-surface-500">Bytes out</dt>
                    <dd class="m-0 text-right font-semibold tabular-nums">{{ formatBytes(item.agentBytesSent) }}</dd>
                  </dl>
                </div>
              </template>
            </Card>
          </div>
        </section>

        <section class="grid gap-3">
          <h2 class="m-0 text-base font-semibold text-surface-950">Agent health</h2>
          <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            <Card class="min-w-0 border border-surface-200 shadow-sm">
              <template #content>
                <div class="grid min-h-20 gap-2">
                  <span class="text-sm text-surface-500">Latest memory</span>
                  <strong class="text-xl font-semibold text-surface-950">
                    {{ bigIntLabel(status.latestAgentStats?.memorySysMb) }} MB
                  </strong>
                </div>
              </template>
            </Card>
            <Card class="min-w-0 border border-surface-200 shadow-sm">
              <template #content>
                <div class="grid min-h-20 gap-2">
                  <span class="text-sm text-surface-500">Goroutines</span>
                  <strong class="text-xl font-semibold text-surface-950">
                    {{ bigIntLabel(status.latestAgentStats?.numGoroutine) }}
                  </strong>
                </div>
              </template>
            </Card>
            <Card class="min-w-0 border border-surface-200 shadow-sm">
              <template #content>
                <div class="grid min-h-20 gap-2">
                  <span class="text-sm text-surface-500">Active requests</span>
                  <strong class="text-xl font-semibold text-surface-950">
                    {{ status.latestAgentStats?.activeRequests ?? 0 }}
                  </strong>
                </div>
              </template>
            </Card>
            <Card class="min-w-0 border border-surface-200 shadow-sm">
              <template #content>
                <div class="grid min-h-20 gap-2">
                  <span class="text-sm text-surface-500">Avg memory, 1h</span>
                  <strong class="text-xl font-semibold text-surface-950">
                    {{ bigIntLabel(oneHourWindow?.agentAvgMemoryMb) }} MB
                  </strong>
                </div>
              </template>
            </Card>
            <Card class="min-w-0 border border-surface-200 shadow-sm">
              <template #content>
                <div class="grid min-h-20 gap-2">
                  <span class="text-sm text-surface-500">Max memory, 24h</span>
                  <strong class="text-xl font-semibold text-surface-950">
                    {{ bigIntLabel(dayWindow?.agentMaxMemoryMb) }} MB
                  </strong>
                </div>
              </template>
            </Card>
            <Card class="min-w-0 border border-surface-200 shadow-sm">
              <template #content>
                <div class="grid min-h-20 gap-2">
                  <span class="text-sm text-surface-500">Max goroutines, 24h</span>
                  <strong class="text-xl font-semibold text-surface-950">
                    {{ bigIntLabel(dayWindow?.agentMaxGoroutines) }}
                  </strong>
                </div>
              </template>
            </Card>
          </div>
        </section>

        <Card class="border border-surface-200 shadow-sm">
          <template #content>
            <div class="grid gap-3 lg:grid-cols-[minmax(0,1fr)_minmax(0,2fr)] lg:items-start">
              <div class="grid gap-2">
                <span class="text-sm font-medium text-surface-500">Agent connections</span>
                <Tag
                  :severity="dashboard.agentConnections?.connected ? 'success' : 'danger'"
                  :value="dashboard.agentConnections?.connected ? 'Connected' : 'Disconnected'"
                  class="w-fit"
                />
              </div>
              <dl class="grid grid-cols-1 gap-2 text-sm sm:grid-cols-2">
                <div class="flex items-center justify-between gap-3">
                  <dt class="text-surface-500">Total retained</dt>
                  <dd class="m-0 font-semibold tabular-nums">
                    {{ bigIntLabel(dashboard.agentConnections?.totalConnections) }}
                  </dd>
                </div>
                <div class="flex items-center justify-between gap-3">
                  <dt class="text-surface-500">Active since</dt>
                  <dd class="m-0 text-right font-semibold">
                    {{ formatDate(dashboard.agentConnections?.activeConnectedAtUnixMillis) }}
                  </dd>
                </div>
                <div class="flex items-center justify-between gap-3">
                  <dt class="text-surface-500">Last connected</dt>
                  <dd class="m-0 text-right font-semibold">
                    {{ formatDate(dashboard.agentConnections?.lastConnectedAtUnixMillis) }}
                  </dd>
                </div>
                <div class="flex items-center justify-between gap-3">
                  <dt class="text-surface-500">Last disconnected</dt>
                  <dd class="m-0 text-right font-semibold">
                    {{ formatDate(dashboard.agentConnections?.lastDisconnectedAtUnixMillis) }}
                  </dd>
                </div>
              </dl>
            </div>
          </template>
        </Card>
      </section>
    </div>
  </main>
</template>
