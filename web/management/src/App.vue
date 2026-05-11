<script setup lang="ts">
import RefreshIcon from "@primevue/icons/refresh";
import { computed, onBeforeUnmount, onMounted, ref, provide } from "vue";
import { managementClient } from "@/api/managementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import { BUSY_REASON } from "@/lib/disabledReasons";
import Button from "@/volt/Button.vue";
import DangerButton from "@/volt/DangerButton.vue";
import Message from "@/volt/Message.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import Skeleton from "@/volt/Skeleton.vue";
import {
  type GetDashboardResponse,
  type GetPublicProxyConfigResponse,
  type GetSetupStateResponse,
  type User,
} from "@/gen/proto/p2pstream/v1/management_pb";

const setupState = ref<GetSetupStateResponse | null>(null);
const currentUser = ref<User | null>(null);
const dashboard = ref<GetDashboardResponse | null>(null);
const publicProxyConfig = ref<GetPublicProxyConfigResponse | null>(null);
const isLoading = ref(true);
const isBusy = ref(false);
const isRefreshing = ref(false);
const isLogoutConfirmOpen = ref(false);
const refreshTimer = ref<number | null>(null);
const error = ref<string | null>(null);

const tabs = [
  { path: "/overview", label: "Overview" },
  { path: "/traffic", label: "Traffic" },
  { path: "/agent", label: "Agent Health" },
  { path: "/management", label: "Management" },
];

const setupForm = ref({ username: "admin", password: "" });
const loginForm = ref({ username: "admin", password: "" });
const refreshDisabledReason = computed(() => {
  if (isRefreshing.value) return "Dashboard refresh is already running.";
  if (isBusy.value) return BUSY_REASON;
  return "";
});
const busyDisabledReason = computed(() => isBusy.value ? BUSY_REASON : "");

// Provide state to views
provide('dashboard', computed(() => dashboard.value));
provide('publicProxyConfig', computed(() => publicProxyConfig.value));
provide('isBusy', computed(() => isBusy.value));

