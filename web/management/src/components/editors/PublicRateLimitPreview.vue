<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { PublicRateLimitAlgorithm } from "@/gen/proto/p2pstream/v1/management_pb";

type PreviewRequest = {
  id: number;
  atMs: number;
  allowed: boolean;
  x: number;
  y: number;
  opacity: number;
};

type PreviewSnapshot = {
  allowed: number;
  rejected: number;
  remaining: number;
  meterFill: number;
  resetSeconds: number;
  requests: PreviewRequest[];
  activeHits: number;
  tokens: number;
  bucketLevel: number;
};

type ScheduledRequest = {
  id: number;
  atMs: number;
};

const props = defineProps<{
  algorithm: PublicRateLimitAlgorithm;
  limit: number;
  windowSeconds: number;
  burst: number;
  enabled: boolean;
}>();

const PREVIEW_LOOP_MS = 8000;
const MAX_RENDERED_REQUESTS = 36;
const MIN_RENDERED_REQUESTS = 10;
const BURST_PHASE_RATIO = 0.28;
const STEADY_PHASE_RATIO = 0.72;
const TRAVEL_MS = 1550;
const REQUEST_LINGER_MS = 1400;
const SVG_WIDTH = 640;
const SVG_HEIGHT = 188;

const nowMs = ref(0);
const prefersReducedMotion = ref(false);

let rafId: number | null = null;
let loopStartedAt = 0;
let mediaQuery: MediaQueryList | null = null;

const safeLimit = computed(() => Math.max(1, Math.floor(Number(props.limit) || 1)));
const safeWindowSeconds = computed(() => Math.max(1, Math.floor(Number(props.windowSeconds) || 1)));
const effectiveBurst = computed(() => Math.max(1, Math.floor(Number(props.burst) > 0 ? Number(props.burst) : safeLimit.value)));
const safeAlgorithm = computed(() => {
  if (
    props.algorithm === PublicRateLimitAlgorithm.SLIDING_WINDOW ||
    props.algorithm === PublicRateLimitAlgorithm.TOKEN_BUCKET ||
    props.algorithm === PublicRateLimitAlgorithm.LEAKY_BUCKET
  ) {
    return props.algorithm;
  }
  return PublicRateLimitAlgorithm.FIXED_WINDOW;
});

const loopElapsedMs = computed(() => {
  if (!props.enabled || prefersReducedMotion.value) {
    return PREVIEW_LOOP_MS * 0.6;
  }
  const elapsed = nowMs.value - loopStartedAt;
  return ((elapsed % PREVIEW_LOOP_MS) + PREVIEW_LOOP_MS) % PREVIEW_LOOP_MS;
});

const scheduledRequests = computed(() => buildSchedule(safeLimit.value, effectiveBurst.value));
const snapshot = computed(() => buildSnapshot(loopElapsedMs.value));
const segmentCount = computed(() => Math.min(18, safeLimit.value));
const filledSegments = computed(() => Math.round(snapshot.value.meterFill * segmentCount.value));
const sweepX = computed(() => 92 + 456 * clamp(loopElapsedMs.value / PREVIEW_LOOP_MS, 0, 1));
const tankFillHeight = computed(() => 78 * clamp(snapshot.value.meterFill, 0, 1));
const tankFillY = computed(() => 122 - tankFillHeight.value);
const hasCapacityBurst = computed(() =>
  safeAlgorithm.value === PublicRateLimitAlgorithm.TOKEN_BUCKET ||
  safeAlgorithm.value === PublicRateLimitAlgorithm.LEAKY_BUCKET,
);

const algorithmLabel = computed(() => {
  switch (safeAlgorithm.value) {
    case PublicRateLimitAlgorithm.SLIDING_WINDOW:
      return "Sliding window";
    case PublicRateLimitAlgorithm.TOKEN_BUCKET:
      return "Token bucket";
    case PublicRateLimitAlgorithm.LEAKY_BUCKET:
      return "Leaky bucket";
    default:
      return "Fixed window";
  }
});

