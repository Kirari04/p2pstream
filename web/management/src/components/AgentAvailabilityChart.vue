<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { NAlert, NButton, NSkeleton, NTag } from "naive-ui";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
import MetricCard from "@/components/ui/MetricCard.vue";
import MetricStrip from "@/components/ui/MetricStrip.vue";
import SectionCard from "@/components/ui/SectionCard.vue";
import SegmentedControl from "@/components/ui/SegmentedControl.vue";
import { useNarrowScreen } from "@/composables/useMediaQuery";
import {
  availabilityObservedMillis,
  availabilityRangeTicks,
  availabilitySegmentFocusRange,
  availabilitySegments,
  clipAvailabilitySegments,
  fleetAvailabilityLanes,
  fleetAvailabilityOverview,
  fleetAvailabilityRange,
  fleetLaneSegmentAt,
  formatUptimeFraction,
  recentAvailabilityFocusRange,
} from "@/lib/agentAvailability";
import type {
  AvailabilityRange,
  AvailabilitySegment,
  AvailabilityWindow,
  FleetAvailabilityLane,
  FleetLaneSegment,
} from "@/lib/agentAvailability";
import { formatLongDuration, formatPercent } from "@/lib/dashboardStats";
import { diagnosticExcerpt, diagnosticInspectionText } from "@/lib/diagnosticText";
import type {
  GetAgentAvailabilityResponse,
  ListAgentAvailabilityResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

const props = defineProps<{
  availability: GetAgentAvailabilityResponse | null;
  /** Every agent's availability on one axis; shown when no agent is selected. */
  fleetAvailability: ListAgentAvailabilityResponse | null;
  selectedAgentId: string;
  agentOptions: Array<{ label: string; value: string }>;
  window: AvailabilityWindow;
  loading: boolean;
  error: string;
}>();

const emit = defineEmits<{
  "select-agent": [agentPublicId: string];
  "update:window": [window: AvailabilityWindow];
  "inspect-segment": [segment: AvailabilitySegment];
  retry: [];
}>();

const windowOptions: Array<{ value: AvailabilityWindow; label: string }> = [
  { value: "24h", label: "24h" },
  { value: "7d", label: "7d" },
  { value: "30d", label: "30d" },
];
const chart = {
  width: 960,
  height: 224,
  left: 68,
  right: 18,
  top: 26,
  bottom: 52,
  onlineY: 62,
  offlineY: 148,
};
const plotWidth = chart.width - chart.left - chart.right;
const plotBottom = chart.height - chart.bottom;
const tooltipWidth = 330;
const tooltipHeight = 52;
const tooltipY = 82;

/** Select value for the fleet view. The NUL prefix keeps it from colliding with an agent public ID. */
const ALL_AGENTS_VALUE = "\u0000all-agents";
const FLEET_TOOLTIP_WIDTH = 288;

const currentTimeMillis = ref(Date.now());
const zoomRange = ref<AvailabilityRange | null>(null);
const hoveredSegment = ref<AvailabilitySegment | null>(null);
const hoverX = ref(chart.left);
let freshnessTimer: number | undefined;

const fullSegments = computed(() => availabilitySegments(props.availability));
const observedMillis = computed(() => availabilityObservedMillis(props.availability));
const observedRange = computed<AvailabilityRange>(() => ({
  startMillis: Number(props.availability?.observedSinceUnixMillis ?? 0n),
  endMillis: Number(props.availability?.observedUntilUnixMillis ?? 0n),
}));
const activeRange = computed<AvailabilityRange>(() => {
  const observed = observedRange.value;
  if (!zoomRange.value) return observed;
  const startMillis = Math.max(observed.startMillis, zoomRange.value.startMillis);
  const endMillis = Math.min(observed.endMillis, zoomRange.value.endMillis);
  return endMillis > startMillis ? { startMillis, endMillis } : observed;
});
const segments = computed(() => clipAvailabilitySegments(fullSegments.value, activeRange.value));
const ticks = computed(() => availabilityRangeTicks(activeRange.value.startMillis, activeRange.value.endMillis));
const activeRangeMillis = computed(() => Math.max(0, activeRange.value.endMillis - activeRange.value.startMillis));
const isZoomed = computed(() => Boolean(zoomRange.value));
const hasDenseActivity = computed(() => recentAvailabilityFocusRange(fullSegments.value) !== null);
const selectedAgentValue = computed(() => props.selectedAgentId || ALL_AGENTS_VALUE);
const agentSelectOptions = computed(() => [
  { label: "All agents", value: ALL_AGENTS_VALUE },
  ...props.agentOptions.filter((option) => option.value !== ALL_AGENTS_VALUE),
]);

const fleetLanes = computed(() => fleetAvailabilityLanes(props.fleetAvailability));
const fleetOverview = computed(() => fleetAvailabilityOverview(fleetLanes.value));
const fleetRange = computed(() => fleetAvailabilityRange(props.fleetAvailability));
const fleetRangeMillis = computed(() => Math.max(0, fleetRange.value.endMillis - fleetRange.value.startMillis));
const narrowScreen = useNarrowScreen();
const fleetTicks = computed(() => availabilityRangeTicks(
  fleetRange.value.startMillis,
  fleetRange.value.endMillis,
  narrowScreen.value ? 3 : 5,
));
const fleetSummaryParts = computed(() => {
  const overview = fleetOverview.value;
  const parts = [
    `${overview.agentCount.toString()} agent${overview.agentCount === 1 ? "" : "s"}`,
    overview.averageUptimeFraction === null
      ? "No observed uptime yet"
      : `${formatUptimeFraction(overview.averageUptimeFraction)} average uptime`,
    `${overview.disconnectCount.toString()} disconnect${overview.disconnectCount === 1 ? "" : "s"}`,
  ];
  if (overview.disabledCount) parts.push(`${overview.disabledCount.toString()} disabled`);
  return parts;
});
const fleetAverageNote = computed(() => fleetOverview.value.disabledCount
  ? "Average covers enabled agents with observed time."
  : "");
const fleetTruncatedNote = computed(() => {
  const count = fleetOverview.value.truncatedCount;
  if (!count) return "";
  return `Older intervals are omitted for ${count.toString()} agent${count === 1 ? "" : "s"} with frequent reconnects. Select the agent for its full history.`;
});
const fleetFreshnessLabel = computed(() => props.fleetAvailability
  ? freshnessFor(props.fleetAvailability.windowUntilUnixMillis)
  : "");
const fleetLanesElement = ref<HTMLElement | null>(null);
const fleetTooltip = ref<{
  key: string;
  title: string;
  detail: string;
  range: string;
  left: number;
  top: number;
} | null>(null);
const chartLabel = computed(() => {
  const agent = diagnosticExcerpt(props.availability?.agentName || props.selectedAgentId || "Selected agent", 64).text;
  return `${agent} availability over ${props.window}: ${formatPercent(props.availability?.uptimePercent)} uptime`;
});
const observationDetail = computed(() => {
  if (!props.availability) return "";
  const observed = observedMillis.value;
  const requested = windowMillis(props.window);
  if (observed + 60_000 < requested) {
    return `${formatLongDuration(observed)} observed since registration or retention cutoff`;
  }
  return `${props.window} observation window`;
});
const freshnessLabel = computed(() => props.availability
  ? freshnessFor(props.availability.observedUntilUnixMillis)
  : "");

function freshnessFor(updatedAtUnixMillis: bigint): string {
  const ageSeconds = Math.max(
    0,
    Math.floor((currentTimeMillis.value - Number(updatedAtUnixMillis)) / 1000),
  );
  if (ageSeconds < 5) return "Updated just now";
  if (ageSeconds < 60) return `Updated ${ageSeconds.toString()}s ago`;
  const ageMinutes = Math.floor(ageSeconds / 60);
  return `Updated ${ageMinutes.toString()}m ago`;
}
const tooltipX = computed(() => Math.max(
  chart.left + 4,
  Math.min(chart.width - chart.right - tooltipWidth - 4, hoverX.value - tooltipWidth / 2),
));
const tooltipTitle = computed(() => {
  if (!hoveredSegment.value) return "";
  const state = hoveredSegment.value.state === "online" ? "Online" : "Offline";
  return `${state} · ${formatAvailabilityDuration(hoveredSegment.value.endMillis - hoveredSegment.value.startMillis)}`;
});
const tooltipDetail = computed(() => hoveredSegment.value
  ? `${formatTimestamp(hoveredSegment.value.startMillis)} — ${formatTimestamp(hoveredSegment.value.endMillis)}`
  : "");

function xFor(millis: number): number {
  if (!props.availability || activeRangeMillis.value <= 0) return chart.left;
  const ratio = Math.max(0, Math.min(1, (millis - activeRange.value.startMillis) / activeRangeMillis.value));
  return chart.left + ratio * plotWidth;
}

function segmentY(state: "online" | "offline"): number {
  return state === "online" ? chart.onlineY : chart.offlineY;
}

function segmentTooltip(segment: AvailabilitySegment): string {
  const state = segment.state === "online" ? "Online" : "Offline";
  return `${state} for ${formatAvailabilityDuration(segment.endMillis - segment.startMillis)}. ${formatTimestamp(segment.startMillis)} to ${formatTimestamp(segment.endMillis)}. Activate to inspect the related session.`;
}

function formatAvailabilityDuration(value: bigint | number): string {
  return Number(value) <= 0 ? "0s" : formatLongDuration(value);
}

function tickLabel(millis: number, index: number): string {
  if (index === ticks.value.length - 1 && Math.abs(activeRange.value.endMillis - observedRange.value.endMillis) < 1_000) {
    return "Now";
  }
  return axisTimeLabel(millis, activeRangeMillis.value);
}

function axisTimeLabel(millis: number, rangeMillis: number): string {
  if (rangeMillis <= 36 * 60 * 60 * 1000) {
    return new Intl.DateTimeFormat([], { hour: "2-digit", minute: "2-digit" }).format(millis);
  }
  return new Intl.DateTimeFormat([], { month: "short", day: "numeric" }).format(millis);
}

function fleetTickLabel(millis: number, index: number): string {
  if (index === fleetTicks.value.length - 1) return "Now";
  return axisTimeLabel(millis, fleetRangeMillis.value);
}

function fleetTickPosition(millis: number): string {
  if (fleetRangeMillis.value <= 0) return "0%";
  return `${(((millis - fleetRange.value.startMillis) / fleetRangeMillis.value) * 100).toFixed(3)}%`;
}

function fleetSegmentStyle(segment: FleetLaneSegment): Record<string, string> {
  const range = fleetRangeMillis.value;
  if (range <= 0) return { left: "0%", width: "0%" };
  const left = ((segment.startMillis - fleetRange.value.startMillis) / range) * 100;
  const width = ((segment.endMillis - segment.startMillis) / range) * 100;
  return { left: `${left.toFixed(3)}%`, width: `${width.toFixed(3)}%` };
}

function fleetLaneKey(lane: FleetAvailabilityLane): string {
  return lane.summary.agentPublicId;
}

function fleetLaneName(lane: FleetAvailabilityLane): string {
  return lane.summary.agentName || lane.summary.agentPublicId || "Unnamed agent";
}

function fleetLaneState(lane: FleetAvailabilityLane): string {
  if (!lane.summary.enabled) return "Disabled";
  return lane.summary.connected ? "Online now" : "Offline now";
}

function fleetLaneDisconnects(lane: FleetAvailabilityLane): string {
  const count = lane.summary.disconnectCount;
  return `${count.toString()} disconnect${count === 1n ? "" : "s"}`;
}

function fleetLaneLabel(lane: FleetAvailabilityLane): string {
  const name = diagnosticExcerpt(fleetLaneName(lane), 64).text;
  const uptime = lane.uptimeFraction === null ? "not observed in this window" : `${formatUptimeFraction(lane.uptimeFraction)} uptime`;
  const parts = [`\u2068${name}\u2069: ${uptime}`, fleetLaneDisconnects(lane), fleetLaneState(lane).toLowerCase()];
  if (lane.summary.intervalsTruncated) parts.push("older intervals omitted");
  return `${parts.join(", ")}. Show availability for this agent.`;
}

function fleetLaneSummaryDetail(lane: FleetAvailabilityLane): string {
  const parts = [fleetLaneState(lane), fleetLaneDisconnects(lane)];
  if (lane.summary.longestDowntimeMillis > 0n) {
    parts.push(`longest outage ${formatAvailabilityDuration(lane.summary.longestDowntimeMillis)}`);
  }
  return parts.join(" · ");
}

function fleetSegmentDetail(segment: FleetLaneSegment): string {
  const duration = formatAvailabilityDuration(segment.endMillis - segment.startMillis);
  switch (segment.state) {
    case "online":
      return `Online · ${duration}${segment.active ? " · current session" : ""}`;
    case "offline":
      return `Offline · ${duration}`;
    case "omitted":
      return `Older intervals omitted · ${duration}`;
    default:
      return `Not observed · ${duration}`;
  }
}

function showFleetLaneAtPointer(event: PointerEvent, lane: FleetAvailabilityLane) {
  const button = event.currentTarget as HTMLElement | null;
  const bar = button?.querySelector<HTMLElement>(".fleet-lane__bar");
  const container = fleetLanesElement.value;
  if (!bar || !container) return;
  const barBounds = bar.getBoundingClientRect();
  const containerBounds = container.getBoundingClientRect();
  const overBar = barBounds.width > 0 && event.clientX >= barBounds.left && event.clientX <= barBounds.right;
  const segment = overBar
    ? fleetLaneSegmentAt(lane.segments, fleetRange.value, (event.clientX - barBounds.left) / barBounds.width)
    : null;
  placeFleetTooltip(lane, segment, event.clientX - containerBounds.left, barBounds.top - containerBounds.top, containerBounds.width);
}

function showFleetLaneAtFocus(event: FocusEvent, lane: FleetAvailabilityLane) {
  const button = event.currentTarget as HTMLElement | null;
  const bar = button?.querySelector<HTMLElement>(".fleet-lane__bar");
  const container = fleetLanesElement.value;
  if (!bar || !container) return;
  const barBounds = bar.getBoundingClientRect();
  const containerBounds = container.getBoundingClientRect();
  placeFleetTooltip(
    lane,
    null,
    barBounds.left - containerBounds.left + barBounds.width / 2,
    barBounds.top - containerBounds.top,
    containerBounds.width,
  );
}

function placeFleetTooltip(
  lane: FleetAvailabilityLane,
  segment: FleetLaneSegment | null,
  anchorX: number,
  barTop: number,
  containerWidth: number,
) {
  const width = Math.min(FLEET_TOOLTIP_WIDTH, containerWidth);
  fleetTooltip.value = {
    key: fleetLaneKey(lane),
    title: diagnosticExcerpt(fleetLaneName(lane), 64).text,
    detail: segment ? fleetSegmentDetail(segment) : fleetLaneSummaryDetail(lane),
    range: segment ? `${formatTimestamp(segment.startMillis)} — ${formatTimestamp(segment.endMillis)}` : "",
    left: Math.max(0, Math.min(containerWidth - width, anchorX - width / 2)),
    top: barTop,
  };
}

function clearFleetLane(lane: FleetAvailabilityLane) {
  if (fleetTooltip.value?.key === fleetLaneKey(lane)) fleetTooltip.value = null;
}

function selectFleetLane(lane: FleetAvailabilityLane) {
  fleetTooltip.value = null;
  emit("select-agent", lane.summary.agentPublicId);
}

function formatTimestamp(millis: number): string {
  return new Intl.DateTimeFormat([], {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  }).format(millis);
}

function windowMillis(window: AvailabilityWindow): number {
  if (window === "7d") return 7 * 24 * 60 * 60 * 1000;
  if (window === "30d") return 30 * 24 * 60 * 60 * 1000;
  return 24 * 60 * 60 * 1000;
}

function handleAgentSelection(value: string | number | Array<string | number> | null) {
  if (value === ALL_AGENTS_VALUE || value === null) {
    emit("select-agent", "");
    return;
  }
  if (typeof value === "string" && value) emit("select-agent", value);
}

function showSegmentAtPointer(event: PointerEvent, segment: AvailabilitySegment) {
  hoveredSegment.value = segment;
  const svg = (event.currentTarget as SVGRectElement).ownerSVGElement;
  const bounds = svg?.getBoundingClientRect();
  if (!bounds?.width) {
    hoverX.value = (xFor(segment.startMillis) + xFor(segment.endMillis)) / 2;
    return;
  }
  const chartX = ((event.clientX - bounds.left) / bounds.width) * chart.width;
  hoverX.value = Math.max(xFor(segment.startMillis), Math.min(xFor(segment.endMillis), chartX));
}

function showSegmentAtCenter(segment: AvailabilitySegment) {
  hoveredSegment.value = segment;
  hoverX.value = (xFor(segment.startMillis) + xFor(segment.endMillis)) / 2;
}

function clearSegment(segment: AvailabilitySegment) {
  if (hoveredSegment.value?.startMillis === segment.startMillis && hoveredSegment.value.state === segment.state) {
    hoveredSegment.value = null;
  }
}

function inspectSegment(segment: AvailabilitySegment) {
  emit("inspect-segment", segment);
  const focus = availabilitySegmentFocusRange(segment, observedRange.value);
  if (focus) zoomRange.value = focus;
  showSegmentAtCenter(segment);
}

function focusRecentActivity() {
  const focus = recentAvailabilityFocusRange(fullSegments.value);
  if (focus) zoomRange.value = focus;
  hoveredSegment.value = null;
}

function resetZoom() {
  zoomRange.value = null;
  hoveredSegment.value = null;
}

watch(
  () => [props.selectedAgentId, props.window],
  () => {
    resetZoom();
    fleetTooltip.value = null;
  },
);

onMounted(() => {
  freshnessTimer = window.setInterval(() => {
    currentTimeMillis.value = Date.now();
  }, 1_000);
});

onBeforeUnmount(() => {
  if (freshnessTimer !== undefined) window.clearInterval(freshnessTimer);
});
</script>

<template>
  <SectionCard
    title="Availability history"
    description="Exact management connection state—no synthetic ping or smoothed gaps."
    class="agent-availability-card"
    aria-label="Availability history"
  >
    <template v-if="selectedAgentId && availability" #badge>
      <NTag size="small" :bordered="false" :type="availability.connected ? 'success' : 'error'">
        {{ availability.connected ? 'Online now' : 'Offline now' }}
      </NTag>
      <span class="agent-availability-card__freshness">{{ freshnessLabel }}</span>
    </template>
    <template v-else-if="!selectedAgentId && fleetAvailability" #badge>
      <span class="agent-availability-card__freshness">{{ fleetFreshnessLabel }}</span>
    </template>
    <template #actions>
      <div class="agent-availability-card__controls">
        <AccessibleSelect
          :value="selectedAgentValue"
          accessible-label="Agent availability target"
          size="small"
          filterable
          placeholder="All agents"
          :options="agentSelectOptions"
          class="agent-availability-card__agent-select"
          @update:value="handleAgentSelection"
        />
        <SegmentedControl
          :model-value="window"
          :options="windowOptions"
          label="Availability window"
          @update:model-value="emit('update:window', $event)"
        />
      </div>
    </template>

    <div v-if="!selectedAgentId" class="agent-availability-card__content">
      <NAlert v-if="error" :type="fleetAvailability ? 'warning' : 'error'" :show-icon="false" :bordered="false">
        <div class="agent-availability-card__alert-content">
          <bdi dir="ltr">{{ diagnosticInspectionText(error) }}</bdi>
          <NButton secondary size="tiny" attr-type="button" @click="emit('retry')">Retry</NButton>
        </div>
      </NAlert>

      <div v-if="loading && !fleetAvailability" class="agent-availability-card__loading" aria-label="Loading fleet availability">
        <NSkeleton text :repeat="1" />
        <NSkeleton height="8rem" />
      </div>

      <div v-else-if="fleetAvailability && !fleetLanes.length" class="agent-availability-card__empty">
        <strong>No agents registered</strong>
        <p>Availability appears here once agents are added and connect to management.</p>
      </div>

      <figure
        v-else-if="fleetAvailability"
        class="fleet-availability"
        :aria-busy="loading"
        aria-labelledby="fleet-availability-summary"
      >
        <p id="fleet-availability-summary" class="fleet-availability__summary">
          <template v-for="(part, index) in fleetSummaryParts" :key="part">
            <span v-if="index > 0" class="fleet-availability__separator" aria-hidden="true"> · </span>
            <span>{{ part }}</span>
          </template>
          <span class="visually-hidden">, over {{ window }}</span>
        </p>

        <div ref="fleetLanesElement" class="fleet-availability__plot">
          <ul class="fleet-availability__lanes" aria-label="Agent availability lanes, worst uptime first">
            <li v-for="lane in fleetLanes" :key="fleetLaneKey(lane)">
              <button
                type="button"
                :class="['fleet-lane', { 'fleet-lane--disabled': !lane.summary.enabled }]"
                :aria-label="fleetLaneLabel(lane)"
                @click="selectFleetLane(lane)"
                @pointerenter="showFleetLaneAtPointer($event, lane)"
                @pointermove="showFleetLaneAtPointer($event, lane)"
                @pointerleave="clearFleetLane(lane)"
                @focus="showFleetLaneAtFocus($event, lane)"
                @blur="clearFleetLane(lane)"
              >
                <span class="fleet-lane__name" :title="diagnosticInspectionText(fleetLaneName(lane))">
                  <bdi dir="ltr">{{ diagnosticExcerpt(fleetLaneName(lane), 64).text }}</bdi>
                  <span v-if="!lane.summary.enabled" class="fleet-lane__tag">Disabled</span>
                </span>
                <span class="fleet-lane__bar" aria-hidden="true">
                  <span
                    v-for="segment in lane.segments"
                    :key="`${segment.startMillis}-${segment.state}`"
                    :class="['fleet-lane__segment', `fleet-lane__segment--${segment.state}`]"
                    :style="fleetSegmentStyle(segment)"
                  />
                </span>
                <span class="fleet-lane__uptime" aria-hidden="true">{{ formatUptimeFraction(lane.uptimeFraction) }}</span>
              </button>
            </li>
          </ul>

          <div class="fleet-availability__axis" aria-hidden="true">
            <span class="fleet-availability__axis-track">
              <span
                v-for="(tick, index) in fleetTicks"
                :key="tick"
                :class="[
                  'fleet-availability__tick',
                  { 'fleet-availability__tick--start': index === 0, 'fleet-availability__tick--end': index === fleetTicks.length - 1 },
                ]"
                :style="{ left: fleetTickPosition(tick) }"
              >{{ fleetTickLabel(tick, index) }}</span>
            </span>
          </div>

          <div
            v-if="fleetTooltip"
            class="fleet-availability__tooltip"
            :style="{ left: `${fleetTooltip.left}px`, top: `${fleetTooltip.top}px` }"
            aria-hidden="true"
          >
            <p class="fleet-availability__tooltip-title"><bdi dir="ltr">{{ fleetTooltip.title }}</bdi></p>
            <p class="fleet-availability__tooltip-detail">{{ fleetTooltip.detail }}</p>
            <p v-if="fleetTooltip.range" class="fleet-availability__tooltip-detail">{{ fleetTooltip.range }}</p>
          </div>
        </div>

        <figcaption>
          <span class="agent-availability-chart__legend-item"><i class="fleet-availability__legend fleet-availability__legend--online" />Online</span>
          <span class="agent-availability-chart__legend-item"><i class="fleet-availability__legend fleet-availability__legend--offline" />Offline</span>
          <span class="agent-availability-chart__legend-item"><i class="fleet-availability__legend fleet-availability__legend--unobserved" />Not observed</span>
          <span class="agent-availability-chart__hint">Select an agent to open its history.</span>
          <p v-if="fleetAverageNote || fleetTruncatedNote" class="fleet-availability__note">
            {{ [fleetAverageNote, fleetTruncatedNote].filter(Boolean).join(" ") }}
          </p>
        </figcaption>
      </figure>
    </div>

    <div v-else class="agent-availability-card__content">
      <NAlert v-if="error" :type="availability ? 'warning' : 'error'" :show-icon="false" :bordered="false">
        <div class="agent-availability-card__alert-content">
          <bdi dir="ltr">{{ diagnosticInspectionText(error) }}</bdi>
          <NButton secondary size="tiny" attr-type="button" @click="emit('retry')">Retry</NButton>
        </div>
      </NAlert>

      <div v-if="loading && !availability" class="agent-availability-card__loading" aria-label="Loading agent availability">
        <NSkeleton text :repeat="2" />
        <NSkeleton height="12rem" />
      </div>

      <div v-else-if="availability" class="agent-availability-card__body" :aria-busy="loading">
        <MetricStrip label="Availability summary">
          <MetricCard label="Availability" :value="formatPercent(availability.uptimePercent)" :detail="observationDetail" />
          <MetricCard label="Total downtime" :value="formatAvailabilityDuration(availability.downtimeMillis)" detail="Inside observed range" />
          <MetricCard
            label="Disconnects"
            :value="availability.disconnectCount.toString()"
            :tone="availability.disconnectCount > 0n ? 'warning' : 'default'"
            detail="Completed sessions"
          />
          <MetricCard label="Longest outage" :value="formatAvailabilityDuration(availability.longestDowntimeMillis)" detail="Continuous offline time" />
        </MetricStrip>

        <figure class="agent-availability-chart">
          <div class="agent-availability-chart__viewport">
            <svg
              :viewBox="`0 0 ${chart.width} ${chart.height}`"
              role="img"
              :aria-label="chartLabel"
              preserveAspectRatio="xMidYMid meet"
            >
              <title>{{ chartLabel }}</title>
              <line class="agent-availability-chart__grid" :x1="chart.left" :x2="chart.width - chart.right" :y1="chart.onlineY" :y2="chart.onlineY" />
              <line class="agent-availability-chart__grid" :x1="chart.left" :x2="chart.width - chart.right" :y1="chart.offlineY" :y2="chart.offlineY" />
              <line class="agent-availability-chart__baseline" :x1="chart.left" :x2="chart.width - chart.right" :y1="plotBottom" :y2="plotBottom" />
              <text class="agent-availability-chart__state-label" :x="chart.left - 12" :y="chart.onlineY + 4" text-anchor="end">Online</text>
              <text class="agent-availability-chart__state-label" :x="chart.left - 12" :y="chart.offlineY + 4" text-anchor="end">Offline</text>

              <g v-for="(segment, index) in segments" :key="`${segment.startMillis}-${segment.state}`">
                <rect
                  :class="`agent-availability-chart__region agent-availability-chart__region--${segment.state}`"
                  :x="xFor(segment.startMillis)"
                  :y="chart.top"
                  :width="Math.max(0.75, xFor(segment.endMillis) - xFor(segment.startMillis))"
                  :height="plotBottom - chart.top"
                  tabindex="0"
                  role="button"
                  :aria-label="segmentTooltip(segment)"
                  @pointerenter="showSegmentAtPointer($event, segment)"
                  @pointermove="showSegmentAtPointer($event, segment)"
                  @pointerleave="clearSegment(segment)"
                  @focus="showSegmentAtCenter(segment)"
                  @blur="clearSegment(segment)"
                  @click="inspectSegment(segment)"
                  @keydown.enter.prevent="inspectSegment(segment)"
                  @keydown.space.prevent="inspectSegment(segment)"
                />
                <line
                  :class="`agent-availability-chart__state-line agent-availability-chart__state-line--${segment.state}`"
                  :x1="xFor(segment.startMillis)"
                  :x2="xFor(segment.endMillis)"
                  :y1="segmentY(segment.state)"
                  :y2="segmentY(segment.state)"
                />
                <line
                  v-if="index > 0"
                  :class="`agent-availability-chart__transition agent-availability-chart__transition--${segment.state}`"
                  :x1="xFor(segment.startMillis)"
                  :x2="xFor(segment.startMillis)"
                  :y1="chart.onlineY"
                  :y2="chart.offlineY"
                />
              </g>

              <g v-for="(tick, index) in ticks" :key="tick">
                <line class="agent-availability-chart__tick" :x1="xFor(tick)" :x2="xFor(tick)" :y1="plotBottom" :y2="plotBottom + 5" />
                <text
                  class="agent-availability-chart__tick-label"
                  :x="xFor(tick)"
                  :y="plotBottom + 22"
                  :text-anchor="index === 0 ? 'start' : index === ticks.length - 1 ? 'end' : 'middle'"
                >
                  {{ tickLabel(tick, index) }}
                </text>
              </g>

              <g v-if="hoveredSegment" class="agent-availability-chart__tooltip" aria-hidden="true">
                <line
                  class="agent-availability-chart__crosshair"
                  :x1="hoverX"
                  :x2="hoverX"
                  :y1="chart.top"
                  :y2="plotBottom"
                />
                <circle
                  :class="`agent-availability-chart__crosshair-point agent-availability-chart__crosshair-point--${hoveredSegment.state}`"
                  :cx="hoverX"
                  :cy="segmentY(hoveredSegment.state)"
                  r="4"
                />
                <rect
                  class="agent-availability-chart__tooltip-panel"
                  :x="tooltipX"
                  :y="tooltipY"
                  :width="tooltipWidth"
                  :height="tooltipHeight"
                  rx="8"
                />
                <text class="agent-availability-chart__tooltip-title" :x="tooltipX + 12" :y="tooltipY + 21">
                  {{ tooltipTitle }}
                </text>
                <text class="agent-availability-chart__tooltip-detail" :x="tooltipX + 12" :y="tooltipY + 39">
                  {{ tooltipDetail }}
                </text>
              </g>
            </svg>
          </div>
          <figcaption>
            <span class="agent-availability-chart__legend-item"><i class="agent-availability-chart__legend agent-availability-chart__legend--online" />Online</span>
            <span class="agent-availability-chart__legend-item"><i class="agent-availability-chart__legend agent-availability-chart__legend--offline" />Offline</span>
            <span class="agent-availability-chart__hint">Hover or focus a segment; activate it to inspect.</span>
            <div class="agent-availability-chart__actions">
              <NTag v-if="isZoomed" size="small" :bordered="false" type="info">Focused view</NTag>
              <NButton
                v-if="hasDenseActivity && !isZoomed"
                quaternary
                size="tiny"
                attr-type="button"
                @click="focusRecentActivity"
              >
                Focus recent activity
              </NButton>
              <NButton v-if="isZoomed" quaternary size="tiny" attr-type="button" @click="resetZoom">
                Reset view
              </NButton>
            </div>
          </figcaption>
        </figure>
        <p class="visually-hidden" aria-live="polite">{{ hoveredSegment ? `${tooltipTitle}. ${tooltipDetail}` : '' }}</p>
      </div>
    </div>
  </SectionCard>
