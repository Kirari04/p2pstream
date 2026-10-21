<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { NAlert, NButton, NSkeleton, NTag } from "naive-ui";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
import MetricCard from "@/components/ui/MetricCard.vue";
import MetricStrip from "@/components/ui/MetricStrip.vue";
import SectionCard from "@/components/ui/SectionCard.vue";
import SegmentedControl from "@/components/ui/SegmentedControl.vue";
import {
  availabilityObservedMillis,
  availabilityRangeTicks,
  availabilitySegmentFocusRange,
  availabilitySegments,
  clipAvailabilitySegments,
  recentAvailabilityFocusRange,
} from "@/lib/agentAvailability";
import type {
  AvailabilityRange,
  AvailabilitySegment,
  AvailabilityWindow,
} from "@/lib/agentAvailability";
import { formatLongDuration, formatPercent } from "@/lib/dashboardStats";
import { diagnosticExcerpt, diagnosticInspectionText } from "@/lib/diagnosticText";
import type { GetAgentAvailabilityResponse } from "@/gen/proto/p2pstream/v1/management_pb";

const props = defineProps<{
  availability: GetAgentAvailabilityResponse | null;
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
const selectedAgentValue = computed(() => props.selectedAgentId || null);
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
const freshnessLabel = computed(() => {
  if (!props.availability) return "";
  const ageSeconds = Math.max(
    0,
    Math.floor((currentTimeMillis.value - Number(props.availability.observedUntilUnixMillis)) / 1000),
  );
  if (ageSeconds < 5) return "Updated just now";
  if (ageSeconds < 60) return `Updated ${ageSeconds.toString()}s ago`;
  const ageMinutes = Math.floor(ageSeconds / 60);
  return `Updated ${ageMinutes.toString()}m ago`;
});
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
  if (activeRangeMillis.value <= 36 * 60 * 60 * 1000) {
    return new Intl.DateTimeFormat([], { hour: "2-digit", minute: "2-digit" }).format(millis);
  }
  return new Intl.DateTimeFormat([], { month: "short", day: "numeric" }).format(millis);
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
  resetZoom,
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
    <template v-if="availability" #badge>
      <NTag size="small" :bordered="false" :type="availability.connected ? 'success' : 'error'">
        {{ availability.connected ? 'Online now' : 'Offline now' }}
      </NTag>
      <span class="agent-availability-card__freshness">{{ freshnessLabel }}</span>
    </template>
    <template #actions>
      <div class="agent-availability-card__controls">
        <AccessibleSelect
          :value="selectedAgentValue"
          accessible-label="Agent availability target"
          size="small"
          filterable
          placeholder="Select an agent"
          :options="agentOptions"
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

    <div v-if="!selectedAgentId" class="agent-availability-card__empty">
      <strong>Select an agent to inspect availability</strong>
      <p>The chart will reconstruct online sessions and outage gaps across the selected window.</p>
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
.agent-availability-chart figcaption {
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

.agent-availability-chart figcaption {
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
