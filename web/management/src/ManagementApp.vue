<script setup lang="ts">
import { LogOut as LogoutIcon, Menu as MenuIcon, RefreshCw as RefreshIcon } from "@lucide/vue";
import { NAlert, NButton, NCard, NForm, NFormItem, NInput, NModal, NSkeleton, useMessage, useNotification } from "naive-ui";
import { computed, nextTick, onBeforeUnmount, onMounted, provide, ref, watch } from "vue";
import { useRoute } from "vue-router";
import DisabledHint from "@/components/DisabledHint.vue";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
import ManagementSidebar from "@/components/ui/ManagementSidebar.vue";
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
import { managementBreadcrumbsForPath, managementRouteLabel } from "@/lib/managementNavigation";

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

const sidebarStorageKey = "p2pstream:management-sidebar-collapsed";
const sidebarCollapsed = ref(loadSidebarPreference());
const mobileNavigationOpen = ref(false);
const mobileMenuButton = ref<HTMLButtonElement | null>(null);
const mainContent = ref<HTMLElement | null>(null);
const showNavigation = computed(() => Boolean(currentUser.value) && !isLoading.value && !setupState.value?.setupRequired);
const pageLabel = computed(() => managementRouteLabel(route.path));
const breadcrumbs = computed(() => managementBreadcrumbsForPath(route.path));
let desktopNavigationQuery: MediaQueryList | undefined;

function closeNavigationAtDesktop(event: MediaQueryListEvent | MediaQueryList) {
  if (event.matches) mobileNavigationOpen.value = false;
}

const sourceOfferHref = "/.well-known/p2pstream/source";
const sourceOfferTitle = computed(() => {
  const repository = import.meta.env.VITE_RELEASE_REPOSITORY?.trim();
  const ref = import.meta.env.VITE_RELEASE_REF?.trim();
  if (repository && ref) return `View source for ${repository}@${ref}`;
  return "View source and license";
});
const releaseChannel = computed(() => {
  const configured = import.meta.env.VITE_RELEASE_CHANNEL?.trim().toLowerCase();
  return configured || dashboard.value?.status?.releaseChannel?.trim().toLowerCase() || "development";
});
const releaseReference = computed(() =>
  import.meta.env.VITE_RELEASE_REF?.trim() || dashboard.value?.status?.version?.trim() || "unversioned",
);
const showStagingIdentity = computed(() => releaseChannel.value === "staging");

const refreshDisabledReason = computed(() => {
  if (isRefreshing.value) return "Dashboard refresh is already running.";
  if (isBusy.value) return BUSY_REASON;
  return "";
});
const busyDisabledReason = computed(() => isBusy.value ? BUSY_REASON : "");
const canShowRouteContent = computed(() =>
  Boolean(dashboard.value) || route.path.startsWith("/settings") || route.name === "not-found",
);

function loadSidebarPreference(): boolean {
  try {
    return window.localStorage.getItem(sidebarStorageKey) === "true";
  } catch {
    return false;
  }
}

function toggleSidebar() {
  sidebarCollapsed.value = !sidebarCollapsed.value;
}

async function openMobileNavigation() {
  mobileNavigationOpen.value = true;
  await nextTick();
  document.querySelector<HTMLElement>("#management-navigation a")?.focus();
}

async function closeMobileNavigation() {
  const wasOpen = mobileNavigationOpen.value;
  mobileNavigationOpen.value = false;
  if (wasOpen) {
    await nextTick();
    mobileMenuButton.value?.focus();
  }
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
    try {
      await action();
    } catch (actionError) {
      // Refresh even after a failed mutation because refresh endpoints persist
      // their attempt/error status before returning the failure.
      await loadDashboard();
      throw actionError;
    }
    // The mutation has already succeeded. A failed authoritative read-back
    // must not be reported as a failed mutation because retrying could create
    // duplicate resources or repeat an expensive operation.
    try {
      await loadDashboard({ propagateError: true });
    } catch (refreshError) {
      const refreshMessage = messageFromError(refreshError);
      error.value = refreshMessage;
      notification.warning({
        title: "Change saved; refresh failed",
        content: "The change was accepted, but the latest configuration could not be loaded. Refresh before retrying.",
        duration: 7000,
      });
      return true;
    }
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
  desktopNavigationQuery = window.matchMedia("(min-width: 768px)");
  closeNavigationAtDesktop(desktopNavigationQuery);
  desktopNavigationQuery.addEventListener("change", closeNavigationAtDesktop);
  void bootstrap();
});