</template>

<style scoped>
.agent-availability-card__freshness {
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-variant-numeric: tabular-nums;
}

.agent-availability-card__controls,
.agent-availability-card__alert-content,
.agent-availability-chart figcaption,
.fleet-availability figcaption {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.625rem;
}

.agent-availability-card__controls {
  width: 100%;
}

.agent-availability-card__agent-select {
  min-width: 0;
  flex: 1 1 16rem;
}

.agent-availability-card__empty {
  display: grid;
  gap: 0.25rem;
  padding: 1.5rem 0 0.75rem;
  text-align: center;
}

.agent-availability-card__empty strong {
  color: var(--app-text);
  font-size: 0.875rem;
  font-weight: 600;
}

.agent-availability-card__empty p {
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  line-height: 1.5;
}

.agent-availability-card__content,
.agent-availability-card__loading,
.agent-availability-card__body {
  display: grid;
  min-width: 0;
  gap: 1rem;
}

.agent-availability-card__alert-content {
  justify-content: space-between;
  overflow-wrap: anywhere;
}

.agent-availability-card__body :deep(.metric-card__detail) {
  white-space: normal;
}

.agent-availability-chart {
  min-width: 0;
  margin: 0;
}

.agent-availability-chart__viewport {
  overflow-x: auto;
}

