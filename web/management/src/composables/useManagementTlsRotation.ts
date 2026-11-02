import { computed, inject, onBeforeUnmount, onMounted, reactive, ref } from "vue";
import { localManagementClient } from "@/api/managementClient";
import { selectedEnvironmentIdKey } from "@/composables/managementContextKeys";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import {
  ManagementTlsCleanupReason,
  ManagementTlsRotationPhase,
  type ManagementTlsRotation,
} from "@/gen/proto/p2pstream/v1/management_pb";
import { messageFromError } from "@/lib/errors";
import { summarizeManagementTlsFleet } from "@/lib/managementTlsPresentation";

/**
 * Shared management TLS rotation state for the overview and rotation pages.
 * Owns polling and every rotation action, including its confirmation copy, so
 * both pages drive the same server workflow.
 */
export function useManagementTlsRotation() {
  const selectedEnvironmentId = inject(selectedEnvironmentIdKey, computed(() => "0"));
  const confirmDialog = useConfirmDialog();
  const rotation = ref<ManagementTlsRotation>();
  const loading = ref(false);
  const operation = ref("");
  const operationError = ref("");
  const pollError = ref("");
  const lastUpdatedAt = ref(0);
  const custom = reactive({ certificatePem: "", privateKeyPem: "", caBundlePem: "", currentCaBundlePem: "" });
  let pollTimer: number | undefined;

  const phase = computed(() => rotation.value?.phase ?? ManagementTlsRotationPhase.UNSPECIFIED);
  const remoteSelected = computed(() => selectedEnvironmentId.value !== "0");
  const canOperate = computed(() => Boolean(rotation.value?.managedRotationAvailable) && !loading.value && !remoteSelected.value);
  const fleetSummary = computed(() => summarizeManagementTlsFleet(rotation.value?.agents ?? []));
  const blockingAgentCount = computed(() => rotation.value?.blockingAgentCount ?? 0);
  const cleanupTitle = computed(() => rotation.value?.cleanupReason === ManagementTlsCleanupReason.ROLLED_BACK_CERTIFICATE ? "Rollback trust cleanup" : "Cancelled rotation cleanup");

  onMounted(() => {
    void refresh();
    pollTimer = window.setInterval(() => void refresh(true), 5000);
  });

  onBeforeUnmount(() => {
    if (pollTimer !== undefined) window.clearInterval(pollTimer);
    confirmDialog.handleCancel();
  });

  async function refresh(quiet = false) {
    if (loading.value && quiet) return;
    if (!quiet) loading.value = true;
    try {
      const response = await localManagementClient.getManagementTlsRotation({});
      rotation.value = response.rotation;
      lastUpdatedAt.value = Date.now();
      pollError.value = "";
      if (!quiet) operationError.value = "";
    } catch (error) {
      if (quiet) pollError.value = messageFromError(error);
      else operationError.value = messageFromError(error);
    } finally {
      if (!quiet) loading.value = false;
    }
  }

  async function runAction(name: string, action: () => Promise<{ rotation?: ManagementTlsRotation }>) {
    operation.value = name;
    loading.value = true;
    operationError.value = "";
    try {
      const response = await action();
      if (response.rotation) rotation.value = response.rotation;
    } catch (error) {
      operationError.value = messageFromError(error);
    } finally {
      loading.value = false;
      operation.value = "";
    }
  }

  async function regenerate() {
    if (!await confirmDialog.confirm("Generate a new management CA?", "A new private CA and server certificate will be staged. The active certificate does not change until every enabled compatible agent acknowledges durable trust, or you explicitly force activation.", "Generate and stage")) return;
    await runAction("generate", () => localManagementClient.generateManagementTlsRotation({}));
  }

  /** Returns true when the custom material was staged and cleared. */
  async function stageCustom(): Promise<boolean> {
    await runAction("stage", () => localManagementClient.stageManagementTlsRotation(custom));
    if (operationError.value) return false;
    custom.certificatePem = "";
    custom.privateKeyPem = "";
    custom.caBundlePem = "";
    custom.currentCaBundlePem = "";
    return true;
  }

  async function activate(force: boolean) {
    const description = force
      ? `Force activation can strand ${blockingAgentCount.value} enabled agent(s). They will need the repair command or a full reinstall before reconnecting.`
      : "New TLS handshakes will begin using the staged certificate. The previous certificate and dual trust bundle remain available for rollback.";
    if (!await confirmDialog.confirm(force ? "Force certificate activation?" : "Activate staged certificate?", description, force ? "Force activation" : "Activate")) return;
    await runAction(force ? "force-activate" : "activate", () => localManagementClient.activateManagementTlsRotation({ force }));
  }

  async function cancelRotation() {
    if (!await confirmDialog.confirm("Cancel staged rotation?", "The active certificate remains unchanged. A cleanup rollout will remove the staged CA from every compatible agent that may have installed the dual bundle.", "Cancel and clean up")) return;
    await runAction("cancel", () => localManagementClient.cancelManagementTlsRotation({}));
  }

  async function rollback() {
    if (!await confirmDialog.confirm("Roll back the active certificate?", "The retained previous certificate becomes active immediately, then agents acknowledge removal of the abandoned CA. Rollback is unavailable after old trust is retired.", "Roll back and clean up")) return;
    await runAction("rollback", () => localManagementClient.rollbackManagementTlsRotation({}));
  }

  async function beginRetirement() {
    const leafOnly = (rotation.value?.rolloutGeneration ?? 0n) === 0n;
    if (!await confirmDialog.confirm(leafOnly ? "Finish leaf-certificate rotation?" : "Remove trust in the old CA?", leafOnly ? "The CA did not change, so no agent trust cleanup is required. This closes the rollback window." : "Agents will receive a new bundle containing only the active CA. The previous certificate remains available until every enabled agent acknowledges this cleanup.", leafOnly ? "Finish rotation" : "Begin retirement")) return;
    await runAction("retire", () => localManagementClient.beginManagementTlsTrustRetirement({}));
  }

  async function finalizeRetirement(force: boolean) {
    if (!await confirmDialog.confirm(force ? "Force trust retirement?" : "Finalize old trust retirement?", force ? "Agents that did not acknowledge will retain old trust. Review and repair them manually." : "The rollback certificate will be retired after all enabled agents have acknowledged the active-only bundle.", force ? "Force retirement" : "Finalize")) return;
    await runAction(force ? "force-finalize" : "finalize", () => localManagementClient.finalizeManagementTlsTrustRetirement({ force }));
  }

  async function finalizeCleanup(force: boolean) {
    const description = force
      ? "Compatible agents that have not acknowledged may continue trusting the abandoned CA. The server will keep offering the active-only repair bundle when they return."
      : "The cancelled or rolled-back certificate files are already retired. This closes cleanup after every participating agent has durably removed the abandoned CA.";
    if (!await confirmDialog.confirm(force ? "Force trust cleanup?" : "Finish trust cleanup?", description, force ? "Force cleanup" : "Finish cleanup")) return;
    await runAction(force ? "force-cleanup" : "cleanup", () => localManagementClient.finalizeManagementTlsTrustCleanup({ force }));
  }

  return {
    rotation,
    loading,
    operation,
    operationError,
    pollError,
    lastUpdatedAt,
    custom,
    phase,
    remoteSelected,
    canOperate,
    fleetSummary,
    blockingAgentCount,
    cleanupTitle,
    refresh,
    regenerate,
    stageCustom,
    activate,
    cancelRotation,
    rollback,
    beginRetirement,
    finalizeRetirement,
    finalizeCleanup,
  };
}