const meterLabel = computed(() => {
  if (!props.enabled) return "Disabled";
  switch (safeAlgorithm.value) {
    case PublicRateLimitAlgorithm.SLIDING_WINDOW:
      return `Active: ${snapshot.value.activeHits.toString()} / ${safeLimit.value.toString()}`;
    case PublicRateLimitAlgorithm.TOKEN_BUCKET:
      return `Tokens: ${formatDecimal(snapshot.value.tokens)} / ${effectiveBurst.value.toString()}`;
    case PublicRateLimitAlgorithm.LEAKY_BUCKET:
      return `Queue: ${formatDecimal(snapshot.value.bucketLevel)} / ${effectiveBurst.value.toString()}`;
    default:
      return `Used: ${snapshot.value.allowed.toString()} / ${safeLimit.value.toString()}`;
  }
});

const rateLabel = computed(() => {
  if (!props.enabled) return "No requests are evaluated while this rule is disabled.";
  if (safeAlgorithm.value === PublicRateLimitAlgorithm.TOKEN_BUCKET) {
    return `Refill: ${safeLimit.value.toString()} per ${safeWindowSeconds.value.toString()}s window`;
  }
  if (safeAlgorithm.value === PublicRateLimitAlgorithm.LEAKY_BUCKET) {
    return `Drain: ${safeLimit.value.toString()} per ${safeWindowSeconds.value.toString()}s window`;
  }
  if (safeAlgorithm.value === PublicRateLimitAlgorithm.SLIDING_WINDOW) {
    return "Old hits fade out as the active window moves.";
  }
  return `Reset: ${snapshot.value.resetSeconds.toString()}s`;
});

const remainingLabel = computed(() => {
  if (!props.enabled) return "-";
  return snapshot.value.remaining.toString();
});

const allowedLabel = computed(() => props.enabled ? snapshot.value.allowed.toString() : "-");
const rejectedLabel = computed(() => props.enabled ? snapshot.value.rejected.toString() : "-");

onMounted(() => {
  loopStartedAt = performance.now();
  nowMs.value = loopStartedAt;
  mediaQuery = window.matchMedia("(prefers-reduced-motion: reduce)");
  prefersReducedMotion.value = mediaQuery.matches;
  mediaQuery.addEventListener("change", handleMotionPreferenceChange);
  syncAnimation();
});

onBeforeUnmount(() => {
  stopAnimation();
  mediaQuery?.removeEventListener("change", handleMotionPreferenceChange);
  mediaQuery = null;
});

watch(
  () => [props.algorithm, props.limit, props.windowSeconds, props.burst],
  () => resetLoop(),
);

watch(
  () => [props.enabled, prefersReducedMotion.value],
  () => syncAnimation(),
);

function handleMotionPreferenceChange(event: MediaQueryListEvent) {
  prefersReducedMotion.value = event.matches;
}

function resetLoop() {
  loopStartedAt = performance.now();
  nowMs.value = loopStartedAt;
}

function syncAnimation() {
  if (!props.enabled || prefersReducedMotion.value) {
    stopAnimation();
    return;
  }
  startAnimation();
}

function startAnimation() {
  if (rafId !== null) return;
  rafId = window.requestAnimationFrame(tick);
}

function stopAnimation() {
  if (rafId === null) return;
  window.cancelAnimationFrame(rafId);
  rafId = null;
}

function tick(time: number) {
  nowMs.value = time;
  rafId = window.requestAnimationFrame(tick);
}

function buildSchedule(limit: number, burst: number): ScheduledRequest[] {
  const requestCount = Math.min(
    MAX_RENDERED_REQUESTS,
    Math.max(MIN_RENDERED_REQUESTS, limit + Math.ceil(burst * 0.5)),
  );
  const burstCount = Math.max(4, Math.ceil(requestCount * 0.55));
  const steadyCount = Math.max(0, requestCount - burstCount);
  const burstWindow = PREVIEW_LOOP_MS * BURST_PHASE_RATIO;
  const steadyWindow = PREVIEW_LOOP_MS * STEADY_PHASE_RATIO;
  const requests: ScheduledRequest[] = [];

  for (let index = 0; index < burstCount; index += 1) {
    const offset = burstCount <= 1 ? 0 : index / (burstCount - 1);
    requests.push({
      id: index,
      atMs: 180 + offset * (burstWindow - 320),
    });
  }

  for (let index = 0; index < steadyCount; index += 1) {
    const offset = steadyCount <= 1 ? 0 : index / (steadyCount - 1);
    requests.push({
      id: burstCount + index,
      atMs: burstWindow + 260 + offset * (steadyWindow - 580),
    });
  }

  return requests;
}

