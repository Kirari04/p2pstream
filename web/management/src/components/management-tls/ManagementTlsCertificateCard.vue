<script setup lang="ts">
import { Copy as CopyIcon } from "@lucide/vue";
import { NButton, NTag } from "naive-ui";
import { computed, ref } from "vue";
import type { ManagementTlsCertificateSummary } from "@/gen/proto/p2pstream/v1/management_pb";
import { managementCertificateExpiryStatus } from "@/lib/managementTlsPresentation";

const props = defineProps<{
  label: string;
  certificate?: ManagementTlsCertificateSummary;
  emptyText?: string;
  staged?: boolean;
  showExpiry?: boolean;
}>();

const copyState = ref<"" | "copied" | "failed">("");
const names = computed(() => props.certificate ? [...props.certificate.dnsNames, ...props.certificate.ipAddresses] : []);
const expiry = computed(() => managementCertificateExpiryStatus(props.certificate?.notAfterUnixMillis ?? 0n));
const digest = computed(() => props.certificate?.sha256 ?? "");
const shortDigest = computed(() => digest.value ? `${digest.value.slice(0, 12)}…${digest.value.slice(-8)}` : "Unavailable");

function formatDate(value: bigint): string {
  return value === 0n ? "Unknown" : new Date(Number(value)).toLocaleString();
}

async function copyDigest() {
  if (!digest.value) return;
  try {
    await navigator.clipboard.writeText(digest.value);
    copyState.value = "copied";
  } catch {
    copyState.value = "failed";
  }
  window.setTimeout(() => { copyState.value = ""; }, 1800);
}
</script>

<template>
  <article class="cert-card" :class="{ 'cert-card--staged': staged }">
    <div class="layout-row align-center spread-items space-sm">
      <p class="copy-xs label-case letter-wide muted-text">{{ label }}</p>
      <NTag v-if="certificate && showExpiry" size="small" :bordered="false" :type="expiry.type">{{ expiry.label }}</NTag>
    </div>
    <template v-if="certificate">
      <p class="copy-sm weight-semibold wrap-anywhere">{{ certificate.subject || 'Unavailable' }}</p>
      <dl class="cert-facts">
        <div><dt>Expires</dt><dd>{{ formatDate(certificate.notAfterUnixMillis) }}</dd></div>
        <div><dt>Issuer</dt><dd>{{ certificate.issuer || 'Unavailable' }}</dd></div>
        <div>
          <dt>Names</dt>
          <dd class="cert-names">
            <NTag v-for="name in names" :key="name" size="small" :bordered="false" class="tag-wrap max-full mono-text">{{ name }}</NTag>
            <span v-if="!names.length" class="muted-text">No SANs</span>
          </dd>
        </div>
        <div>
          <dt>SHA-256</dt>
          <dd class="cert-digest">
            <code :title="digest">{{ shortDigest }}</code>
            <NButton v-if="digest" text size="tiny" :aria-label="`Copy ${label.toLowerCase()} certificate fingerprint`" @click="copyDigest">
              <template #icon><CopyIcon class="icon-sm" /></template>
            </NButton>
            <span v-if="copyState === 'copied'" class="copy-xs success-text">Copied</span>
            <span v-else-if="copyState === 'failed'" class="copy-xs warning-text">Copy failed; hover to read it</span>
          </dd>
        </div>
      </dl>
    </template>
    <p v-else class="copy-sm muted-text">{{ emptyText ?? 'Unavailable' }}</p>
  </article>
</template>

<style scoped>
.cert-card { padding: 1rem; border-radius: 8px; background: var(--app-panel-muted); display: grid; align-content: start; gap: 0.75rem; min-width: 0; }
.cert-card--staged { box-shadow: inset 0 0 0 1px var(--app-border); background: transparent; }
.cert-facts { display: grid; gap: 0.45rem; }
.cert-facts > div { display: grid; grid-template-columns: 4.5rem minmax(0, 1fr); gap: 0.75rem; font-size: 0.75rem; }
.cert-facts dt { color: var(--app-text-muted); }
.cert-facts dd { overflow-wrap: anywhere; }
.cert-names { display: flex; flex-wrap: wrap; gap: 0.25rem; }
.cert-digest { display: flex; align-items: center; gap: 0.375rem; min-width: 0; }
</style>
