<script setup lang="ts">
import { NDataTable, NTag } from "naive-ui";
import type { DataTableColumns } from "naive-ui";
import { computed, h } from "vue";
import {
  ManagementTlsAgentRolloutState,
  type ManagementTlsAgentRollout,
  type ManagementTlsRotationPhase,
} from "@/gen/proto/p2pstream/v1/management_pb";
import { diagnosticExcerpt } from "@/lib/diagnosticText";
import { managementTlsAgentDetail } from "@/lib/managementTlsPresentation";

const props = defineProps<{
  agents: readonly ManagementTlsAgentRollout[];
  phase: ManagementTlsRotationPhase;
}>();

const columns = computed<DataTableColumns<ManagementTlsAgentRollout>>(() => [
  {
    title: "Agent",
    key: "agent",
    minWidth: 170,
    render: (agent) => h("div", { class: "layout-grid space-2xs min-width-zero" }, [
      h("span", { class: "clip-text weight-medium" }, agent.agentName || agent.agentPublicId),
      h("code", { class: "clip-text copy-2xs muted-text", title: agent.agentPublicId }, agent.agentPublicId),
    ]),
  },
  {
    title: "Connection",
    key: "connected",
    width: 104,
    render: (agent) => h(NTag, { size: "small", bordered: false, type: agent.connected ? "success" : "default" }, { default: () => agent.enabled ? (agent.connected ? "Online" : "Offline") : "Disabled" }),
  },
  {
    title: "Trust state",
    key: "state",
    width: 160,
    render: (agent) => h("div", { class: "layout-row align-center space-xs" }, [
      h(NTag, { size: "small", bordered: false, type: rolloutTagType(agent.state) }, { default: () => rolloutLabel(agent.state) }),
      agent.needsTrustAttention ? h(NTag, { size: "small", bordered: false, type: "warning" }, { default: () => "Attention" }) : null,
    ]),
  },
  {
    title: "Version",
    key: "version",
    width: 104,
    render: (agent) => h("code", { class: "copy-xs" }, agent.agentVersion ? diagnosticExcerpt(agent.agentVersion, 48).text : "Unknown"),
  },
  {
    title: "Last report",
    key: "reported",
    width: 150,
    render: (agent) => h("span", { class: "copy-xs muted-text" }, formatTime(agent.reportedAtUnixMillis)),
  },
  {
    title: "Detail",
    key: "detail",
    minWidth: 160,
    render: (agent) => h("span", { class: "copy-xs muted-text wrap-anywhere" }, agent.errorDetail ? diagnosticExcerpt(agent.errorDetail, 160).text : managementTlsAgentDetail(agent, props.phase)),
  },
]);

function rolloutLabel(state: ManagementTlsAgentRolloutState): string {
  return ({
    [ManagementTlsAgentRolloutState.DISABLED]: "Excluded",
    [ManagementTlsAgentRolloutState.INCOMPATIBLE]: "Incompatible",
    [ManagementTlsAgentRolloutState.PENDING]: "Pending",
    [ManagementTlsAgentRolloutState.READY]: "Ready",
    [ManagementTlsAgentRolloutState.FAILED]: "Failed",
    [ManagementTlsAgentRolloutState.STRANDED]: "Stranded",
  } as Record<number, string>)[state] ?? "Unknown";
}

function rolloutTagType(state: ManagementTlsAgentRolloutState): "default" | "success" | "warning" | "error" {
  if (state === ManagementTlsAgentRolloutState.READY) return "success";
  if (state === ManagementTlsAgentRolloutState.FAILED || state === ManagementTlsAgentRolloutState.STRANDED) return "error";
  if (state === ManagementTlsAgentRolloutState.PENDING || state === ManagementTlsAgentRolloutState.INCOMPATIBLE) return "warning";
  return "default";
}

function formatTime(value: bigint): string {
  if (value === 0n) return "Never";
  return new Date(Number(value)).toLocaleString();
}
</script>

<template>
  <NDataTable :columns="columns" :data="[...agents]" :row-key="(agent: ManagementTlsAgentRollout) => agent.agentId.toString()" :pagination="false" :bordered="false" :scroll-x="848" size="small" />
</template>