.agent-availability-chart svg {
  display: block;
  width: 100%;
  min-width: 40rem;
  height: auto;
  aspect-ratio: 960 / 224;
}

.agent-availability-chart__grid {
  pointer-events: none;
  stroke: var(--app-border-subtle);
  stroke-width: 1;
  vector-effect: non-scaling-stroke;
}

.agent-availability-chart__baseline {
  pointer-events: none;
  stroke: var(--app-border-subtle);
  stroke-width: 1;
  vector-effect: non-scaling-stroke;
}

.agent-availability-chart__state-label,
.agent-availability-chart__tick-label {
  fill: var(--app-text-muted);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}

.agent-availability-chart__region {
  cursor: pointer;
  transition: opacity 120ms ease;
}

.agent-availability-chart__region:hover,
.agent-availability-chart__region:focus {
  opacity: 0.16;
}

.agent-availability-chart__region:focus {
  outline: none;
  stroke: var(--app-focus);
  stroke-width: 2;
  vector-effect: non-scaling-stroke;
}

.agent-availability-chart__region--online {
  fill: var(--app-success);
  opacity: 0.045;
}

.agent-availability-chart__region--offline {
  fill: var(--app-error);
  opacity: 0.06;
}

.agent-availability-chart__state-line,
.agent-availability-chart__transition {
  pointer-events: none;
  stroke-linecap: round;
  vector-effect: non-scaling-stroke;
}