function buildSnapshot(elapsedMs: number): PreviewSnapshot {
  if (!props.enabled) {
    return {
      allowed: 0,
      rejected: 0,
      remaining: 0,
      meterFill: 0,
      resetSeconds: safeWindowSeconds.value,
      requests: [],
      activeHits: 0,
      tokens: effectiveBurst.value,
      bucketLevel: 0,
    };
  }

  switch (safeAlgorithm.value) {
    case PublicRateLimitAlgorithm.SLIDING_WINDOW:
      return slidingWindowSnapshot(elapsedMs);
    case PublicRateLimitAlgorithm.TOKEN_BUCKET:
      return tokenBucketSnapshot(elapsedMs);
    case PublicRateLimitAlgorithm.LEAKY_BUCKET:
      return leakyBucketSnapshot(elapsedMs);
    default:
      return fixedWindowSnapshot(elapsedMs);
  }
}

function fixedWindowSnapshot(elapsedMs: number): PreviewSnapshot {
  let allowed = 0;
  let rejected = 0;
  const requests = scheduledRequests.value
    .filter((request) => request.atMs <= elapsedMs)
    .map((request) => {
      const requestAllowed = allowed < safeLimit.value;
      if (requestAllowed) allowed += 1;
      else rejected += 1;
      return requestWithPosition(request, requestAllowed, elapsedMs);
    })
    .filter((request) => request.opacity > 0);

  return {
    allowed,
    rejected,
    remaining: Math.max(0, safeLimit.value - allowed),
    meterFill: allowed / safeLimit.value,
    resetSeconds: Math.max(1, Math.ceil((1 - elapsedMs / PREVIEW_LOOP_MS) * safeWindowSeconds.value)),
    requests,
    activeHits: allowed,
    tokens: 0,
    bucketLevel: allowed,
  };
}

function slidingWindowSnapshot(elapsedMs: number): PreviewSnapshot {
  let allowed = 0;
  let rejected = 0;
  const acceptedAt: number[] = [];
  const requests = scheduledRequests.value
    .filter((request) => request.atMs <= elapsedMs)
    .map((request) => {
      pruneHits(acceptedAt, request.atMs - PREVIEW_LOOP_MS);
      const requestAllowed = acceptedAt.length < safeLimit.value;
      if (requestAllowed) {
        acceptedAt.push(request.atMs);
        allowed += 1;
      } else {
        rejected += 1;
      }
      return requestWithPosition(request, requestAllowed, elapsedMs);
    })
    .filter((request) => request.opacity > 0);

  pruneHits(acceptedAt, elapsedMs - PREVIEW_LOOP_MS);
  return {
    allowed,
    rejected,
    remaining: Math.max(0, safeLimit.value - acceptedAt.length),
    meterFill: acceptedAt.length / safeLimit.value,
    resetSeconds: Math.max(1, Math.ceil((1 - elapsedMs / PREVIEW_LOOP_MS) * safeWindowSeconds.value)),
    requests,
    activeHits: acceptedAt.length,
    tokens: 0,
    bucketLevel: acceptedAt.length,
  };
}

