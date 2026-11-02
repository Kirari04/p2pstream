<script setup lang="ts">
import { Copy as CopyIcon } from "@lucide/vue";
import { NButton, NTag } from "naive-ui";
import { computed, onBeforeUnmount, ref } from "vue";
import type { PublicTlsCertificate } from "@/gen/proto/p2pstream/v1/management_pb";
import { diagnosticInspectionText } from "@/lib/diagnosticText";
import { formatUnixMillis } from "@/lib/publicProxyLabels";
import {
  publicTlsLeafNames,
  publicTlsLeafUnavailableReason,
  shortFingerprint,
} from "@/lib/publicTlsPresentation";

const props = defineProps<{
  certificate: PublicTlsCertificate;
  /** Already sanitized hostname, used only in accessible names. */
  hostnameLabel: string;
}>();

const unavailableReason = computed(() => publicTlsLeafUnavailableReason(props.certificate));
const names = computed(() => publicTlsLeafNames(props.certificate));
const digest = computed(() => props.certificate.leafSha256);
const issued = computed(() => formatUnixMillis(props.certificate.issuedAtUnixMillis));
const expires = computed(() => formatUnixMillis(props.certificate.expiresAtUnixMillis));

const copyState = ref<"" | "copied" | "failed">("");
let copyResetTimer: number | undefined;

async function copyDigest() {
  if (!digest.value) return;
  try {
    await navigator.clipboard.writeText(digest.value);
    copyState.value = "copied";
  } catch {
    copyState.value = "failed";
  }
  window.clearTimeout(copyResetTimer);
  copyResetTimer = window.setTimeout(() => { copyState.value = ""; }, 1800);
}

onBeforeUnmount(() => window.clearTimeout(copyResetTimer));
</script>

<template>
  <section class="tls-facts" :aria-label="`Certificate details for ${hostnameLabel}`">
    <p v-if="unavailableReason" class="copy-sm">
      <span aria-hidden="true">—</span>
      <span class="muted-text"> {{ unavailableReason }}</span>
    </p>
    <dl v-else class="tls-facts__list">
      <div>
        <dt>Issuer</dt>
        <dd>
          <bdi v-if="certificate.leafIssuer" dir="ltr" class="wrap-anywhere">{{ diagnosticInspectionText(certificate.leafIssuer) }}</bdi>
          <span v-else><span aria-hidden="true">—</span> <span class="muted-text">Not provided</span></span>
        </dd>
      </div>
      <div>
        <dt>Names</dt>
        <dd class="tls-facts__names">
          <NTag
            v-for="name in names"
            :key="name"
            size="small"
            :bordered="false"
            class="tag-wrap max-full mono-text"
          >
            <bdi dir="ltr">{{ diagnosticInspectionText(name) }}</bdi>
          </NTag>
          <span v-if="!names.length"><span aria-hidden="true">—</span> <span class="muted-text">No SANs</span></span>
        </dd>
      </div>
      <div>
        <dt>SHA-256</dt>
        <dd class="tls-facts__digest">
          <template v-if="digest">
            <code class="mono-text" :title="digest">{{ shortFingerprint(digest) }}</code>
            <NButton
              text
              size="tiny"
              :aria-label="`Copy certificate fingerprint for ${hostnameLabel}`"
              title="Copy fingerprint"
              @click="copyDigest"
            >
              <template #icon><CopyIcon class="icon-sm" /></template>
            </NButton>
            <span v-if="copyState === 'copied'" class="copy-xs success-text" role="status">Copied</span>
            <span v-else-if="copyState === 'failed'" class="copy-xs warning-text" role="status">Copy failed; hover to read it</span>
          </template>
          <span v-else><span aria-hidden="true">—</span> <span class="muted-text">Not available</span></span>
        </dd>
      </div>
      <div>
        <dt>Serial</dt>
        <dd>
          <code v-if="certificate.leafSerialNumber" class="mono-text wrap-anywhere">{{ certificate.leafSerialNumber }}</code>
          <span v-else><span aria-hidden="true">—</span> <span class="muted-text">Not available</span></span>
        </dd>
      </div>
      <div>
        <dt>Validity</dt>
        <dd class="tls-facts__tabular">
          <template v-if="issued || expires">{{ issued || "Unknown" }} – {{ expires || "Unknown" }}</template>
          <span v-else><span aria-hidden="true">—</span> <span class="muted-text">Not recorded</span></span>
        </dd>
      </div>
    </dl>
  </section>
</template>

<style scoped>
.tls-facts {
  min-width: 0;
  max-width: 52rem;
  padding: 0.25rem 0;
}

.tls-facts__list {
  display: grid;
  gap: 0.5rem;
}

.tls-facts__list > div {
  display: grid;
  grid-template-columns: 4.5rem minmax(0, 1fr);
  gap: 0.75rem;
  font-size: 0.75rem;
  line-height: 1.45;
}

.tls-facts__list dt {
  color: var(--app-text-muted);
}

.tls-facts__list dd {
  min-width: 0;
  overflow-wrap: anywhere;
}

.tls-facts__names {
  display: flex;
  flex-wrap: wrap;
  gap: 0.25rem;
}

.tls-facts__digest {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.375rem;
}

.tls-facts__tabular {
  font-variant-numeric: tabular-nums;
}
</style>