.agent-availability-chart__state-line {
  stroke-width: 2;
}

.agent-availability-chart__transition {
  stroke-width: 1;
  opacity: 0.55;
}

.agent-availability-chart__state-line--online,
.agent-availability-chart__transition--online {
  stroke: var(--app-success);
}

.agent-availability-chart__state-line--offline,
.agent-availability-chart__transition--offline {
  stroke: var(--app-error);
}

.agent-availability-chart__tick {
  pointer-events: none;
  stroke: var(--app-border-subtle);
  stroke-width: 1;
  vector-effect: non-scaling-stroke;
}

.agent-availability-chart__tooltip {
  pointer-events: none;
}

.agent-availability-chart__crosshair {
  stroke: var(--app-text-muted);
  stroke-dasharray: 2 3;
  stroke-opacity: 0.6;
  stroke-width: 1;
  vector-effect: non-scaling-stroke;
}

.agent-availability-chart__crosshair-point {
  stroke: var(--app-panel);
  stroke-width: 2;
  vector-effect: non-scaling-stroke;
}

.agent-availability-chart__crosshair-point--online {
  fill: var(--app-success);
}

.agent-availability-chart__crosshair-point--offline {
  fill: var(--app-error);
}

.agent-availability-chart__tooltip-panel {
  fill: var(--app-panel);
  stroke: var(--app-border-subtle);
  stroke-width: 1;
  vector-effect: non-scaling-stroke;
}