function tokenBucketSnapshot(elapsedMs: number): PreviewSnapshot {
  const capacity = effectiveBurst.value;
  let tokens = capacity;
  let previousMs = 0;
  let allowed = 0;
  let rejected = 0;
  const requests = scheduledRequests.value
    .filter((request) => request.atMs <= elapsedMs)
    .map((request) => {
      tokens = refill(tokens, capacity, request.atMs - previousMs);
      previousMs = request.atMs;
      const requestAllowed = tokens >= 1;
      if (requestAllowed) {
        tokens -= 1;
        allowed += 1;
      } else {
        rejected += 1;
      }
      return requestWithPosition(request, requestAllowed, elapsedMs);
    })
    .filter((request) => request.opacity > 0);

  tokens = refill(tokens, capacity, elapsedMs - previousMs);
  return {
    allowed,
    rejected,
    remaining: Math.max(0, Math.floor(tokens)),
    meterFill: tokens / capacity,
    resetSeconds: Math.max(1, Math.ceil((1 - elapsedMs / PREVIEW_LOOP_MS) * safeWindowSeconds.value)),
    requests,
    activeHits: 0,
    tokens,
    bucketLevel: capacity - tokens,
  };
}

function leakyBucketSnapshot(elapsedMs: number): PreviewSnapshot {
  const capacity = effectiveBurst.value;
  let level = 0;
  let previousMs = 0;
  let allowed = 0;
  let rejected = 0;
  const requests = scheduledRequests.value
    .filter((request) => request.atMs <= elapsedMs)
    .map((request) => {
      level = drain(level, request.atMs - previousMs);
      previousMs = request.atMs;
      const requestAllowed = level + 1 <= capacity;
      if (requestAllowed) {
        level += 1;
        allowed += 1;
      } else {
        rejected += 1;
      }
      return requestWithPosition(request, requestAllowed, elapsedMs);
    })
    .filter((request) => request.opacity > 0);

  level = drain(level, elapsedMs - previousMs);
  return {
    allowed,
    rejected,
    remaining: Math.max(0, capacity - Math.ceil(level)),
    meterFill: level / capacity,
    resetSeconds: Math.max(1, Math.ceil((1 - elapsedMs / PREVIEW_LOOP_MS) * safeWindowSeconds.value)),
    requests,
    activeHits: 0,
    tokens: capacity - level,
    bucketLevel: level,
  };
}

function requestWithPosition(request: ScheduledRequest, allowed: boolean, elapsedMs: number): PreviewRequest {
  const age = Math.max(0, elapsedMs - request.atMs);
  const progress = clamp(age / TRAVEL_MS, 0, 1);
  const eased = easeInOutCubic(progress);
  const lane = (request.id % 7) - 3;
  const targetX = allowed ? 604 : 452;
  const baseX = allowed
    ? lerp(34, targetX, eased)
    : progress < 0.78
      ? lerp(34, targetX, eased / 0.78)
      : targetX - Math.sin(((progress - 0.78) / 0.22) * Math.PI) * 7;
  const linger = clamp(1 - Math.max(0, age - TRAVEL_MS) / REQUEST_LINGER_MS, 0, 1);

  return {
    id: request.id,
    atMs: request.atMs,
    allowed,
    x: baseX,
    y: 76 + lane * 8,
    opacity: linger,
  };
}

function refill(tokens: number, capacity: number, elapsedMs: number): number {
  if (elapsedMs <= 0) return tokens;
  return Math.min(capacity, tokens + (elapsedMs * safeLimit.value) / PREVIEW_LOOP_MS);
}

function drain(level: number, elapsedMs: number): number {
  if (elapsedMs <= 0) return level;
  return Math.max(0, level - (elapsedMs * safeLimit.value) / PREVIEW_LOOP_MS);
}

function pruneHits(values: number[], cutoff: number) {
  while (values.length && values[0] <= cutoff) {
    values.shift();
  }
}

function formatDecimal(value: number): string {
  if (value >= 10 || Number.isInteger(value)) return Math.max(0, Math.floor(value)).toString();
  return Math.max(0, value).toFixed(1);
}

function segmentX(index: number): number {
  return 94 + index * (452 / segmentCount.value);
}

function segmentWidth(): number {
  return Math.max(6, 452 / segmentCount.value - 4);
}

function lerp(from: number, to: number, progress: number): number {
  return from + (to - from) * clamp(progress, 0, 1);
}

