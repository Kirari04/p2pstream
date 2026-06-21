<script setup lang="ts">
import { Eye as EyeIcon } from "@lucide/vue";
import { EyeOff as EyeSlashIcon } from "@lucide/vue";
import { RefreshCw as RefreshIcon } from "@lucide/vue";
import { Trash2 as TrashIcon } from "@lucide/vue";
import { NButton, NCheckbox, NDataTable, NDatePicker, NInput, NModal, NTag } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { computed, h, inject, onMounted, reactive, ref, watch } from "vue";
import {
  isBusyKey,
  selectedEnvironmentBlockedKey,
  selectedEnvironmentIdKey,
  selectedEnvironmentLabelKey,
} from "@/composables/managementContextKeys";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { BUSY_REASON } from "@/lib/disabledReasons";
import { modalCardStyle, naiveTagType } from "@/lib/naiveUi";
import type { ManagementAccessToken } from "@/gen/proto/p2pstream/v1/management_pb";
import { messageFromError } from "@/lib/errors";

const managementClient = useManagementClient();
const isBusy = inject(isBusyKey, computed(() => false));
const selectedEnvironmentId = inject(selectedEnvironmentIdKey, computed(() => "0"));
const selectedEnvironmentLabel = inject(selectedEnvironmentLabelKey, computed(() => "Local"));
const selectedEnvironmentBlocked = inject(selectedEnvironmentBlockedKey, computed(() => ""));
const revokeTokenDialog = useConfirmDialog();

const tokens = ref<ManagementAccessToken[]>([]);
const isLoading = ref(false);
const refreshQueued = ref(false);
const issuedToken = ref("");
const isIssuedTokenModalOpen = ref(false);
const isIssuedTokenVisible = ref(false);
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
    ellipsis: { tooltip: true },
    render: (token) => token.name,
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
    width: 120,
    render: (token) => h(
      NTag,
      { size: "small", bordered: false, type: naiveTagType(token.enabled ? "success" : "warn") },
      { default: () => token.enabled ? "Enabled" : "Disabled" },
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
            "aria-label": "Revoke token",
            title: "Revoke token",
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
    tokenCopyLabel.value = "Copy Token";
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
}

async function deleteToken(token: ManagementAccessToken) {
  const confirmed = await revokeTokenDialog.confirm(
    "Revoke API Token",
    `Revoke "${token.name}"?`,
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


function tokenRowKey(token: ManagementAccessToken): string {
  return token.id.toString();
}

function handleIssuedTokenModalUpdate(show: boolean) {
  if (!show) clearIssuedToken();
}
</script>

<template>
  <div class="stack-xl">
    <div class="layout-row layout-column space-lg mq-md-row mq-md-align-end mq-md-spread">
      <div>
        <h4 class="margin-bottom-sm copy-lg weight-semibold base-text">API Tokens</h4>
        <p class="copy-sm muted-text">Admin API credentials for {{ selectedEnvironmentLabel }}.</p>
      </div>
      <DisabledHint :disabled="Boolean(actionDisabledReason)" :reason="actionDisabledReason">
        <NButton
          secondary
          size="small"
          aria-label="Refresh API tokens"
          title="Refresh API tokens"
          :disabled="Boolean(actionDisabledReason)"
          :loading="isLoading"
          @click="refreshTokens"
        >
          <template #icon><RefreshIcon class="icon-sm icon-sm" /></template>
        </NButton>
      </DisabledHint>
    </div>

    <div v-if="selectedEnvironmentBlocked" class="round-md framed warning-border panel-bg pad-lg copy-sm warning-text">
      {{ selectedEnvironmentBlocked }}
    </div>
    <div v-if="operationError" class="round-md framed error-border panel-bg pad-lg copy-sm error-text">
      {{ operationError }}
    </div>

    <section class="layout-grid space-2xl settings-token-grid">
      <div class="surface-card pad-xl">
        <h5 class="margin-bottom-lg copy-sm weight-semibold label-case letter-widest muted-text">Create API Token</h5>
        <form class="layout-grid space-lg" @submit.prevent="createToken">
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Name
            <NInput
              v-model:value="tokenForm.name"
              size="small"
              required
              :disabled="Boolean(actionDisabledReason)"
            />
          </label>
          <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
            Expires
            <NDatePicker
              v-model:value="tokenForm.expiresAtUnixMillis"
              type="datetime"
              clearable
              size="small"
              :disabled="Boolean(actionDisabledReason)"
            />
          </label>
          <NCheckbox v-model:checked="tokenForm.enabled" :disabled="Boolean(actionDisabledReason)">
            Enabled
          </NCheckbox>
          <DisabledHint :disabled="Boolean(actionDisabledReason)" :reason="actionDisabledReason">
            <NButton type="primary" attr-type="submit" :disabled="Boolean(actionDisabledReason)">
              Create Token
            </NButton>
          </DisabledHint>
        </form>
      </div>

      <div class="surface-card hide-overflow">
        <div class="divider-bottom frame-standard pad-x-xl pad-y-lg">
          <h5 class="copy-sm weight-semibold label-case letter-widest muted-text">API Tokens</h5>
        </div>
        <NDataTable
          :columns="tokenColumns"
          :data="tokens"
          :row-key="tokenRowKey"
          :pagination="false"
          :bordered="false"
          :single-line="false"
          :scroll-x="760"
          size="small"
        />
      </div>
    </section>

    <NModal
      :show="isIssuedTokenModalOpen"
      preset="card"
      title="API Token Created"
      :style="modalCardStyle('38rem')"
      :bordered="false"
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
          <NButton secondary attr-type="button" @click="clearIssuedToken">Done</NButton>
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