.agent-availability-chart__tooltip-title {
  fill: var(--app-text);
  font-size: 13px;
  font-variant-numeric: tabular-nums;
  font-weight: 600;
}

.agent-availability-chart__tooltip-detail {
  fill: var(--app-text-muted);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
}

.agent-availability-chart figcaption,
.fleet-availability figcaption {
  min-height: 2.25rem;
  border-top: 1px solid var(--app-border-subtle);
  padding-top: 0.625rem;
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

.agent-availability-chart__legend-item {
  display: inline-flex;
  align-items: center;
  gap: 0.375rem;
}

.agent-availability-chart__legend {
  width: 0.875rem;
  height: 2px;
  border-radius: 999px;
}

.agent-availability-chart__legend--online { background: var(--app-success); }
.agent-availability-chart__legend--offline { background: var(--app-error); }

.agent-availability-chart__hint {
  margin-left: auto;
}

.agent-availability-chart__actions {
  display: inline-flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.375rem;
}

/* All-agents swimlanes: one lane per agent on the window's shared time axis. */
.fleet-availability {
  --fleet-lane-columns: minmax(6rem, 13rem) minmax(0, 1fr) 4rem;
  display: grid;
  min-width: 0;
  gap: 0.75rem;
  margin: 0;
}

.fleet-availability__summary {
  margin: 0;
  color: var(--app-text);
  font-size: 0.875rem;
  font-variant-numeric: tabular-nums;
}

.fleet-availability__separator {
  color: var(--app-text-muted);
}

.fleet-availability__plot {
  position: relative;
  min-width: 0;
}

.fleet-availability__lanes {
  display: grid;
  margin: 0;
  padding: 0;
  list-style: none;
}

.fleet-lane {
  display: grid;
  width: 100%;
  min-width: 0;
  grid-template-columns: var(--fleet-lane-columns);
  align-items: center;
  gap: 0.75rem;
  margin: 0;
  border: 0;
  border-radius: 6px;
  background: transparent;
  padding: 0.375rem 0.5rem;
  color: var(--app-text);
  cursor: pointer;
  font: inherit;
  text-align: left;
}

.fleet-lane:hover {
  background: var(--app-hover);
}

.fleet-lane:focus-visible {
  outline: 2px solid var(--app-focus);
  outline-offset: -2px;
}

.fleet-lane__name {
  display: flex;
  min-width: 0;
  align-items: baseline;
  gap: 0.375rem;
  font-size: 0.8125rem;
  font-weight: 500;
}

.fleet-lane__name bdi {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  unicode-bidi: isolate;
}

.fleet-lane__tag {
  flex: 0 0 auto;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-weight: 400;
}

.fleet-lane__bar {
  position: relative;
  display: block;
  height: 0.625rem;
  overflow: hidden;
  border-radius: 4px;
  background: var(--app-panel-muted);
}

.fleet-lane__segment {
  position: absolute;
  top: 0;
  bottom: 0;
}

.fleet-lane__segment--online {
  background: var(--app-success);
}

/* Short outages must stay visible on a 30-day axis. */
.fleet-lane__segment--offline {
  min-width: 2px;
  background: var(--app-error);
}

.fleet-lane__segment--unobserved,
.fleet-lane__segment--omitted,
.fleet-availability__legend--unobserved {
  background: repeating-linear-gradient(
    135deg,
    color-mix(in srgb, var(--app-text-muted) 45%, transparent) 0 1px,
    transparent 1px 4px
  );
}

.fleet-lane__segment--omitted {
  opacity: 0.6;
}

.fleet-lane__uptime {
  color: var(--app-text);
  font-family: var(--font-mono);
  font-size: 0.75rem;
  font-variant-numeric: tabular-nums;
  text-align: right;
}

.fleet-lane--disabled .fleet-lane__name bdi,
.fleet-lane--disabled .fleet-lane__bar,
.fleet-lane--disabled .fleet-lane__uptime {
  opacity: 0.5;
}

.fleet-availability__axis {
  display: grid;
  grid-template-columns: var(--fleet-lane-columns);
  gap: 0.75rem;
  padding: 0.25rem 0.5rem 0;
}

.fleet-availability__axis-track {
  position: relative;
  grid-column: 2;
  height: 1rem;
  border-top: 1px solid var(--app-border-subtle);
}

.fleet-availability__tick {
  position: absolute;
  top: 0.25rem;
  color: var(--app-text-muted);
  font-size: 0.6875rem;
  font-variant-numeric: tabular-nums;
  line-height: 1;
  transform: translateX(-50%);
  white-space: nowrap;
}

.fleet-availability__tick--start {
  transform: none;
}

.fleet-availability__tick--end {
  transform: translateX(-100%);
}

.fleet-availability__tooltip {
  position: absolute;
  z-index: 2;
  width: min(18rem, 100%);
  border: 1px solid var(--app-border-subtle);
  border-radius: 8px;
  background: var(--app-panel);
  box-shadow: 0 6px 18px rgb(0 0 0 / 0.08);
  padding: 0.5rem 0.75rem;
  pointer-events: none;
  transform: translateY(calc(-100% - 0.375rem));
}

.fleet-availability__tooltip p {
  margin: 0;
}

.fleet-availability__tooltip-title {
  overflow: hidden;
  color: var(--app-text);
  font-size: 0.8125rem;
  font-weight: 600;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.fleet-availability__tooltip-title bdi {
  unicode-bidi: isolate;
}

.fleet-availability__tooltip-detail {
  color: var(--app-text-muted);
  font-size: 0.75rem;
  font-variant-numeric: tabular-nums;
}

.fleet-availability__legend {
  width: 0.875rem;
  height: 0.5rem;
  border-radius: 2px;
}

.fleet-availability__legend--online { background: var(--app-success); }
.fleet-availability__legend--offline { background: var(--app-error); }
.fleet-availability__legend--unobserved { box-shadow: inset 0 0 0 1px var(--app-border-subtle); }

.fleet-availability__note {
  flex-basis: 100%;
  margin: 0;
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

@media (max-width: 639px) {
  .fleet-lane {
    grid-template-columns: minmax(0, 1fr) auto;
    grid-template-areas:
      "name uptime"
      "bar bar";
    gap: 0.375rem 0.75rem;
  }

  .fleet-lane__name { grid-area: name; }
  .fleet-lane__bar { grid-area: bar; }
  .fleet-lane__uptime { grid-area: uptime; }

  .fleet-availability__axis {
    grid-template-columns: minmax(0, 1fr);
  }

  .fleet-availability__axis-track {
    grid-column: 1;
  }
}

@media (pointer: coarse) {
  .fleet-lane {
    min-height: 2.75rem;
  }
}

@media (min-width: 720px) {
  .agent-availability-card__controls {
    width: auto;
    flex-wrap: nowrap;
    justify-content: flex-end;
  }

  .agent-availability-card__agent-select {
    width: min(22rem, 30vw);
    flex-basis: 16rem;
  }

  .agent-availability-chart svg {
    min-width: 0;
  }
}

@media (max-width: 479px) {
  .agent-availability-chart__hint {
    display: none !important;
  }

  .agent-availability-chart__actions {
    width: 100%;
  }
}

@media (prefers-reduced-motion: reduce) {
  .agent-availability-chart__region {
    transition: none;
  }
}
</style>