async function bootstrap() {
  isLoading.value = true;
  error.value = null;
  stopAutoRefresh();

  try {
    setupState.value = await managementClient.getSetupState({});
    if (setupState.value.setupRequired) {
      currentUser.value = null;
      dashboard.value = null;
      publicProxyConfig.value = null;
      return;
    }

    try {
      const userResp = await managementClient.getCurrentUser({});
      currentUser.value = userResp.user ?? null;
    } catch {
      currentUser.value = null;
      dashboard.value = null;
      publicProxyConfig.value = null;
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
  if (isRefreshing.value) return;
  isRefreshing.value = true;
  error.value = null;
  try {
    const [dashboardResp, publicProxyResp] = await Promise.all([
      managementClient.getDashboard({}),
      managementClient.getPublicProxyConfig({}),
    ]);
    dashboard.value = dashboardResp;
    publicProxyConfig.value = publicProxyResp;
  } catch (err) {
    error.value = messageFromError(err);
  } finally {
    isRefreshing.value = false;
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
    await managementClient.logout({});
    stopAutoRefresh();
    currentUser.value = null;
    dashboard.value = null;
    publicProxyConfig.value = null;
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

async function runManagementAction(action: () => Promise<void>): Promise<boolean> {
  isBusy.value = true;
  error.value = null;
  try {
    await action();
    await loadDashboard();
    return true;
  } catch (err) {
    error.value = messageFromError(err);
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

onMounted(() => {
  void bootstrap();
});

onBeforeUnmount(() => {
  stopAutoRefresh();
});
</script>

<template>
  <div class="min-h-screen bg-black text-[#ededed]">
    <!-- Top Header -->
    <header class="border-b border-[#333] bg-black sticky top-0 z-50">
      <div class="mx-auto max-w-7xl px-4 sm:px-6 lg:px-8">
        <div class="flex h-16 items-center justify-between">
          <div class="flex items-center gap-4">
            <div class="flex items-center gap-2">
              <div class="h-6 w-6 bg-white rounded-sm flex items-center justify-center">
                <div class="h-3 w-3 bg-black transform rotate-45"></div>
              </div>
              <span class="text-xl font-bold tracking-tight">p2pstream</span>
            </div>
            <div class="h-6 w-px bg-[#333] hidden sm:block"></div>
            <div v-if="currentUser" class="hidden sm:flex items-center gap-2">
              <span class="text-sm font-medium text-[#888]">{{ currentUser.username }}</span>
            </div>
          </div>

          <div class="flex items-center gap-3">
            <DisabledHint v-if="currentUser" :disabled="Boolean(refreshDisabledReason)" :reason="refreshDisabledReason">
              <SecondaryButton
                size="small"
                class="!bg-transparent !border-[#333] hover:!border-[#666] !text-[#ededed] h-8"
                :loading="isRefreshing"
                :disabled="Boolean(refreshDisabledReason)"
                aria-label="Refresh dashboard"
                title="Refresh dashboard"
                @click="loadDashboard"
              >
                <template #icon>
                  <RefreshIcon class="h-3.5 w-3.5" />
                </template>
              </SecondaryButton>
            </DisabledHint>
            <DisabledHint v-if="currentUser" :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
              <SecondaryButton
                label="Log out"
                size="small"
                class="!bg-transparent !border-[#333] hover:!border-[#666] !text-[#888] h-8"
                :disabled="Boolean(busyDisabledReason)"
                @click="requestLogout"
              />
            </DisabledHint>
          </div>
        </div>
      </div>

      <!-- Navigation Tabs (Only if logged in) -->
      <div v-if="currentUser && !isLoading && !setupState?.setupRequired" class="mx-auto max-w-7xl px-4 sm:px-6 lg:px-8">
        <nav class="flex gap-6 overflow-x-auto no-scrollbar">
          <router-link
            v-for="tab in tabs"
            :key="tab.path"
            :to="tab.path"
            class="pb-3 text-sm font-medium transition-colors border-b-2"
            active-class="border-white text-white"
            class-inactive="border-transparent text-[#888] hover:text-[#ededed]"
          >
            {{ tab.label }}
          </router-link>
        </nav>
      </div>
    </header>

    <main class="mx-auto max-w-7xl px-4 py-8 sm:px-6 lg:px-8">
      <Message v-if="error" severity="error" class="mb-6 border-[#333] !bg-black !text-red-500">
        {{ error }}
      </Message>

      <!-- Loading State -->
      <div v-if="isLoading" class="grid gap-6 sm:grid-cols-2 lg:grid-cols-4">
        <div v-for="i in 4" :key="i" class="vercel-card p-6">
          <Skeleton width="40%" height="0.75rem" class="mb-3" />
          <Skeleton width="70%" height="1.5rem" />
        </div>
      </div>

      <!-- Setup View -->
      <div v-else-if="setupState?.setupRequired && setupState.setupAvailable" class="max-w-md mx-auto py-12">
        <div class="vercel-card p-8">
          <h2 class="text-2xl font-bold mb-2">Setup Admin</h2>
          <p class="text-[#888] text-sm mb-6">Create the primary administrator account.</p>
          <form class="grid gap-4" @submit.prevent="submitSetup">
            <div class="grid gap-1.5">
              <label class="text-xs font-medium text-[#888] uppercase tracking-wider">Username</label>
              <input v-model="setupForm.username" type="text" class="vercel-input" autocomplete="username" required />
            </div>
            <div class="grid gap-1.5">
              <label class="text-xs font-medium text-[#888] uppercase tracking-wider">Password</label>
              <input
                v-model="setupForm.password"
                type="password"
                class="vercel-input"
                autocomplete="new-password"
                minlength="12"
                required
              />
            </div>
            <Button label="Complete Setup" type="submit" class="mt-4 !bg-white !text-black !border-white w-full" :loading="isBusy" />
          </form>
        </div>
      </div>

      <!-- Setup Locked View -->
      <div v-else-if="setupState?.setupRequired" class="max-w-xl mx-auto py-12">
        <div class="vercel-card border-red-900/50 p-8">
          <div class="mb-3 inline-flex rounded-full border border-red-900/50 px-2.5 py-1 text-xs font-medium text-red-500">
            Setup locked
          </div>
          <h2 class="mb-2 text-2xl font-bold">Restart required</h2>
          <p class="break-words text-sm leading-6 text-[#888]">
            {{ setupState.setupUnavailableReason || "Setup window expired; restart the server to retry setup." }}
          </p>
        </div>
      </div>

      <!-- Login View -->
      <div v-else-if="!currentUser && !isLoading" class="max-w-md mx-auto py-12">
        <div class="vercel-card p-8">
          <h2 class="text-2xl font-bold mb-2">Login</h2>
          <p class="text-[#888] text-sm mb-6">Enter your credentials to access the dashboard.</p>
          <form class="grid gap-4" @submit.prevent="submitLogin">
            <div class="grid gap-1.5">
              <label class="text-xs font-medium text-[#888] uppercase tracking-wider">Username</label>
              <input v-model="loginForm.username" type="text" class="vercel-input" autocomplete="username" required />
            </div>
            <div class="grid gap-1.5">
              <label class="text-xs font-medium text-[#888] uppercase tracking-wider">Password</label>
              <input
                v-model="loginForm.password"
                type="password"
                class="vercel-input"
                autocomplete="current-password"
                required
              />
            </div>
            <Button label="Continue" type="submit" class="mt-4 !bg-white !text-black !border-white w-full" :loading="isBusy" />
          </form>
        </div>
      </div>

      <!-- Real Route Content -->
      <router-view v-else-if="dashboard"></router-view>
    </main>

    <div
      v-if="isLogoutConfirmOpen && currentUser"
      class="fixed inset-0 z-[80] flex items-center justify-center bg-black/70 px-4"
      role="presentation"
      @click.self="cancelLogout"
    >
      <section
        class="w-full max-w-md rounded-md border border-[#333] bg-black p-6 shadow-2xl shadow-black/60"
        role="dialog"
        aria-modal="true"
        aria-labelledby="logout-confirm-title"
        aria-describedby="logout-confirm-description"
      >
        <div class="mb-5">
          <div class="mb-3 inline-flex rounded-full border border-[#333] px-2.5 py-1 text-xs font-medium text-[#888]">
            Session
          </div>
          <h2 id="logout-confirm-title" class="mb-2 text-xl font-semibold tracking-tight text-white">
            Log out of p2pstream?
          </h2>
          <p id="logout-confirm-description" class="text-sm leading-6 text-[#888]">
            Your current session will end and dashboard data will be cleared from this browser view.
          </p>
        </div>

        <div class="flex flex-col-reverse gap-3 sm:flex-row sm:justify-end">
          <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
            <SecondaryButton
              label="Stay logged in"
              class="!border-[#333] !bg-transparent !text-[#ededed] hover:!border-[#666]"
              :disabled="Boolean(busyDisabledReason)"
              @click="cancelLogout"
            />
          </DisabledHint>
          <DisabledHint :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
            <DangerButton
              label="Log out"
              class="!border-red-600 !bg-red-600 !text-white hover:!border-red-500 hover:!bg-red-500"
              :loading="isBusy"
              :disabled="Boolean(busyDisabledReason)"
              @click="confirmLogout"
            />
          </DisabledHint>
        </div>
      </section>
    </div>
  </div>
</template>

<style>
.no-scrollbar::-webkit-scrollbar {
  display: none;
}
.no-scrollbar {
  -ms-overflow-style: none;
  scrollbar-width: none;
}

/* Helper for router-link inactive state */
.router-link-active {
  color: white;
  border-bottom-color: white;
}
.router-link-exact-active {
  color: white;
  border-bottom-color: white;
}
nav a:not(.router-link-active) {
  color: #888;
  border-bottom-color: transparent;
}
nav a:not(.router-link-active):hover {
  color: #ededed;
}
</style>
