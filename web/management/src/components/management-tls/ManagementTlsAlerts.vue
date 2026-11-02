<script setup lang="ts">
import { NAlert } from "naive-ui";
import { computed } from "vue";
import type { ManagementTlsRotation } from "@/gen/proto/p2pstream/v1/management_pb";
import { managementCertificateExpiryWarning } from "@/lib/managementTlsPresentation";

const props = defineProps<{
  rotation?: ManagementTlsRotation;
  remoteSelected: boolean;
  operationError: string;
  pollError: string;
  lastUpdatedAt: number;
}>();

const trustAttentionResolved = computed(() => (props.rotation?.trustAttentionAgentCount ?? 0) === 0);
const activeExpiryWarning = computed(() => managementCertificateExpiryWarning(props.rotation?.activeCertificate?.notAfterUnixMillis ?? 0n));
</script>

<template>
  <NAlert v-if="remoteSelected" type="info" :bordered="false">
    Showing this server's certificate. Management TLS changes are intentionally local-only, so actions stay disabled until you select the Local environment.
  </NAlert>
  <NAlert v-if="operationError" type="error" :bordered="false">{{ operationError }}</NAlert>
  <NAlert v-if="pollError" type="warning" :bordered="false">Live refresh failed: {{ pollError }}. Showing the last confirmed status from {{ lastUpdatedAt ? new Date(lastUpdatedAt).toLocaleTimeString() : 'startup' }}.</NAlert>
  <NAlert v-if="rotation?.statusMessage" type="warning" :bordered="false">{{ rotation.statusMessage }}</NAlert>
  <NAlert v-if="activeExpiryWarning" type="error" :bordered="false">{{ activeExpiryWarning }}</NAlert>
  <NAlert v-if="rotation?.secretCleanupPending" type="warning" :bordered="false">
    Retired certificate material could not yet be removed from disk. Cleanup is retried automatically; check server filesystem permissions.
  </NAlert>
  <NAlert v-if="rotation?.forcedActivation" :type="trustAttentionResolved ? 'info' : 'error'" :bordered="false">
    Certificate activation was forced. {{ trustAttentionResolved ? 'Every known agent has reconciled; old-trust retirement may now proceed.' : 'Repair every stranded agent before retiring old trust.' }}
  </NAlert>
  <NAlert v-if="rotation?.forcedRetirement" :type="trustAttentionResolved ? 'info' : 'warning'" :bordered="false">
    Old-CA retirement was forced. {{ trustAttentionResolved ? 'Every known agent has since reconciled to the active trust bundle.' : 'Agents marked Attention have not confirmed the active-only trust bundle yet.' }}
  </NAlert>
  <NAlert v-if="rotation?.forcedCleanup" :type="trustAttentionResolved ? 'info' : 'warning'" :bordered="false">
    Cleanup was forced. {{ trustAttentionResolved ? 'Every known agent has since confirmed cleanup.' : 'Agents marked Attention may still trust the abandoned CA; they will be reconciled when they report again.' }}
  </NAlert>
</template>