onBeforeUnmount(() => {
  desktopNavigationQuery?.removeEventListener("change", closeNavigationAtDesktop);
});

watch(sidebarCollapsed, (collapsed) => {
  try {
    window.localStorage.setItem(sidebarStorageKey, collapsed ? "true" : "false");
  } catch {
    // Storage is optional; keep the in-memory navigation preference.
  }
});

watch(() => route.fullPath, async () => {
  mobileNavigationOpen.value = false;
  await nextTick();
  mainContent.value?.focus({ preventScroll: true });
});

</script>

<template>
  <div
    class="app-shell"
    :class="{ 'app-shell--authenticated': showNavigation, 'app-shell--sidebar-collapsed': sidebarCollapsed }"
    @keydown.esc.capture="closeMobileNavigation"
  >
    <a href="#management-main" class="skip-link" :tabindex="mobileNavigationOpen ? -1 : undefined">Skip to main content</a>
    <ManagementSidebar
      v-if="showNavigation"
      :collapsed="sidebarCollapsed"
      :mobile-open="mobileNavigationOpen"
      :username="currentUser?.username || 'Administrator'"
      @close-mobile="closeMobileNavigation"
      @toggle-collapsed="toggleSidebar"
    >
      <template #environment>
        <label class="app-sidebar__environment-label">
          <span>Environment</span>
          <AccessibleSelect
            v-model:value="selectedEnvironmentId"
            accessible-label="Environment"
            data-testid="environment-select-mobile"
            size="small"
            :options="environmentSelectOptions"
            :title="`Selected environment: ${selectedEnvironmentLabel}`"
          />
        </label>
        <a
          :href="sourceOfferHref"
          :title="sourceOfferTitle"
          class="app-sidebar__source-link"
          target="_blank"
          rel="noreferrer"
        >
          View source and license
        </a>
      </template>
    </ManagementSidebar>

    <div
      v-if="showNavigation && mobileNavigationOpen"
      class="app-sidebar-backdrop"
      aria-hidden="true"
      @click="closeMobileNavigation"
    />

    <div class="app-workspace" :inert="mobileNavigationOpen || undefined">
      <header class="app-header">
        <div class="app-header__bar">
          <div v-if="showNavigation" class="app-header__context">
            <button
              ref="mobileMenuButton"
              type="button"
              class="app-header__menu"
              aria-label="Open navigation"
              aria-controls="management-navigation"
              :aria-expanded="mobileNavigationOpen"
              @click="openMobileNavigation"
            >
              <MenuIcon aria-hidden="true" />
            </button>
            <nav class="app-breadcrumbs" aria-label="Breadcrumb">
              <ol>
                <li v-for="(crumb, index) in breadcrumbs" :key="crumb.key">
                  <router-link v-if="crumb.path && index < breadcrumbs.length - 1" :to="crumb.path">
                    {{ crumb.label }}
                  </router-link>
                  <span v-else :aria-current="index === breadcrumbs.length - 1 ? 'page' : undefined">
                    {{ crumb.label }}
                  </span>
                </li>
              </ol>
            </nav>
            <strong class="app-header__mobile-title">{{ pageLabel }}</strong>
          </div>

          <router-link v-else to="/overview" class="app-brand__group" aria-label="p2pstream home">
            <span class="app-brand__mark" aria-hidden="true"><span class="app-brand__diamond" /></span>
            <span class="app-brand__name">p2pstream</span>
          </router-link>

          <div class="app-header__actions">
            <span
              v-if="showStagingIdentity"
              class="app-release-channel"
              data-testid="release-channel"
              :title="`Staging prerelease ${releaseReference}`"
            >
              <span class="app-release-channel__signal" aria-hidden="true" />
              Staging
              <code>{{ releaseReference }}</code>
            </span>
            <label v-if="currentUser" class="app-env-label">
              <span>Environment</span>
              <AccessibleSelect
                v-model:value="selectedEnvironmentId"
                accessible-label="Environment"
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
                quaternary
                size="small"
                :loading="isRefreshing"
                :disabled="Boolean(refreshDisabledReason)"
                aria-label="Refresh management data"
                title="Refresh management data"
                @click="() => loadDashboard()"
              >
                <template #icon><RefreshIcon class="icon-sm" /></template>
              </NButton>
            </DisabledHint>
            <DisabledHint v-if="currentUser" :disabled="Boolean(busyDisabledReason)" :reason="busyDisabledReason">
              <NButton
                quaternary
                size="small"
                :disabled="Boolean(busyDisabledReason)"
                aria-label="Log out"
                title="Log out"
                @click="requestLogout"
              >
                <template #icon><LogoutIcon class="icon-sm" /></template>
                <span class="app-header__logout-label">Log out</span>
              </NButton>
            </DisabledHint>
          </div>
        </div>
      </header>

      <main id="management-main" ref="mainContent" class="app-main" tabindex="-1">
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
          <h1 class="margin-bottom-sm copy-2xl weight-semibold letter-normal">Setup Admin</h1>
          <p class="margin-bottom-xl copy-sm line-relaxed muted-text">Create the administrator account that will manage this p2pstream instance.</p>
          <NForm :model="setupForm" class="layout-grid space-lg" @submit.prevent="submitSetup">
              <NFormItem label="Username" path="username">
                <NInput v-model:value="setupForm.username" autocomplete="username" placeholder="Choose an administrator name" />
              </NFormItem>
              <NFormItem label="Password" path="password">
                <NInput v-model:value="setupForm.password" type="password" autocomplete="new-password" minlength="12" placeholder="At least 12 characters" />
              </NFormItem>
            <NButton type="primary" attr-type="submit" class="margin-top-lg fill-width" :loading="isBusy">
              Create administrator
            </NButton>
          </NForm>
        </NCard>
      </div>

      <div v-else-if="setupState?.setupRequired" class="max-panel-width centered-block pad-y-4xl">
        <NCard :bordered="false" class="surface-shadow">
          <div class="status-lock-pill margin-bottom-md">
            Setup locked
          </div>
          <h1 class="margin-bottom-sm copy-2xl weight-semibold letter-normal">Restart required</h1>
          <p class="wrap-anywhere copy-sm line-relaxed muted-text">
            {{ setupState.setupUnavailableReason || "Setup window expired; restart the server to retry setup." }}
          </p>
        </NCard>
      </div>

      <div v-else-if="!currentUser && !isLoading" class="max-auth-width centered-block pad-y-4xl">
        <NCard :bordered="false" class="surface-shadow">
          <h1 class="margin-bottom-sm copy-2xl weight-semibold letter-normal">Log in</h1>
          <p class="margin-bottom-xl copy-sm line-relaxed muted-text">Use your administrator credentials to open the management panel.</p>
          <NForm :model="loginForm" class="layout-grid space-lg" @submit.prevent="submitLogin">
              <NFormItem label="Username" path="username">
                <NInput v-model:value="loginForm.username" autocomplete="username" placeholder="Administrator username" />
              </NFormItem>
              <NFormItem label="Password" path="password">
                <NInput v-model:value="loginForm.password" type="password" autocomplete="current-password" placeholder="Password" />
              </NFormItem>
            <NButton type="primary" attr-type="submit" class="margin-top-lg fill-width" :loading="isBusy">
              Log in
            </NButton>
          </NForm>
        </NCard>
      </div>

        <router-view v-else-if="canShowRouteContent"></router-view>
      </main>
    </div>

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
