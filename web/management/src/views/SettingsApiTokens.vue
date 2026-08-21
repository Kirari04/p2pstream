<script setup lang="ts">
import { Eye as EyeIcon } from "@lucide/vue";
import { EyeOff as EyeSlashIcon } from "@lucide/vue";
import { Plus as PlusIcon } from "@lucide/vue";
import { RefreshCw as RefreshIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { NButton, NCheckbox, NDataTable, NDatePicker, NDrawer, NDrawerContent, NInput, NModal, NTag } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import type { InputInst } from "naive-ui";
import { computed, h, inject, onMounted, reactive, ref, watch } from "vue";
import {
  isBusyKey,
  selectedEnvironmentBlockedKey,
  selectedEnvironmentIdKey,
  selectedEnvironmentLabelKey,
} from "@/composables/managementContextKeys";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import EmptyState from "@/components/EmptyState.vue";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { BUSY_REASON } from "@/lib/disabledReasons";
import { diagnosticExcerpt, diagnosticInspectionText } from "@/lib/diagnosticText";
import { editorDrawerWidth, modalCardStyle, naiveTagType } from "@/lib/naiveUi";
import type { ManagementAccessToken } from "@/gen/proto/p2pstream/v1/management_pb";
import { messageFromError } from "@/lib/errors";

const managementClient = useManagementClient();
const isBusy = inject(isBusyKey, computed(() => false));
const selectedEnvironmentId = inject(selectedEnvironmentIdKey, computed(() => "0"));
const selectedEnvironmentLabel = inject(selectedEnvironmentLabelKey, computed(() => "Local"));
const selectedEnvironmentBlocked = inject(selectedEnvironmentBlockedKey, computed(() => ""));
const revokeTokenDialog = useConfirmDialog();
const discardIssuedTokenDialog = useConfirmDialog();

const tokens = ref<ManagementAccessToken[]>([]);
const isLoading = ref(false);
const refreshQueued = ref(false);
const isCreateTokenDrawerOpen = ref(false);
const tokenNameInput = ref<InputInst | null>(null);
const issuedToken = ref("");
const isIssuedTokenModalOpen = ref(false);
const isIssuedTokenVisible = ref(false);
const issuedTokenWasCopied = ref(false);
const tokenCopyLabel = ref("Copy Token");
const operationError = ref("");
const issuedTokenVisiblePrefix = computed(() => issuedToken.value.slice(0, Math.min(10, issuedToken.value.length)));
const issuedTokenBlurredRemainder = computed(() => issuedToken.value.slice(issuedTokenVisiblePrefix.value.length));

const tokenForm = reactive({
  name: "",
  expiresAtUnixMillis: null as number | null,
  enabled: true,
});

interface TokenRefreshSnapshot {
  environmentId: string;
  blockedReason: string;
}

const actionDisabledReason = computed(() => {
  if (selectedEnvironmentBlocked.value) return selectedEnvironmentBlocked.value;
  if (isBusy.value || isLoading.value) return BUSY_REASON;
  return "";
});
const tokenColumns = computed<DataTableColumns<ManagementAccessToken>>(() => [
  {
    title: "Name",
    key: "name",
    minWidth: 180,
    render: (token) => h("bdi", {
      class: "clip-text base-text",
      dir: "ltr",
      title: diagnosticInspectionText(token.name),
    }, diagnosticExcerpt(token.name, 64).text),
  },
  {
    title: "Expires",
    key: "expires",
    width: 180,
    render: (token) => h("span", { class: "mono-text copy-xs" }, formatDate(token.expiresAtUnixMillis)),
  },
  {
    title: "Last Used",
    key: "lastUsed",
    width: 180,
    render: (token) => h("span", { class: "mono-text copy-xs" }, formatDate(token.lastUsedAtUnixMillis)),
  },
  {
    title: "Status",
    key: "status",
    width: 140,
    render: (token) => h(
      NTag,
      { size: "small", bordered: false, type: naiveTagType(tokenStatusSeverity(token)) },
      { default: () => tokenStatusLabel(token) },
    ),
  },
  {
    title: "Actions",
    key: "actions",
    width: 96,
    align: "right",
    render: (token) => h(
      DisabledHint,
      { disabled: Boolean(actionDisabledReason.value), reason: actionDisabledReason.value },
      {
        default: () => h(
          NButton,
          {
            type: "error",
            size: "small",
            "aria-label": `Revoke ${diagnosticExcerpt(token.name, 48).text}`,
            disabled: Boolean(actionDisabledReason.value),
            onClick: () => void deleteToken(token),
          },
          { icon: () => h(TrashIcon, { class: "icon-sm" }) },
        ),
      },
    ),
  },
]);

onMounted(() => {
  void refreshTokens();
});

watch([selectedEnvironmentId, selectedEnvironmentBlocked], () => {
  revokeTokenDialog.handleCancel();
  discardIssuedTokenDialog.handleCancel();
  isCreateTokenDrawerOpen.value = false;
  clearIssuedToken();
  operationError.value = "";
  tokens.value = [];
  void refreshTokens();
});

async function refreshTokens() {
  if (isLoading.value) {
    refreshQueued.value = true;
    return;
  }
  do {
    refreshQueued.value = false;
    const snapshot = currentTokenRefreshSnapshot();
    isLoading.value = true;
    operationError.value = "";
    try {
      await loadTokens(snapshot);
    } catch (err) {
      if (isTokenRefreshSnapshotCurrent(snapshot)) {
        operationError.value = messageFromError(err);
      } else {
        refreshQueued.value = true;
      }
    } finally {
      isLoading.value = false;
    }
  } while (refreshQueued.value);
}

async function loadTokens(snapshot = currentTokenRefreshSnapshot()) {
  if (snapshot.blockedReason) {
    if (isTokenRefreshSnapshotCurrent(snapshot)) {
      tokens.value = [];
    }
    return;
  }
  const resp = await managementClient.listManagementAccessTokens({});
  if (!isTokenRefreshSnapshotCurrent(snapshot)) {
    refreshQueued.value = true;
    return;
  }
  tokens.value = resp.accessTokens;
}

function currentTokenRefreshSnapshot(): TokenRefreshSnapshot {
  return {
    environmentId: selectedEnvironmentId.value,
    blockedReason: selectedEnvironmentBlocked.value,
  };
}

function isTokenRefreshSnapshotCurrent(snapshot: TokenRefreshSnapshot): boolean {
  return snapshot.environmentId === selectedEnvironmentId.value
    && snapshot.blockedReason === selectedEnvironmentBlocked.value;
}

async function createToken() {
  await runTokenAction(async () => {
    const resp = await managementClient.createManagementAccessToken({
      name: tokenForm.name,
      enabled: tokenForm.enabled,
      expiresAtUnixMillis: tokenExpiryMillis(),
    });
    issuedToken.value = resp.token;
    isIssuedTokenVisible.value = false;
    issuedTokenWasCopied.value = false;
    tokenCopyLabel.value = "Copy Token";
    isCreateTokenDrawerOpen.value = false;
    isIssuedTokenModalOpen.value = true;
    tokenForm.name = "";
    tokenForm.expiresAtUnixMillis = null;
    tokenForm.enabled = true;
    await loadTokens();
  });
}

async function copyIssuedToken() {
  if (!issuedToken.value) return;
  try {
    await navigator.clipboard.writeText(issuedToken.value);
    issuedTokenWasCopied.value = true;
    tokenCopyLabel.value = "Copied";
  } catch (err) {
    operationError.value = messageFromError(err);
    tokenCopyLabel.value = "Copy Failed";
  }
}

function clearIssuedToken() {
  issuedToken.value = "";
  isIssuedTokenModalOpen.value = false;
  isIssuedTokenVisible.value = false;
  tokenCopyLabel.value = "Copy Token";
  issuedTokenWasCopied.value = false;
}

function openCreateTokenDrawer() {
  tokenForm.name = "";
  tokenForm.expiresAtUnixMillis = null;
  tokenForm.enabled = true;
  operationError.value = "";
  isCreateTokenDrawerOpen.value = true;
}

function focusCreateTokenName() {
  tokenNameInput.value?.focus();
}

async function requestClearIssuedToken() {
  if (issuedToken.value && !issuedTokenWasCopied.value) {
    const confirmed = await discardIssuedTokenDialog.confirm(
      "Close Without Copying?",
      "This token is shown only once. Closing now will permanently discard it.",
      "Discard Token",
    );
    if (!confirmed) return;
  }
  clearIssuedToken();
}

async function deleteToken(token: ManagementAccessToken) {
  const confirmed = await revokeTokenDialog.confirm(
    "Revoke API Token",
    `Revoke "${diagnosticInspectionText(token.name)}"? Integrations using it will immediately lose access.`,
    "Revoke",
  );
  if (!confirmed) return;
  await runTokenAction(async () => {
    await managementClient.deleteManagementAccessToken({ id: token.id });
    await loadTokens();
  });
}

async function runTokenAction(action: () => Promise<void>) {
  if (isLoading.value || selectedEnvironmentBlocked.value) return;
  isLoading.value = true;
  operationError.value = "";
  try {
    await action();
  } catch (err) {
    operationError.value = messageFromError(err);
  } finally {
    isLoading.value = false;
    if (refreshQueued.value) {
      void refreshTokens();
    }
  }
}

function tokenExpiryMillis(): bigint {
  if (tokenForm.expiresAtUnixMillis === null) return 0n;
  const millis = tokenForm.expiresAtUnixMillis;
  if (!Number.isFinite(millis)) {
    throw new Error("Expiry date is invalid.");
  }
  return BigInt(millis);
}

function formatDate(value: bigint | undefined): string {
  if (!value || value === 0n) return "Never";
  return new Date(Number(value)).toLocaleString();
}

function tokenStatusLabel(token: ManagementAccessToken): string {
  if (!token.enabled) return "Disabled";
  const expiry = Number(token.expiresAtUnixMillis || 0n);
  if (expiry > 0 && expiry <= Date.now()) return "Expired";
  if (expiry > 0 && expiry - Date.now() <= 7 * 24 * 60 * 60 * 1000) return "Expires soon";
  return "Enabled";
}

function tokenStatusSeverity(token: ManagementAccessToken): "success" | "warn" | "danger" {
  const label = tokenStatusLabel(token);
  if (label === "Expired") return "danger";
  if (label === "Disabled" || label === "Expires soon") return "warn";
  return "success";
}


function tokenRowKey(token: ManagementAccessToken): string {
  return token.id.toString();
}

function handleIssuedTokenModalUpdate(show: boolean) {
  if (!show) void requestClearIssuedToken();
}
</script>

<template>
  <div class="stack-xl">
    <div class="layout-row layout-column space-lg mq-md-row mq-md-align-end mq-md-spread">
      <div>
        <h4 class="margin-bottom-sm copy-lg weight-semibold base-text">API Tokens</h4>
        <p class="copy-sm muted-text">Admin API credentials for <bdi dir="ltr">{{ diagnosticInspectionText(selectedEnvironmentLabel) }}</bdi>.</p>
      </div>
      <div class="layout-row wrap-items space-sm">
        <DisabledHint :disabled="Boolean(actionDisabledReason)" :reason="actionDisabledReason">
          <NButton
            secondary
            size="small"
            aria-label="Refresh API tokens"
            :disabled="Boolean(actionDisabledReason)"
            :loading="isLoading"
            @click="refreshTokens"
          >
            <template #icon><RefreshIcon class="icon-sm" /></template>
            Refresh
          </NButton>
        </DisabledHint>
        <DisabledHint :disabled="Boolean(actionDisabledReason)" :reason="actionDisabledReason">
          <NButton type="primary" size="small" :disabled="Boolean(actionDisabledReason)" @click="openCreateTokenDrawer">
            <template #icon><PlusIcon class="icon-sm" /></template>
            Create Token
          </NButton>
        </DisabledHint>
      </div>
    </div>

    <div v-if="selectedEnvironmentBlocked" class="round-md framed warning-border panel-bg pad-lg copy-sm warning-text">
      <bdi dir="ltr">{{ diagnosticInspectionText(selectedEnvironmentBlocked) }}</bdi>
    </div>
    <div v-if="operationError" class="round-md framed error-border panel-bg pad-lg copy-sm error-text">
      <bdi dir="ltr">{{ diagnosticInspectionText(operationError) }}</bdi>
    </div>

    <section class="surface-card hide-overflow">
      <div class="divider-bottom frame-standard pad-x-xl pad-y-lg layout-row align-center spread-items space-md">
        <div>
          <h5 class="copy-sm weight-semibold base-text">Issued tokens</h5>
          <p class="margin-top-xs copy-xs muted-text">Review usage and revoke credentials that are no longer needed.</p>
        </div>
        <NTag size="small" :bordered="false">{{ tokens.length }} total</NTag>
      </div>
      <template v-if="tokens.length || isLoading">
        <NDataTable
          :columns="tokenColumns"
          :data="tokens"
          :row-key="tokenRowKey"
          :pagination="false"
          :bordered="false"
          :single-line="false"
          :scroll-x="760"
          :loading="isLoading"
          size="small"
        />
      </template>
      <EmptyState
        v-else
        title="No API tokens"
        description="Create a scoped management credential when an integration needs API access. The secret is shown only once."
        action-label="Create Token"
        @action="openCreateTokenDrawer"
      />
    </section>

    <NDrawer
      v-model:show="isCreateTokenDrawerOpen"
      placement="right"
      :width="editorDrawerWidth('32rem')"
      aria-label="Create API Token"
      class="editor-drawer"
      @after-enter="focusCreateTokenName"
    >
      <NDrawerContent title="Create API Token" closable>
        <form class="editor-drawer-form layout-grid space-lg" @submit.prevent="createToken">
          <p class="copy-sm muted-text">The secret is revealed once after creation. Store it before closing the confirmation.</p>
          <div class="layout-grid space-xs">
            <label for="create-api-token-name" class="copy-xs weight-medium muted-text">Name</label>
            <NInput
              ref="tokenNameInput"
              v-model:value="tokenForm.name"
              size="small"
              placeholder="Deployment automation"
              required
              :input-props="{ id: 'create-api-token-name', name: 'token-name' }"
              :disabled="Boolean(actionDisabledReason)"
            />
          </div>
          <label class="layout-grid space-xs copy-xs weight-medium muted-text">
            Expires
            <NDatePicker
              v-model:value="tokenForm.expiresAtUnixMillis"
              type="datetime"
              clearable
              size="small"
              :disabled="Boolean(actionDisabledReason)"
            />
            <span class="copy-xs muted-text">Optional. Tokens without an expiry remain valid until revoked.</span>
          </label>
          <NCheckbox v-model:checked="tokenForm.enabled" :disabled="Boolean(actionDisabledReason)">
            Enable immediately
          </NCheckbox>
          <div class="editor-drawer-actions margin-top-lg layout-row align-end-row space-md">
            <NButton secondary attr-type="button" @click="isCreateTokenDrawerOpen = false">Cancel</NButton>
            <DisabledHint :disabled="Boolean(actionDisabledReason)" :reason="actionDisabledReason">
              <NButton type="primary" attr-type="submit" :disabled="Boolean(actionDisabledReason)" :loading="isLoading">
                Create Token
              </NButton>
            </DisabledHint>
          </div>
        </form>
      </NDrawerContent>
    </NDrawer>

    <NModal
      :show="isIssuedTokenModalOpen"
      preset="card"
      title="API Token Created"
      :style="modalCardStyle('38rem')"
      :bordered="false"
      :mask-closable="false"
      :close-on-esc="false"
      @update:show="handleIssuedTokenModalUpdate"
    >
      <div class="stack-md">
        <div class="round-md framed frame-standard muted-bg pad-lg">
          <div class="margin-bottom-sm">
            <p class="copy-xs weight-medium label-case letter-wide muted-text">One-Time Token</p>
          </div>
          <code
            class="flow-box min-code-height wrap-anywhere round-md framed frame-standard panel-bg pad-md mono-text copy-xs line-relaxed base-text"
          >
            <template v-if="isIssuedTokenVisible">
              {{ issuedToken }}
            </template>
            <template v-else>
              <span>{{ issuedTokenVisiblePrefix }}</span><span class="inline-block no-select muted-text secret-blur" aria-hidden="true">{{ issuedTokenBlurredRemainder }}</span>
            </template>
          </code>
        </div>

        <div class="layout-row layout-column-reverse space-md mq-sm-row mq-sm-end">
          <NButton secondary attr-type="button" @click="requestClearIssuedToken">Done</NButton>
          <NButton
            secondary
            attr-type="button"
            :aria-label="isIssuedTokenVisible ? 'Hide API token' : 'Reveal API token'"
            @click="isIssuedTokenVisible = !isIssuedTokenVisible"
          >
            <template #icon>
              <EyeSlashIcon v-if="isIssuedTokenVisible" class="icon-sm icon-sm" />
              <EyeIcon v-else class="icon-sm icon-sm" />
            </template>
            {{ isIssuedTokenVisible ? 'Hide' : 'Reveal' }}
          </NButton>
          <NButton type="primary" attr-type="button" @click="copyIssuedToken">{{ tokenCopyLabel }}</NButton>
        </div>
      </div>
    </NModal>
  </div>
</template>
