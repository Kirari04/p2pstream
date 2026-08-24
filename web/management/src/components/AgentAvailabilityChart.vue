<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { NAlert, NButton, NButtonGroup, NSkeleton, NTag } from "naive-ui";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
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

const windowOptions: AvailabilityWindow[] = ["24h", "7d", "30d"];
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
  const agent = props.availability?.agentName || props.selectedAgentId || "Selected agent";
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
  <section class="surface-card agent-availability-card" aria-labelledby="agent-availability-title">
    <div class="agent-availability-card__header">
      <div>
        <div class="agent-availability-card__title-row">
          <h4 id="agent-availability-title">Availability history</h4>
          <NTag
            v-if="availability"
            size="small"
            :bordered="false"
            :type="availability.connected ? 'success' : 'error'"
          >
            {{ availability.connected ? 'Online now' : 'Offline now' }}
          </NTag>
          <NTag v-if="availability" size="small" :bordered="false" type="default" class="agent-availability-card__freshness">
            {{ freshnessLabel }}
          </NTag>
        </div>
        <p>Exact management connection state—no synthetic ping or smoothed gaps.</p>
      </div>
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
        <NButtonGroup size="small" role="group" aria-label="Availability window">
          <NButton
            v-for="label in windowOptions"
            :key="label"
            attr-type="button"
            :aria-pressed="window === label"
            :type="window === label ? 'primary' : 'default'"
            @click="emit('update:window', label)"
          >
            {{ label }}
          </NButton>
        </NButtonGroup>
      </div>
    </div>

    <div v-if="!selectedAgentId" class="agent-availability-card__empty">
      <div class="agent-availability-card__empty-signal" aria-hidden="true">
        <span /><span /><span /><span /><span />
      </div>
      <div>
        <strong>Select an agent to inspect availability</strong>
        <p>The chart will reconstruct online sessions and outage gaps across the selected window.</p>
      </div>
    </div>

    <template v-else>
      <NAlert v-if="error" :type="availability ? 'warning' : 'error'" :show-icon="false" class="agent-availability-card__alert">
        <div class="agent-availability-card__alert-content">
          <span>{{ error }}</span>
          <NButton secondary size="tiny" attr-type="button" @click="emit('retry')">Retry</NButton>
        </div>
      </NAlert>

      <div v-if="loading && !availability" class="agent-availability-card__loading" aria-label="Loading agent availability">
        <div class="agent-availability-card__stats">
          <NSkeleton v-for="index in 4" :key="index" text :repeat="2" />
        </div>
        <NSkeleton height="14rem" />
      </div>

      <div v-else-if="availability" class="agent-availability-card__body" :aria-busy="loading">
        <dl class="agent-availability-card__stats">
          <div>
            <dt>Availability</dt>
            <dd>{{ formatPercent(availability.uptimePercent) }}</dd>
            <small>{{ observationDetail }}</small>
          </div>
          <div>
            <dt>Total downtime</dt>
            <dd>{{ formatAvailabilityDuration(availability.downtimeMillis) }}</dd>
            <small>inside observed range</small>
          </div>
          <div>
            <dt>Disconnects</dt>
            <dd>{{ availability.disconnectCount.toString() }}</dd>
            <small>completed sessions</small>
          </div>
          <div>
            <dt>Longest outage</dt>
            <dd>{{ formatAvailabilityDuration(availability.longestDowntimeMillis) }}</dd>
            <small>continuous offline time</small>
          </div>
        </dl>

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
                <line class="agent-availability-chart__tick" :x1="xFor(tick)" :x2="xFor(tick)" :y1="plotBottom" :y2="plotBottom + 6" />
                <text
                  class="agent-availability-chart__tick-label"
                  :x="xFor(tick)"
                  :y="plotBottom + 24"
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
                  rx="5"
                />
                <text class="agent-availability-chart__tooltip-title" :x="tooltipX + 12" :y="tooltipY + 20">
                  {{ tooltipTitle }}
                </text>
                <text class="agent-availability-chart__tooltip-detail" :x="tooltipX + 12" :y="tooltipY + 39">
                  {{ tooltipDetail }}
                </text>
              </g>
            </svg>
          </div>
          <figcaption>
            <span><i class="agent-availability-chart__legend agent-availability-chart__legend--online" />Online</span>
            <span><i class="agent-availability-chart__legend agent-availability-chart__legend--offline" />Offline</span>
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
    </template>
  </section>
</template>

<style scoped>
.agent-availability-card {
  overflow: hidden;
}

.agent-availability-card__header {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 1rem;
  border-bottom: 1px solid var(--app-border);
  padding: 1rem 1.25rem;
}

.agent-availability-card__title-row,
.agent-availability-card__controls,
.agent-availability-card__alert-content,
.agent-availability-chart figcaption {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.65rem;
}

.agent-availability-card__title-row h4 {
  margin: 0;
  color: var(--app-text);
  font-size: 1rem;
  font-weight: 700;
}

.agent-availability-card__freshness {
  color: var(--app-text-muted);
}

.agent-availability-card__header p,
.agent-availability-card__empty p {
  margin: 0.25rem 0 0;
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  line-height: 1.5;
}

.agent-availability-card__controls {
  width: 100%;
}

.agent-availability-card__agent-select {
  min-width: 0;
  flex: 1 1 16rem;
}

.agent-availability-card__empty {
  display: flex;
  align-items: center;
  gap: 1rem;
  padding: 2rem 1.25rem;
}

.agent-availability-card__empty strong {
  color: var(--app-text);
  font-size: 0.875rem;
}

.agent-availability-card__empty-signal {
  display: flex;
  align-items: end;
  gap: 3px;
  width: 3.5rem;
  height: 2.25rem;
  flex: 0 0 auto;
  border-bottom: 1px solid var(--app-border);
}

