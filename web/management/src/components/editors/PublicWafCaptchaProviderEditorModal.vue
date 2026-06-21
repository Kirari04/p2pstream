<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import { NButton, NCheckbox, NInput, NModal, NSelect } from "naive-ui";
import { isBusyKey, runManagementActionKey } from "@/composables/managementContextKeys";
import { useManagementClient } from "@/composables/useManagementClient";
import DisabledHint from "@/components/DisabledHint.vue";
import { BUSY_REASON } from "@/lib/disabledReasons";
import { modalCardStyle } from "@/lib/naiveUi";
import {
  PublicWafCaptchaProviderType,
  type GetPublicProxyConfigResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

const managementClient = useManagementClient();


const props = defineProps<{
  config: GetPublicProxyConfigResponse | null;
}>();

const emit = defineEmits<{
  (event: "saved"): void;
}>();

const runManagementAction = inject(runManagementActionKey);
const isBusy = inject(isBusyKey, computed(() => false));

const isOpen = ref(false);
const providers = computed(() => props.config?.wafCaptchaProviders ?? []);
const form = reactive({
  id: "",
  name: "",
  providerType: PublicWafCaptchaProviderType.TURNSTILE,
  siteKey: "",
  secretKey: "",
  secretKeySaved: false,
  enabled: true,
});

const providerOptions = [
  { label: "Cloudflare Turnstile", value: PublicWafCaptchaProviderType.TURNSTILE },
  { label: "hCaptcha", value: PublicWafCaptchaProviderType.HCAPTCHA },
  { label: "reCAPTCHA v2", value: PublicWafCaptchaProviderType.RECAPTCHA_V2 },
];

const submitDisabledReason = computed(() => {
  if (isBusy?.value) return BUSY_REASON;
  if (!form.name.trim()) return "Enter a provider name.";
  if (!form.siteKey.trim()) return "Enter the site key.";
  if (!form.id && !form.secretKey.trim()) return "Enter the secret key.";
  return "";
});
const submitDisabled = computed(() => Boolean(submitDisabledReason.value));

function nextProviderName(): string {
  const existing = new Set(providers.value.map((provider) => provider.name));
  if (!existing.has("captcha")) return "captcha";
  let index = 2;
  while (existing.has(`captcha-${index}`)) index += 1;
  return `captcha-${index}`;
}

function resetForm() {
  form.id = "";
  form.name = nextProviderName();
  form.providerType = PublicWafCaptchaProviderType.TURNSTILE;
  form.siteKey = "";
  form.secretKey = "";
  form.secretKeySaved = false;
  form.enabled = true;
}

function openCreate() {
  resetForm();
  isOpen.value = true;
}

function openEdit(providerId: bigint | string) {
  const id = providerId.toString();
  const provider = providers.value.find((item) => item.id.toString() === id);
  if (!provider) return;
  form.id = provider.id.toString();
  form.name = provider.name;
  form.providerType = provider.providerType || PublicWafCaptchaProviderType.TURNSTILE;
  form.siteKey = provider.siteKey;
  form.secretKey = "";
  form.secretKeySaved = provider.secretKeySet;
  form.enabled = provider.enabled;
  isOpen.value = true;
}

function close() {
  isOpen.value = false;
}

async function run(action: () => Promise<void>): Promise<boolean> {
  if (!runManagementAction) return false;
  return runManagementAction(action);
}

async function submitProvider() {
  const ok = await run(async () => {
    const payload = {
      name: form.name.trim(),
      providerType: form.providerType,
      siteKey: form.siteKey.trim(),
      secretKey: form.secretKey,
      enabled: form.enabled,
    };
    if (form.id) {
      await managementClient.updatePublicWafCaptchaProvider({
        id: BigInt(form.id),
        ...payload,
        secretKeySet: Boolean(form.secretKey),
      });
    } else {
      await managementClient.createPublicWafCaptchaProvider(payload);
    }
  });
  if (ok) {
    isOpen.value = false;
    emit("saved");
  }
}

defineExpose({ openCreate, openEdit, close });
</script>

<template>
  <NModal
    v-model:show="isOpen"
    preset="card"
    :title="form.id ? 'Edit Captcha Provider' : 'Add Captcha Provider'"
    :style="modalCardStyle('42rem')"
    :bordered="false"
    size="huge"
  >
    <form class="layout-grid max-modal-height space-xl scroll-y pad-right-xs" @submit.prevent="submitProvider">
      <section class="layout-grid space-lg mq-sm-cols-two">
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Name
          <NInput v-model:value="form.name" size="small" required />
        </label>
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Provider
          <NSelect v-model:value="form.providerType" size="small" :options="providerOptions" />
        </label>
      </section>

      <section class="layout-grid space-lg">
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Site key
          <NInput v-model:value="form.siteKey" size="small" autocomplete="off" required />
        </label>
        <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
          Secret key
          <NInput
            v-model:value="form.secretKey"
            size="small"
            autocomplete="off"
            type="password"
            :placeholder="form.secretKeySaved ? 'Saved - leave blank to keep current secret' : ''"
          />
        </label>
        <NCheckbox v-model:checked="form.enabled">
          Enabled
        </NCheckbox>
      </section>

      <div class="layout-row align-end-row space-md">
        <NButton secondary @click="close">Cancel</NButton>
        <DisabledHint :disabled="Boolean(submitDisabledReason)" :reason="submitDisabledReason">
          <NButton type="primary" attr-type="submit" :disabled="submitDisabled">
            {{ form.id ? 'Save Changes' : 'Create Provider' }}
          </NButton>
        </DisabledHint>
      </div>
    </form>
  </NModal>
</template>