function easeInOutCubic(value: number): number {
  return value < 0.5 ? 4 * value * value * value : 1 - Math.pow(-2 * value + 2, 3) / 2;
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}
</script>

<template>
  <section
    class="rate-preview"
    :class="{ 'rate-preview-disabled': !enabled }"
    aria-label="Rate limit behavior preview"
  >
    <div class="rate-preview-header">
      <div class="min-w-0">
        <p class="rate-preview-title">{{ algorithmLabel }}</p>
        <p class="rate-preview-subtitle">{{ meterLabel }} / {{ rateLabel }}</p>
      </div>
      <div class="rate-preview-stats">
        <div>
          <span>Allowed</span>
          <strong class="text-green-400">{{ allowedLabel }}</strong>
        </div>
        <div>
          <span>Rejected</span>
          <strong class="text-red-400">{{ rejectedLabel }}</strong>
        </div>
        <div>
          <span>Remaining</span>
          <strong class="text-[#d4d4d8]">{{ remainingLabel }}</strong>
        </div>
      </div>
    </div>

    <svg
      class="rate-preview-svg"
      :viewBox="`0 0 ${SVG_WIDTH} ${SVG_HEIGHT}`"
      role="img"
      aria-hidden="true"
      preserveAspectRatio="none"
    >
      <defs>
        <marker id="rate-preview-arrow" markerWidth="8" markerHeight="8" refX="7" refY="4" orient="auto">
          <path d="M0,0 L8,4 L0,8 Z" fill="#52525b" />
        </marker>
      </defs>

      <path class="preview-track" d="M34 76 C168 76 188 76 276 76 C374 76 398 76 604 76" marker-end="url(#rate-preview-arrow)" />
      <line class="preview-stop-line" x1="452" y1="42" x2="452" y2="112" />
      <text class="preview-label" x="34" y="32">requests</text>
      <text class="preview-label" x="554" y="32">allowed</text>
      <text class="preview-label preview-label-red" x="414" y="132">blocked</text>

      <g v-if="safeAlgorithm === PublicRateLimitAlgorithm.FIXED_WINDOW">
        <rect class="preview-meter-shell" x="88" y="120" width="464" height="20" rx="5" />
        <rect
          v-for="index in segmentCount"
          :key="`fixed-${index}`"
          class="preview-meter-segment"
          :class="{ 'preview-meter-segment-filled': index <= filledSegments }"
          :x="segmentX(index - 1)"
          y="125"
          :width="segmentWidth()"
          height="10"
          rx="2"
        />
        <line class="preview-sweep" :x1="sweepX" y1="116" :x2="sweepX" y2="145" />
      </g>

      <g v-else-if="safeAlgorithm === PublicRateLimitAlgorithm.SLIDING_WINDOW">
        <rect class="preview-meter-shell" x="88" y="120" width="464" height="20" rx="5" />
        <rect class="preview-window-fill" x="88" y="120" :width="464 * snapshot.meterFill" height="20" rx="5" />
        <line class="preview-sweep" :x1="sweepX" y1="116" :x2="sweepX" y2="145" />
        <circle
          v-for="request in scheduledRequests.filter((item) => item.atMs <= loopElapsedMs)"
          :key="`hit-${request.id}`"
          class="preview-rail-hit"
          :cx="92 + 456 * clamp((loopElapsedMs - request.atMs) / PREVIEW_LOOP_MS, 0, 1)"
          cy="130"
          r="3"
          :opacity="clamp(1 - (loopElapsedMs - request.atMs) / PREVIEW_LOOP_MS, 0.12, 1)"
        />
      </g>

      <g v-else>
        <rect class="preview-tank-shell" x="292" y="44" width="58" height="84" rx="8" />
        <rect
          class="preview-tank-fill"
          :class="{ 'preview-tank-fill-amber': hasCapacityBurst && snapshot.meterFill > 0.75 }"
          x="299"
          :y="tankFillY"
          width="44"
          :height="tankFillHeight"
          rx="5"
        />
        <line v-if="safeAlgorithm === PublicRateLimitAlgorithm.LEAKY_BUCKET" class="preview-drain-line" x1="286" y1="134" x2="356" y2="134" />
        <path v-else class="preview-refill-line" d="M321 28 L321 42" marker-end="url(#rate-preview-arrow)" />
      </g>

      <circle
        v-for="request in snapshot.requests"
        :key="request.id"
        class="preview-request"
        :class="request.allowed ? 'preview-request-allowed' : 'preview-request-rejected'"
        :cx="request.x"
        :cy="request.y"
        r="5"
        :opacity="request.opacity"
      />
    </svg>
  </section>
