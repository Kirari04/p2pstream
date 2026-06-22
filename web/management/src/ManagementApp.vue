<script setup lang="ts">
import { RefreshCw as RefreshIcon } from "@lucide/vue";
import { NAlert, NButton, NCard, NForm, NFormItem, NInput, NModal, NSelect, NSkeleton, useMessage, useNotification } from "naive-ui";
import { computed, onMounted, provide } from "vue";
import { useRoute } from "vue-router";
import DisabledHint from "@/components/DisabledHint.vue";
import ThemeToggle from "@/components/ui/ThemeToggle.vue";
import {
  dashboardKey,
  environmentsKey,
  isBusyKey,
  logoutKey,
  managementClientKey,
  publicProxyConfigKey,
  reloadEnvironmentsKey,
  runManagementActionKey,
  selectedEnvironmentBlockedKey,
  selectedEnvironmentIdKey,
  selectedEnvironmentLabelKey,
  setProxyRunningKey,
} from "@/composables/managementContextKeys";
import { useDashboardRefresh } from "@/composables/useDashboardRefresh";
import { useManagementSession } from "@/composables/useManagementSession";
import { BUSY_REASON } from "@/lib/disabledReasons";
import { messageFromError } from "@/lib/errors";

const message = useMessage();
const notification = useNotification();
const route = useRoute();

const session = useManagementSession();
const {
  setupState,
  currentUser,
  setupForm,
  loginForm,
  isLoading,
  isBusy,
  isLogoutConfirmOpen,
  error,
  requestLogout,
  cancelLogout,
} = session;
const dashboardRefresh = useDashboardRefresh({ currentUser, error, isBusy, isLoading });
const {
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
  stopAutoRefresh,
} = dashboardRefresh;

type NavTab = {
  path: string;
  label: string;
  activePrefix?: string;
};

const tabs: NavTab[] = [
  { path: "/overview", label: "Overview" },
  { path: "/monitor/traffic", label: "Monitor", activePrefix: "/monitor" },
  { path: "/proxy/routes", label: "Proxy", activePrefix: "/proxy" },
  { path: "/agent", label: "Agents", activePrefix: "/agent" },
  { path: "/policies/rate-limits", label: "Traffic Policy", activePrefix: "/policies" },
  { path: "/templates", label: "Templates" },
  { path: "/tls", label: "TLS" },
  { path: "/settings", label: "Settings", activePrefix: "/settings" },
];

const sourceOfferHref = "/.well-known/p2pstream/source";
const sourceOfferTitle = computed(() => {
  const repository = import.meta.env.VITE_RELEASE_REPOSITORY?.trim();
  const ref = import.meta.env.VITE_RELEASE_REF?.trim();
  if (repository && ref) return `View source for ${repository}@${ref}`;
  return "View source and license";
});

const refreshDisabledReason = computed(() => {
  if (isRefreshing.value) return "Dashboard refresh is already running.";
  if (isBusy.value) return BUSY_REASON;
  return "";
});
const busyDisabledReason = computed(() => isBusy.value ? BUSY_REASON : "");
const canShowRouteContent = computed(() =>
  Boolean(dashboard.value) || route.path.startsWith("/settings") || route.name === "not-found",
);

function isNavTabActive(tab: NavTab): boolean {
  if (route.path === tab.path) return true;
  if (!tab.activePrefix) return false;
  return route.path === tab.activePrefix || route.path.startsWith(`${tab.activePrefix}/`);
}

// Provide state to views
provide(dashboardKey, computed(() => dashboard.value));
provide(publicProxyConfigKey, computed(() => publicProxyConfig.value));
provide(isBusyKey, computed(() => isBusy.value));
provide(managementClientKey, managementClient);
provide(environmentsKey, computed(() => environments.value));
provide(selectedEnvironmentIdKey, computed(() => selectedEnvironmentId.value));
provide(selectedEnvironmentLabelKey, selectedEnvironmentLabel);
provide(selectedEnvironmentBlockedKey, selectedEnvironmentBlocked);
provide(reloadEnvironmentsKey, loadEnvironments);

async function bootstrap() {
  isLoading.value = true;
  error.value = null;
  stopAutoRefresh();

  try {
    const sessionState = await session.bootstrapSession();
    if (sessionState !== "authenticated") {
      clearDashboardState();
      return;
    }

    await loadAuthenticatedData();
  } catch (err) {
    error.value = messageFromError(err);
  } finally {
    isLoading.value = false;
  }
}

async function submitSetup() {
  await session.submitSetup(loadAuthenticatedData);
}

async function submitLogin() {
  await session.submitLogin(loadAuthenticatedData);
}

