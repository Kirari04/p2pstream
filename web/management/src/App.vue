<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { managementClient } from "./api/managementClient";
import {
  ProxyState,
  type GetSetupStateResponse,
  type GetStatusResponse,
  type User,
} from "./gen/proto/p2pstream/v1/management_pb";

const setupState = ref<GetSetupStateResponse | null>(null);
const currentUser = ref<User | null>(null);
const status = ref<GetStatusResponse | null>(null);
const isLoading = ref(true);
const isBusy = ref(false);
const error = ref<string | null>(null);

const setupForm = ref({ username: "admin", password: "" });
const loginForm = ref({ username: "admin", password: "" });

const setupExpiresAt = computed(() => {
  const millis = setupState.value?.setupExpiresAtUnixMillis ?? 0n;
  return millis === 0n ? "" : new Date(Number(millis)).toLocaleString();
});

const latestStatsTime = computed(() => {
  const millis = status.value?.latestAgentStats?.reportedAtUnixMillis ?? 0n;
  return millis === 0n ? "No stats reported" : new Date(Number(millis)).toLocaleString();
});

const proxyState = computed(() => status.value?.proxy?.state ?? ProxyState.UNSPECIFIED);
const proxyIsRunning = computed(() => proxyState.value === ProxyState.RUNNING || status.value?.proxyRunning === true);
const proxyStateClass = computed(() => {
  if (proxyState.value === ProxyState.RUNNING) return "ok";
  if (proxyState.value === ProxyState.STARTING || proxyState.value === ProxyState.STOPPING) return "warn";
  return "down";
});

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

function bigIntLabel(value: bigint | undefined, fallback = "0"): string {
  if (value === undefined) return fallback;
  return new Intl.NumberFormat().format(Number(value));
}

function messageFromError(err: unknown): string {
  return err instanceof Error ? err.message : "Request failed";
}

async function bootstrap() {
  isLoading.value = true;
  error.value = null;

  try {
    setupState.value = await managementClient.getSetupState({});
    if (setupState.value.setupRequired) {
      currentUser.value = null;
      status.value = null;
      return;
    }

    try {
      const userResp = await managementClient.getCurrentUser({});
      currentUser.value = userResp.user ?? null;
    } catch {
      currentUser.value = null;
      status.value = null;
      return;
    }

    await loadStatus();
  } catch (err) {
    error.value = messageFromError(err);
  } finally {
    isLoading.value = false;
  }
}

async function loadStatus() {
  error.value = null;
  status.value = await managementClient.getStatus({});
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
    await loadStatus();
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
    await loadStatus();
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
    currentUser.value = null;
    status.value = null;
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
    await loadStatus();
  } catch (err) {
    error.value = messageFromError(err);
  } finally {
    isBusy.value = false;
  }
}

onMounted(() => {
  void bootstrap();
});
</script>

<template>
  <main class="shell">
    <header class="topbar">
      <div>
        <p class="eyebrow">management</p>
        <h1>p2pstream</h1>
      </div>
      <div class="topbar-actions">
        <span v-if="currentUser" class="session-user">{{ currentUser.username }}</span>
        <button v-if="currentUser" class="secondary" type="button" :disabled="isBusy" @click="logout">
          Log out
        </button>
        <button v-if="currentUser" class="secondary" type="button" :disabled="isBusy" @click="loadStatus">
          Refresh
        </button>
      </div>
    </header>

    <section v-if="error" class="notice error">
      {{ error }}
    </section>

    <section v-if="isLoading" class="notice">
      Loading
    </section>

    <section v-else-if="setupState?.setupRequired && setupState.setupAvailable" class="auth-panel">
      <div class="panel-heading">
        <p class="eyebrow">first run</p>
        <h2>Setup admin</h2>
        <span v-if="setupExpiresAt">Available until {{ setupExpiresAt }}</span>
      </div>
      <form class="auth-form" @submit.prevent="submitSetup">
        <label>
          Username
          <input v-model.trim="setupForm.username" autocomplete="username" required />
        </label>
        <label>
          Password
          <input
            v-model="setupForm.password"
            autocomplete="new-password"
            minlength="12"
            required
            type="password"
          />
        </label>
        <button class="primary" type="submit" :disabled="isBusy">Create admin</button>
      </form>
    </section>

    <section v-else-if="setupState?.setupRequired" class="notice error">
      <strong>Setup locked</strong>
      <span>{{ setupState.setupUnavailableReason }}</span>
    </section>

    <section v-else-if="!currentUser" class="auth-panel">
      <div class="panel-heading">
        <p class="eyebrow">session</p>
        <h2>Log in</h2>
      </div>
      <form class="auth-form" @submit.prevent="submitLogin">
        <label>
          Username
          <input v-model.trim="loginForm.username" autocomplete="username" required />
        </label>
        <label>
          Password
          <input v-model="loginForm.password" autocomplete="current-password" required type="password" />
        </label>
        <button class="primary" type="submit" :disabled="isBusy">Log in</button>
      </form>
    </section>

    <section v-else-if="status" class="dashboard">
      <div class="controls">
        <button class="primary" type="button" :disabled="isBusy || proxyIsRunning" @click="setProxyRunning(true)">
          Start proxy
        </button>
        <button class="danger" type="button" :disabled="isBusy || !proxyIsRunning" @click="setProxyRunning(false)">
          Stop proxy
        </button>
      </div>

      <section class="status-grid">
        <article class="metric">
          <span>Proxy</span>
          <strong :class="proxyStateClass">{{ proxyStateLabel(proxyState) }}</strong>
        </article>

        <article class="metric">
          <span>Agent</span>
          <strong :class="status.agentConnected ? 'ok' : 'down'">
            {{ status.agentConnected ? "Connected" : "Disconnected" }}
          </strong>
        </article>

        <article class="metric wide">
          <span>Target origin</span>
          <strong>{{ status.targetOrigin || "Not configured" }}</strong>
        </article>

        <article class="metric wide">
          <span>Latest stats</span>
          <strong>{{ latestStatsTime }}</strong>
        </article>

        <article class="metric">
          <span>Active requests</span>
          <strong>{{ status.latestAgentStats?.activeRequests ?? 0 }}</strong>
        </article>

        <article class="metric">
          <span>Memory</span>
          <strong>{{ bigIntLabel(status.latestAgentStats?.memorySysMb) }} MB</strong>
        </article>

        <article class="metric">
          <span>Success</span>
          <strong>{{ bigIntLabel(status.latestAgentStats?.reqSuccess) }}</strong>
        </article>

        <article class="metric">
          <span>Errors</span>
          <strong>{{ bigIntLabel(status.latestAgentStats?.reqInternalError) }}</strong>
        </article>
      </section>

      <section v-if="status.proxyLastError || status.proxy?.lastError" class="notice error">
        {{ status.proxy?.lastError || status.proxyLastError }}
      </section>
    </section>
  </main>
</template>