</template>

<style scoped>
.rate-preview {
  display: grid;
  gap: 0.85rem;
  overflow: hidden;
  border: 1px solid #333;
  border-radius: 6px;
  background: #050505;
  padding: 1rem;
  transition: opacity 160ms ease, border-color 160ms ease;
}

.rate-preview-disabled {
  opacity: 0.58;
}

.rate-preview-header {
  display: grid;
  gap: 0.85rem;
}

.rate-preview-title {
  color: #ededed;
  font-size: 0.92rem;
  font-weight: 700;
  line-height: 1.25;
}

.rate-preview-subtitle {
  margin-top: 0.18rem;
  overflow: hidden;
  color: #888;
  font-family: var(--font-mono);
  font-size: 0.72rem;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.rate-preview-stats {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 0.5rem;
}

.rate-preview-stats div {
  min-width: 0;
  border: 1px solid #222;
  border-radius: 6px;
  background: #0b0b0b;
  padding: 0.55rem 0.65rem;
}

.rate-preview-stats span {
  display: block;
  color: #71717a;
  font-size: 0.62rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.rate-preview-stats strong {
  display: block;
  margin-top: 0.2rem;
  font-family: var(--font-mono);
  font-size: 0.98rem;
  line-height: 1.1;
}

.rate-preview-svg {
  width: 100%;
  height: 190px;
  border: 1px solid #222;
  border-radius: 6px;
  background: #030303;
}

.preview-track {
  fill: none;
  stroke: #3f3f46;
  stroke-linecap: round;
  stroke-width: 2;
}

.preview-stop-line {
  stroke: #7f1d1d;
  stroke-dasharray: 4 5;
  stroke-linecap: round;
  stroke-width: 2;
}

.preview-label {
  fill: #71717a;
  font-family: var(--font-mono);
  font-size: 10px;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.preview-label-red {
  fill: #ef4444;
}

.preview-meter-shell,
.preview-tank-shell {
  fill: #0b0b0b;
  stroke: #333;
  stroke-width: 1;
}

.preview-meter-segment {
  fill: #18181b;
}

.preview-meter-segment-filled,
.preview-window-fill,
.preview-tank-fill {
  fill: #22c55e;
}

.preview-tank-fill-amber {
  fill: #f59e0b;
}

.preview-sweep {
  stroke: #22d3ee;
  stroke-linecap: round;
  stroke-width: 2;
}

.preview-rail-hit {
  fill: #22d3ee;
}

.preview-drain-line {
  stroke: #22d3ee;
  stroke-dasharray: 5 4;
  stroke-linecap: round;
  stroke-width: 2;
}

.preview-refill-line {
  fill: none;
  stroke: #22d3ee;
  stroke-linecap: round;
  stroke-width: 2;
}

.preview-request {
  filter: drop-shadow(0 0 9px currentColor);
}

.preview-request-allowed {
  color: #22d3ee;
  fill: #22d3ee;
}

.preview-request-rejected {
  color: #ef4444;
  fill: #ef4444;
}

@media (min-width: 720px) {
  .rate-preview-header {
    grid-template-columns: minmax(0, 1fr) 22rem;
    align-items: start;
  }
}

@media (max-width: 560px) {
  .rate-preview {
    padding: 0.8rem;
  }

  .rate-preview-stats {
    grid-template-columns: 1fr;
  }

  .rate-preview-svg {
    height: 160px;
  }
}
</style>