.agent-availability-card__empty-signal span {
  width: 0.4rem;
  border-radius: 2px 2px 0 0;
  background: var(--app-border);
}

.agent-availability-card__empty-signal span:nth-child(1) { height: 35%; }
.agent-availability-card__empty-signal span:nth-child(2) { height: 70%; }
.agent-availability-card__empty-signal span:nth-child(3) { height: 48%; }
.agent-availability-card__empty-signal span:nth-child(4) { height: 90%; }
.agent-availability-card__empty-signal span:nth-child(5) { height: 62%; }

.agent-availability-card__alert {
  margin: 1rem 1.25rem 0;
}

.agent-availability-card__alert-content {
  justify-content: space-between;
}

.agent-availability-card__loading,
.agent-availability-card__body {
  display: grid;
  gap: 1rem;
  padding: 1rem 1.25rem 1.25rem;
}

.agent-availability-card__stats {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  margin: 0;
  overflow: hidden;
  border: 1px solid var(--app-border-subtle);
  border-radius: 6px;
  background: color-mix(in srgb, var(--app-panel-muted) 58%, transparent);
}

.agent-availability-card__stats > * {
  display: grid;
  align-content: center;
  gap: 0.25rem;
  min-width: 0;
  padding: 0.8rem 0.9rem;
}

.agent-availability-card__stats > *:nth-child(even) {
  border-left: 1px solid var(--app-border-subtle);
}

.agent-availability-card__stats > *:nth-child(n + 3) {
  border-top: 1px solid var(--app-border-subtle);
}

.agent-availability-card__stats dt {
  color: var(--app-text-muted);
  font-size: 0.675rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.agent-availability-card__stats dd {
  margin: 0;
  color: var(--app-text);
  font-family: var(--font-mono);
  font-size: 1.25rem;
  font-weight: 700;
  line-height: 1.2;
}

.agent-availability-card__stats small {
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 0.72rem;
  line-height: 1.35;
  text-overflow: ellipsis;
}

.agent-availability-chart {
  margin: 0;
  overflow: hidden;
  border: 1px solid var(--app-border-subtle);
  border-radius: 6px;
  background:
    linear-gradient(180deg, color-mix(in srgb, var(--app-panel-muted) 70%, transparent), transparent 74%),
    var(--app-panel);
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
  stroke-dasharray: 3 5;
  stroke-width: 1;
  vector-effect: non-scaling-stroke;
}

.agent-availability-chart__state-label,
.agent-availability-chart__tick-label {
  fill: var(--app-text-muted);
  font-family: var(--font-mono);
  font-size: 11px;
}

.agent-availability-chart__region {
  cursor: pointer;
  transition: opacity 120ms ease;
}

.agent-availability-chart__region:hover,
.agent-availability-chart__region:focus {
  opacity: 0.18;
}

.agent-availability-chart__region:focus {
  outline: none;
  stroke: var(--app-accent);
  stroke-width: 2;
  vector-effect: non-scaling-stroke;
}

.agent-availability-chart__region--online {
  fill: var(--app-success);
  opacity: 0.06;
}

.agent-availability-chart__region--offline {
  fill: var(--app-error);
  opacity: 0.075;
}

.agent-availability-chart__state-line,
.agent-availability-chart__transition {
  pointer-events: none;
  stroke-width: 2.5;
  vector-effect: non-scaling-stroke;
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
  stroke: var(--app-border);
  stroke-width: 1;
  vector-effect: non-scaling-stroke;
}

.agent-availability-chart__tooltip {
  pointer-events: none;
}

.agent-availability-chart__crosshair {
  stroke: color-mix(in srgb, var(--app-text-muted) 70%, transparent);
  stroke-dasharray: 3 3;
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
  fill: color-mix(in srgb, var(--app-panel) 96%, var(--app-text) 4%);
  stroke: var(--app-border);
  stroke-width: 1;
  vector-effect: non-scaling-stroke;
}

.agent-availability-chart__tooltip-title {
  fill: var(--app-text);
  font-family: var(--font-mono);
  font-size: 13px;
  font-weight: 700;
}

.agent-availability-chart__tooltip-detail {
  fill: var(--app-text-muted);
  font-family: var(--font-mono);
  font-size: 10.5px;
}

.agent-availability-chart figcaption {
  min-height: 2.5rem;
  border-top: 1px solid var(--app-border-subtle);
  padding: 0.65rem 0.85rem;
  color: var(--app-text-muted);
  font-size: 0.72rem;
}

.agent-availability-chart figcaption span {
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
}

.agent-availability-chart__legend {
  width: 0.55rem;
  height: 0.55rem;
  border-radius: 2px;
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
  gap: 0.35rem;
}

@media (min-width: 720px) {
  .agent-availability-card__header {
    flex-direction: row;
    align-items: flex-end;
    justify-content: space-between;
  }

  .agent-availability-card__controls {
    width: auto;
    justify-content: flex-end;
  }

  .agent-availability-card__agent-select {
    width: min(24rem, 32vw);
    flex-basis: 18rem;
  }

  .agent-availability-card__stats {
    grid-template-columns: repeat(4, minmax(0, 1fr));
  }

  .agent-availability-card__stats > * + * {
    border-top: 0;
    border-left: 1px solid var(--app-border-subtle);
  }

  .agent-availability-chart svg {
    min-width: 0;
  }
}

@media (max-width: 479px) {
  .agent-availability-card__controls .n-button-group,
  .agent-availability-card__controls .n-button {
    flex: 1 1 0;
  }

  .agent-availability-card__controls .n-button-group {
    display: flex;
    width: 100%;
  }

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