async function confirmLogout() {
  await session.confirmLogout(clearAuthenticatedData);
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

provide(setProxyRunningKey, setProxyRunning);
provide(runManagementActionKey, runManagementAction);
provide(logoutKey, requestLogout);

onMounted(() => {
  session.initializeSetupToken();
  void bootstrap();
});

</script>

<template>
  <div class="app-shell">
    <header class="app-header">
      <div class="app-header__inner">
        <div class="app-header__bar">
          <div class="app-brand">
            <div class="app-brand__group">
              <div class="app-brand__mark">
                <div class="app-brand__diamond"></div>
              </div>
              <span class="app-brand__name">p2pstream</span>
            </div>
            <div class="app-brand__divider"></div>
            <div v-if="currentUser" class="app-user">
              <span>{{ currentUser.username }}</span>
            </div>
          </div>

          <div class="app-header__actions">
            <label v-if="currentUser" class="app-env-label">
              Environment
              <NSelect
                v-model:value="selectedEnvironmentId"
                data-testid="environment-select"
                size="small"
                class="app-env-select"
                :options="environmentSelectOptions"
                :title="`Selected environment: ${selectedEnvironmentLabel}`"
              />
            </label>
            <a
              :href="sourceOfferHref"
              :title="sourceOfferTitle"
              :aria-label="sourceOfferTitle"
              class="app-source-link"
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
                  <RefreshIcon class="icon-sm" />
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

      <div v-if="currentUser && !isLoading && !setupState?.setupRequired" class="app-header__nav-wrap">
        <nav class="app-nav no-scrollbar">
          <router-link
            v-for="tab in tabs"
            :key="tab.path"
            :to="tab.path"
            class="app-nav__link"
            :class="{ 'app-nav__link--active': isNavTabActive(tab) }"
          >
            {{ tab.label }}
          </router-link>
        </nav>
      </div>
    </header>

    <main class="app-main">
      <NAlert v-if="error" type="error" class="margin-bottom-xl" :bordered="false">
        {{ error }}
      </NAlert>
      <NAlert v-if="selectedEnvironmentBlocked" type="warning" class="margin-bottom-xl" :bordered="false">
        {{ selectedEnvironmentBlocked }}
      </NAlert>

      <div v-if="isLoading" class="layout-grid space-2xl mq-sm-cols-two mq-lg-cols-four">
        <NCard v-for="i in 4" :key="i" :bordered="true">
          <NSkeleton text width="40%" height="0.75rem" class="margin-bottom-md" />
          <NSkeleton text width="70%" height="1.5rem" />
        </NCard>
      </div>

      <div v-else-if="setupState?.setupRequired && setupState.setupAvailable" class="max-auth-width centered-block pad-y-4xl">
        <NCard :bordered="false" class="surface-shadow">
          <h2 class="margin-bottom-sm copy-2xl weight-semibold letter-normal">Setup Admin</h2>
          <p class="margin-bottom-xl copy-sm muted-text">Create the primary administrator account.</p>
          <form class="layout-grid space-lg" @submit.prevent="submitSetup">
            <NForm :model="setupForm">
              <NFormItem label="Username" path="username">
                <NInput v-model:value="setupForm.username" autocomplete="username" />
              </NFormItem>
              <NFormItem label="Password" path="password">
                <NInput v-model:value="setupForm.password" type="password" autocomplete="new-password" minlength="12" />
              </NFormItem>
            </NForm>
            <NButton type="primary" attr-type="submit" class="margin-top-lg fill-width" :loading="isBusy">
              Complete Setup
            </NButton>
          </form>
        </NCard>
      </div>

      <div v-else-if="setupState?.setupRequired" class="max-panel-width centered-block pad-y-4xl">
        <NCard :bordered="false" class="surface-shadow">
          <div class="status-lock-pill margin-bottom-md">
            Setup locked
          </div>
          <h2 class="margin-bottom-sm copy-2xl weight-semibold letter-normal">Restart required</h2>
          <p class="wrap-anywhere copy-sm line-relaxed muted-text">
            {{ setupState.setupUnavailableReason || "Setup window expired; restart the server to retry setup." }}
          </p>
        </NCard>
      </div>

      <div v-else-if="!currentUser && !isLoading" class="max-auth-width centered-block pad-y-4xl">
        <NCard :bordered="false" class="surface-shadow">
          <h2 class="margin-bottom-sm copy-2xl weight-semibold letter-normal">Login</h2>
          <p class="margin-bottom-xl copy-sm muted-text">Enter your credentials to access the dashboard.</p>
          <form class="layout-grid space-lg" @submit.prevent="submitLogin">
            <NForm :model="loginForm">
              <NFormItem label="Username" path="username">
                <NInput v-model:value="loginForm.username" autocomplete="username" />
              </NFormItem>
              <NFormItem label="Password" path="password">
                <NInput v-model:value="loginForm.password" type="password" autocomplete="current-password" />
              </NFormItem>
            </NForm>
            <NButton type="primary" attr-type="submit" class="margin-top-lg fill-width" :loading="isBusy">
              Continue
            </NButton>
          </form>
        </NCard>
      </div>

      <router-view v-else-if="canShowRouteContent"></router-view>
    </main>

    <NModal
      :show="isLogoutConfirmOpen && Boolean(currentUser)"
      :mask-closable="!isBusy"
      @update:show="(show) => { if (!show) cancelLogout(); }"
    >
      <NCard
        class="logout-card"
        :bordered="false"
        role="dialog"
        aria-modal="true"
        aria-labelledby="logout-confirm-title"
        aria-describedby="logout-confirm-description"
      >
        <div class="margin-bottom-xl">
          <div class="margin-bottom-md inline-row round-full framed frame-standard pad-x-smd pad-y-xs copy-xs weight-semibold muted-text">
            Session
          </div>
          <h2 id="logout-confirm-title" class="margin-bottom-sm copy-xl weight-semibold letter-normal base-text">
            Log out of p2pstream?
          </h2>
          <p id="logout-confirm-description" class="copy-sm line-relaxed muted-text">
            Your current session will end and dashboard data will be cleared from this browser view.
          </p>
        </div>

        <div class="layout-row layout-column-reverse space-md mq-sm-row mq-sm-end">
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
      </NCard>
    </NModal>
  </div>
</template>
