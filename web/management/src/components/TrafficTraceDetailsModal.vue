<script setup lang="ts">
import { computed } from "vue";
import { NCollapse, NCollapseItem, NDrawer, NDrawerContent, NEmpty, NTag } from "naive-ui";
import { editorDrawerWidth } from "@/lib/naiveUi";
import { diagnosticInspectionText } from "@/lib/diagnosticText";
import type { TraceRequest } from "@/types/trafficTrace";
import {
  PublicRateLimitAlgorithm,
  PublicRouteTargetTransport,
  PublicRouteTargetType,
  PublicTrafficShaperBudgetScope,
  PublicWafActivationMode,
  PublicWafRuleAction,
  TrafficTraceLevel,
  TrafficTraceStage,
} from "@/gen/proto/p2pstream/v1/management_pb";

const props = defineProps<{
  modelValue: boolean;
  request: TraceRequest | null;
  level: TrafficTraceLevel;
}>();

const emit = defineEmits<{
  (event: "update:modelValue", value: boolean): void;
}>();

const isOpen = computed({
  get: () => props.modelValue,
  set: (value: boolean) => emit("update:modelValue", value),
});

const showDetailed = computed(() => props.level >= TrafficTraceLevel.DETAILED);
const showHeaders = computed(() => props.level >= TrafficTraceLevel.HEADERS);
const showDebug = computed(() => props.level >= TrafficTraceLevel.DEBUG);
const requestHeaders = computed(() => collectedEntries("requestHeaders"));
const responseHeaders = computed(() => collectedEntries("responseHeaders"));
const debugAttributes = computed(() => collectedEntries("debugAttributes"));
const lifecycleEvents = computed(() => [...(props.request?.events ?? [])].sort((left, right) => {
  if (left.sequence === right.sequence) return 0;
  return left.sequence < right.sequence ? -1 : 1;
}));

const outcomeTitle = computed(() => {
  const request = props.request;
  if (!request) return "Trace unavailable";
  if (request.errorKind) return safe(request.errorKind);
  if (request.statusCode > 0n) return `${request.statusCode.toString()} response`;
  return stageLabel(request.stage);
});

const outcomeSummary = computed(() => {
  const request = props.request;
  if (!request) return "This trace is no longer retained.";
  if (request.errorKind) return `The request ended with a captured proxy error after ${formatDuration(request.durationMs)}.`;
  if (request.statusCode > 0n) return `The proxy returned status ${request.statusCode.toString()} after ${formatDuration(request.durationMs)}.`;
  return `The request most recently reached ${stageLabel(request.stage).toLocaleLowerCase()}.`;
});

const outcomeTagType = computed<"success" | "warning" | "error" | "default">(() => {
  const request = props.request;
  if (!request) return "default";
  if (request.stage === TrafficTraceStage.FAILED || request.stage === TrafficTraceStage.WAF_BLOCKED || request.statusCode >= 500n) return "error";
  if (request.stage === TrafficTraceStage.RATE_LIMITED || request.stage === TrafficTraceStage.ACCESS_DENIED || request.statusCode >= 400n) return "warning";
  if (request.statusCode >= 200n) return "success";
  return "default";
});

const resolvedFlow = computed(() => {
  const request = props.request;
  if (!request) return [];
  return [
    { label: "Listener", value: request.listenerName || idValue(request.listenerId) },
    { label: "Route", value: request.routeLabel || (request.defaultRoute ? "Default route" : "Not resolved") },
    { label: "Target", value: request.routeTargetName || idValue(request.routeTargetId) },
    { label: "Agent", value: request.agentName || request.agentPublicId || "Not selected" },
  ];
});

const policyDecisions = computed(() => {
  const request = props.request;
  if (!request) return [];
  const decisions: Array<{ label: string; name: string; detail: string }> = [];
  if (request.wafRuleId || request.wafRuleName || request.wafChallengeKind) {
    const details = [
      wafActionLabel(request.wafAction),
      wafActivationLabel(request.wafActivationMode),
      request.wafActivationMode === PublicWafActivationMode.AUTOMATIC
        ? request.wafAutomaticActive ? "automatic condition active" : "automatic condition inactive"
        : "",
      request.wafChallengeKind,
    ].filter((value) => value && value !== "-");
    decisions.push({
      label: "WAF",
      name: request.wafRuleName || idValue(request.wafRuleId),
      detail: details.join(" · "),
    });
  }
  if (request.rateLimitRuleId || request.rateLimitRuleName || request.stage === TrafficTraceStage.RATE_LIMITED) {
    decisions.push({
      label: "Rate limit",
      name: request.rateLimitRuleName || idValue(request.rateLimitRuleId),
      detail: rateLimitAlgorithmLabel(request.rateLimitAlgorithm),
    });
  }
  if (request.trafficShaperRuleId || request.trafficShaperRuleName) {
    decisions.push({
      label: "Traffic shaper",
      name: request.trafficShaperRuleName || idValue(request.trafficShaperRuleId),
      detail: `${trafficShaperScopeLabel(request.trafficShaperBudgetScope)} · up ${formatRate(request.trafficShaperUploadBytesPerSecond)} · down ${formatRate(request.trafficShaperDownloadBytesPerSecond)} · free ${formatBytes(request.trafficShaperRequestExemptBytes)} request / ${formatBytes(request.trafficShaperResponseExemptBytes)} response`,
    });
  }
  if (request.cacheRuleId || request.cacheRuleName || request.cacheStatus) {
    decisions.push({
      label: "Cache",
      name: request.cacheRuleName || idValue(request.cacheRuleId),
      detail: request.cacheStatus || "Evaluated without a captured result",
    });
  }
  return decisions;
});

const captureNotes = computed(() => {
  const notes: string[] = [];
  if (!showDetailed.value) notes.push("Host, query, target metadata, and error detail were not requested at the Basic trace level.");
  if (!showHeaders.value) notes.push("Request and response headers were not requested at this trace level.");
  if (!showDebug.value) notes.push("Byte counts and debug attributes were not requested at this trace level.");
  if (showHeaders.value && !requestHeaders.value.length && !responseHeaders.value.length) notes.push("No headers were included in the retained trace events.");
  if (showDebug.value && !debugAttributes.value.length) notes.push("No debug attributes were included in the retained trace events.");
  return notes;
});

function statusClass(status: bigint, stage: TrafficTraceStage): string {
  if (stage === TrafficTraceStage.FAILED || stage === TrafficTraceStage.WAF_BLOCKED) return "trace-status--error";
  if (stage === TrafficTraceStage.WAF_CAPTCHA_CHALLENGED || stage === TrafficTraceStage.WAF_WAITING_ROOM || stage === TrafficTraceStage.RATE_LIMITED || stage === TrafficTraceStage.ACCESS_DENIED) return "trace-status--warning";
  if (status >= 500n) return "trace-status--error";
  if (status >= 400n) return "trace-status--warning";
  if (status >= 200n) return "trace-status--success";
  return "trace-status--muted";
}

function stageLabel(stage: TrafficTraceStage): string {
  switch (stage) {
    case TrafficTraceStage.RECEIVED: return "Received";
    case TrafficTraceStage.ROUTE_RESOLVED: return "Route resolved";
    case TrafficTraceStage.BACKEND_SELECTED: return "Target selected";
    case TrafficTraceStage.AGENT_SELECTED: return "Agent selected";
    case TrafficTraceStage.WAF_EVALUATED: return "WAF evaluated";
    case TrafficTraceStage.WAF_BLOCKED: return "WAF blocked";
    case TrafficTraceStage.WAF_CAPTCHA_CHALLENGED: return "Captcha challenge";
    case TrafficTraceStage.WAF_CAPTCHA_VERIFIED: return "Captcha verified";
    case TrafficTraceStage.WAF_WAITING_ROOM: return "Waiting room";
    case TrafficTraceStage.CACHE_LOOKUP: return "Cache lookup";
    case TrafficTraceStage.CACHE_HIT: return "Cache hit";
    case TrafficTraceStage.CACHE_MISS: return "Cache miss";
    case TrafficTraceStage.CACHE_BYPASS: return "Cache bypass";
    case TrafficTraceStage.CACHE_STORED: return "Cache stored";
    case TrafficTraceStage.TRAFFIC_SHAPER_SELECTED: return "Traffic shaper selected";
    case TrafficTraceStage.UPSTREAM_STARTED: return "Upstream started";
    case TrafficTraceStage.UPSTREAM_RESPONDED: return "Upstream responded";
    case TrafficTraceStage.RESPONSE_SENT: return "Response sent";
    case TrafficTraceStage.FAILED: return "Failed";
    case TrafficTraceStage.RATE_LIMITED: return "Rate limited";
    case TrafficTraceStage.ACCESS_GRANTED: return "Access granted";
    case TrafficTraceStage.ACCESS_DENIED: return "Access denied";
    default: return "Unknown";
  }
}

function rateLimitAlgorithmLabel(algorithm: PublicRateLimitAlgorithm): string {
  switch (algorithm) {
    case PublicRateLimitAlgorithm.SLIDING_WINDOW: return "Sliding window";
    case PublicRateLimitAlgorithm.TOKEN_BUCKET: return "Token bucket";
    case PublicRateLimitAlgorithm.LEAKY_BUCKET: return "Leaky bucket";
    case PublicRateLimitAlgorithm.FIXED_WINDOW: return "Fixed window";
    default: return "Algorithm not captured";
  }
}

function trafficShaperScopeLabel(scope: PublicTrafficShaperBudgetScope): string {
  return scope === PublicTrafficShaperBudgetScope.PER_REQUEST ? "Per request" : scope === PublicTrafficShaperBudgetScope.PER_KEY ? "Per key" : "Scope not captured";
}

function wafActionLabel(action: PublicWafRuleAction): string {
  switch (action) {
    case PublicWafRuleAction.CAPTCHA: return "Captcha";
    case PublicWafRuleAction.WAITING_ROOM: return "Waiting room";
    case PublicWafRuleAction.BLOCK: return "Block";
    default: return "-";
  }
}

function wafActivationLabel(mode: PublicWafActivationMode): string {
  if (mode === PublicWafActivationMode.AUTOMATIC) return "Automatic";
  if (mode === PublicWafActivationMode.ALWAYS) return "Always";
  return "-";
}

function routeTargetTypeLabel(type: PublicRouteTargetType): string {
  if (type === PublicRouteTargetType.STATIC) return "Static";
  if (type === PublicRouteTargetType.PROXY) return "Proxy";
  return "Not captured";
}

function routeTargetTransportLabel(transport: PublicRouteTargetTransport): string {
  if (transport === PublicRouteTargetTransport.AGENT) return "Agent-selected";
  if (transport === PublicRouteTargetTransport.DIRECT) return "Direct";
  return "Not captured";
}

function formatRate(value: bigint): string {
  const bytes = Number(value || 0n);
  if (bytes <= 0) return "unlimited";
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024).toString()} KiB/s`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MiB/s`;
}

function formatDate(value: bigint): string {
  if (!value) return "Time not captured";
  const millis = Number(value);
  if (!Number.isFinite(millis)) return "Invalid server timestamp";
  const date = new Date(millis);
  if (Number.isNaN(date.getTime())) return "Invalid server timestamp";
  return date.toLocaleTimeString();
}

function formatDuration(value: bigint): string {
  if (!value) return "Not captured";
  const millis = Number(value);
  return millis < 1000 ? `${millis.toString()} ms` : `${(millis / 1000).toFixed(2)} s`;
}

function formatBytes(value: bigint): string {
  const bytes = Number(value || 0n);
  if (bytes < 1024) return `${bytes.toString()} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MiB`;
}

function numberLabel(value: number): string {
  return new Intl.NumberFormat().format(value);
}

function safe(value: string, fallback = "(empty)"): string {
  return value ? diagnosticInspectionText(value) : fallback;
}

function idValue(value: bigint): string {
  return value > 0n ? `#${value.toString()}` : "Not resolved";
}

function collectedEntries(field: "requestHeaders" | "responseHeaders" | "debugAttributes") {
  const collected = new Map<string, string>();
  for (const event of lifecycleEvents.value) {
    for (const [name, value] of Object.entries(event[field])) {
      collected.set(name, value);
    }
  }
  return [...collected.entries()]
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([name, value], index) => ({
      key: index,
      name: safe(name),
      value: safe(value),
    }));
}
</script>

<template>
  <NDrawer
    v-model:show="isOpen"
    placement="right"
    :width="editorDrawerWidth('52rem')"
    aria-label="Trace details"
    class="editor-drawer trace-details-drawer"
  >
    <NDrawerContent title="Trace details" closable>
      <div v-if="request" class="trace-details-content">
        <p class="trace-security-note">
          Listener and request values are exact. Backslashes and invisible control or formatting characters are shown in a reversible escaped form.
        </p>

        <section class="trace-outcome" aria-labelledby="trace-outcome-heading">
          <div>
            <div class="trace-outcome__heading-row">
              <p>Outcome</p>
              <NTag size="small" :bordered="false" :type="outcomeTagType">{{ stageLabel(request.stage) }}</NTag>
            </div>
            <h3 id="trace-outcome-heading"><bdi dir="ltr">{{ outcomeTitle }}</bdi></h3>
            <p>{{ outcomeSummary }}</p>
          </div>
          <dl class="trace-outcome__facts">
            <div>
              <dt>Status</dt>
              <dd :class="statusClass(request.statusCode, request.stage)">{{ request.statusCode > 0n ? request.statusCode.toString() : "—" }}</dd>
            </div>
            <div>
              <dt>Duration</dt>
              <dd>{{ formatDuration(request.durationMs) }}</dd>
            </div>
            <div>
              <dt>Events retained</dt>
              <dd>{{ numberLabel(lifecycleEvents.length) }}</dd>
            </div>
          </dl>
        </section>

        <p v-if="request.sampledEventCount > 0" class="trace-callout trace-callout--warning">
          {{ numberLabel(request.sampledEventCount) }} intermediate events were omitted under UI load. The lifecycle below is incomplete.
        </p>

        <section class="trace-section" aria-labelledby="trace-flow-heading">
          <div class="trace-section__heading">
            <div>
              <h4 id="trace-flow-heading">Resolved flow</h4>
              <p>The configuration objects selected for this request.</p>
            </div>
          </div>
          <dl class="trace-flow-facts">
            <div v-for="field in resolvedFlow" :key="field.label">
              <dt>{{ field.label }}</dt>
              <dd><bdi dir="ltr">{{ safe(field.value, "Not resolved") }}</bdi></dd>
            </div>
          </dl>
        </section>

        <section class="trace-section trace-lifecycle" aria-labelledby="trace-lifecycle-heading">
          <div class="trace-section__heading">
            <div>
              <h4 id="trace-lifecycle-heading">Lifecycle</h4>
              <p>Retained events in server sequence order.</p>
            </div>
          </div>
          <ol v-if="lifecycleEvents.length">
            <li v-for="event in lifecycleEvents" :key="event.sequence.toString()">
              <span class="trace-lifecycle__marker" aria-hidden="true" />
              <div>
                <strong>{{ stageLabel(event.stage) }}</strong>
                <span>{{ formatDate(event.occurredAtUnixMillis) }}</span>
              </div>
              <span class="trace-lifecycle__duration">{{ formatDuration(event.durationMs) }}</span>
            </li>
          </ol>
          <NEmpty v-else size="small" description="No lifecycle events are retained for this request." />
        </section>

        <section v-if="policyDecisions.length" class="trace-section" aria-labelledby="trace-policy-heading">
          <div class="trace-section__heading">
            <div>
              <h4 id="trace-policy-heading">Policy decisions</h4>
              <p>Rules recorded while the request moved through policy evaluation.</p>
            </div>
          </div>
          <dl class="trace-policy-list">
            <div v-for="decision in policyDecisions" :key="decision.label">
              <dt>{{ decision.label }}</dt>
              <dd>
                <strong><bdi dir="ltr">{{ safe(decision.name, "Not captured") }}</bdi></strong>
                <span><bdi dir="ltr">{{ safe(decision.detail, "No decision detail captured") }}</bdi></span>
              </dd>
            </div>
          </dl>
        </section>

        <section v-if="captureNotes.length" class="trace-capture-notes" aria-labelledby="trace-capture-heading">
          <h4 id="trace-capture-heading">Capture limits</h4>
          <ul>
            <li v-for="note in captureNotes" :key="note">{{ note }}</li>
          </ul>
        </section>

        <NCollapse class="trace-disclosure" :default-expanded-names="['request']">
          <NCollapseItem title="Request and routing fields" name="request">
            <dl class="trace-field-grid">
              <div class="trace-field trace-field--wide">
                <dt>Request ID</dt>
                <dd><bdi dir="ltr">{{ safe(request.requestId) }}</bdi></dd>
              </div>
              <div class="trace-field">
                <dt>Method</dt>
                <dd><bdi dir="ltr">{{ safe(request.method) }}</bdi></dd>
              </div>
              <div class="trace-field">
                <dt>Current stage</dt>
                <dd>{{ stageLabel(request.stage) }}</dd>
              </div>
              <div class="trace-field trace-field--wide">
                <dt>Path</dt>
                <dd><bdi dir="ltr">{{ safe(request.path, "/") }}</bdi></dd>
              </div>
              <template v-if="showDetailed">
                <div class="trace-field">
                  <dt>Host</dt>
                  <dd><bdi dir="ltr">{{ safe(request.host) }}</bdi></dd>
                </div>
                <div class="trace-field">
                  <dt>Query</dt>
                  <dd><bdi dir="ltr">{{ safe(request.query) }}</bdi></dd>
                </div>
                <div class="trace-field">
                  <dt>Target type</dt>
                  <dd>{{ routeTargetTypeLabel(request.routeTargetType) }}</dd>
                </div>
                <div class="trace-field">
                  <dt>Transport</dt>
                  <dd>{{ routeTargetTransportLabel(request.routeTargetTransport) }}</dd>
                </div>
                <div class="trace-field trace-field--wide">
                  <dt>Target origin</dt>
                  <dd><bdi dir="ltr">{{ safe(request.targetOrigin) }}</bdi></dd>
                </div>
                <div class="trace-field trace-field--wide">
                  <dt>Error kind</dt>
                  <dd><bdi dir="ltr">{{ safe(request.errorKind) }}</bdi></dd>
                </div>
              </template>
            </dl>
          </NCollapseItem>

          <NCollapseItem v-if="showHeaders" title="Captured headers" name="headers">
            <div class="trace-header-grid">
              <div class="trace-raw-panel">
                <h5>Request headers</h5>
                <dl v-if="requestHeaders.length">
                  <div v-for="entry in requestHeaders" :key="entry.key">
                    <dt><bdi dir="ltr">{{ entry.name }}</bdi></dt>
                    <dd><bdi dir="ltr">{{ entry.value }}</bdi></dd>
                  </div>
                </dl>
                <p v-else>No request headers were included in retained events.</p>
              </div>
              <div class="trace-raw-panel">
                <h5>Response headers</h5>
                <dl v-if="responseHeaders.length">
                  <div v-for="entry in responseHeaders" :key="entry.key">
                    <dt><bdi dir="ltr">{{ entry.name }}</bdi></dt>
                    <dd><bdi dir="ltr">{{ entry.value }}</bdi></dd>
                  </div>
                </dl>
                <p v-else>No response headers were included in retained events.</p>
              </div>
            </div>
          </NCollapseItem>

          <NCollapseItem v-if="showDebug" title="Debug data" name="debug">
            <dl class="trace-debug-facts">
              <div><dt>Request bytes</dt><dd>{{ formatBytes(request.requestBytes) }}</dd></div>
              <div><dt>Response bytes</dt><dd>{{ formatBytes(request.responseBytes) }}</dd></div>
            </dl>
            <div class="trace-raw-panel trace-raw-panel--debug">
              <h5>Debug attributes</h5>
              <dl v-if="debugAttributes.length">
                <div v-for="entry in debugAttributes" :key="entry.key">
                  <dt><bdi dir="ltr">{{ entry.name }}</bdi></dt>
                  <dd><bdi dir="ltr">{{ entry.value }}</bdi></dd>
                </div>
              </dl>
              <p v-else>No debug attributes were included in retained events.</p>
            </div>
          </NCollapseItem>
        </NCollapse>
      </div>
      <NEmpty v-else size="small" description="This trace is no longer retained." />
    </NDrawerContent>
  </NDrawer>
</template>

<style scoped>
.trace-details-content {
  display: grid;
  gap: 1.25rem;
}

.trace-security-note,
.trace-callout,
.trace-capture-notes {
  border: 1px solid var(--app-border);
  border-radius: 6px;
  background: var(--app-panel-muted);
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  line-height: 1.55;
  padding: 0.75rem;
}

.trace-security-note {
  margin: 0;
}

.trace-callout--warning {
  border-color: color-mix(in srgb, var(--app-warning) 45%, var(--app-border));
  color: var(--app-warning);
}

.trace-outcome {
  display: grid;
  gap: 1rem;
  border-left: 3px solid var(--app-accent);
  background: var(--app-panel-muted);
  padding: 1rem;
}

.trace-outcome__heading-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 0.5rem;
}

.trace-outcome__heading-row > p,
.trace-outcome > div > p:last-child,
.trace-section__heading p {
  color: var(--app-text-muted);
  font-size: 0.8125rem;
}

.trace-outcome h3 {
  margin: 0.25rem 0;
  color: var(--app-text);
  font-size: 1.25rem;
  font-weight: 700;
  overflow-wrap: anywhere;
}

.trace-outcome bdi,
.trace-flow-facts bdi,
.trace-policy-list bdi,
.trace-field bdi,
.trace-raw-panel bdi {
  direction: ltr;
  unicode-bidi: isolate;
}

.trace-outcome__facts,
.trace-flow-facts,
.trace-debug-facts {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  margin: 0;
  border-top: 1px solid var(--app-border-subtle);
  padding-top: 0.75rem;
}

.trace-outcome__facts > div,
.trace-flow-facts > div,
.trace-debug-facts > div {
  min-width: 0;
  padding-inline: 0.75rem;
}

.trace-outcome__facts > div:first-child,
.trace-flow-facts > div:first-child,
.trace-debug-facts > div:first-child {
  padding-left: 0;
}

.trace-outcome__facts > div + div,
.trace-flow-facts > div + div,
.trace-debug-facts > div + div {
  border-left: 1px solid var(--app-border-subtle);
}

.trace-outcome__facts dt,
.trace-flow-facts dt,
.trace-debug-facts dt,
.trace-field dt,
.trace-policy-list dt {
  color: var(--app-text-muted);
  font-size: 0.7rem;
  font-weight: 650;
}

.trace-outcome__facts dd,
.trace-flow-facts dd,
.trace-debug-facts dd,
.trace-field dd {
  min-width: 0;
  margin: 0.25rem 0 0;
  overflow-wrap: anywhere;
  color: var(--app-text);
  font-size: 0.875rem;
  font-weight: 650;
}

.trace-section {
  display: grid;
  gap: 0.75rem;
  border-top: 1px solid var(--app-border);
  padding-top: 1rem;
}

.trace-section__heading h4,
.trace-capture-notes h4 {
  color: var(--app-text);
  font-size: 0.9375rem;
  font-weight: 700;
}

.trace-flow-facts {
  grid-template-columns: repeat(4, minmax(0, 1fr));
  border: 1px solid var(--app-border-subtle);
  border-radius: 6px;
  background: var(--app-panel-muted);
  padding: 0.75rem;
}

.trace-lifecycle ol {
  display: grid;
  margin: 0;
  padding: 0;
  list-style: none;
}

.trace-lifecycle li {
  position: relative;
  display: grid;
  min-width: 0;
  grid-template-columns: 1rem minmax(0, 1fr) auto;
  gap: 0.75rem;
  padding: 0.625rem 0;
}

.trace-lifecycle li + li {
  border-top: 1px solid var(--app-border-subtle);
}

.trace-lifecycle__marker {
  width: 0.625rem;
  height: 0.625rem;
  margin-top: 0.2rem;
  border: 2px solid var(--app-accent);
  border-radius: 50%;
  background: var(--app-panel);
}

.trace-lifecycle li > div {
  display: flex;
  min-width: 0;
  flex-wrap: wrap;
  gap: 0.25rem 0.75rem;
}

.trace-lifecycle strong {
  color: var(--app-text);
  font-size: 0.8125rem;
}

.trace-lifecycle li span,
.trace-lifecycle__duration {
  color: var(--app-text-muted);
  font-family: var(--font-mono);
  font-size: 0.75rem;
}

.trace-policy-list {
  display: grid;
  margin: 0;
}

.trace-policy-list > div {
  display: grid;
  min-width: 0;
  grid-template-columns: 7rem minmax(0, 1fr);
  gap: 0.75rem;
  border-top: 1px solid var(--app-border-subtle);
  padding: 0.75rem 0;
}

.trace-policy-list dd {
  display: grid;
  min-width: 0;
  gap: 0.25rem;
  margin: 0;
  overflow-wrap: anywhere;
}

.trace-policy-list strong {
  color: var(--app-text);
  font-size: 0.8125rem;
}

.trace-policy-list dd > span {
  color: var(--app-text-muted);
  font-size: 0.75rem;
}

.trace-capture-notes ul {
  display: grid;
  gap: 0.25rem;
  margin: 0.5rem 0 0;
  padding-left: 1.15rem;
}

.trace-disclosure {
  border-top: 1px solid var(--app-border);
  padding-top: 0.25rem;
}

.trace-field-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0.75rem;
  margin: 0;
}

.trace-field {
  min-width: 0;
  border: 1px solid var(--app-border-subtle);
  border-radius: 6px;
  background: var(--app-panel-muted);
  padding: 0.75rem;
}

.trace-field--wide {
  grid-column: 1 / -1;
}

.trace-field dd,
.trace-raw-panel dd,
.trace-raw-panel dt {
  font-family: var(--font-mono);
}

.trace-header-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0.75rem;
}

.trace-raw-panel {
  min-width: 0;
  border: 1px solid var(--app-border-subtle);
  border-radius: 6px;
  background: var(--app-panel-muted);
  padding: 0.75rem;
}

.trace-raw-panel--debug {
  margin-top: 0.75rem;
}

.trace-raw-panel h5 {
  margin-bottom: 0.75rem;
  color: var(--app-text);
  font-size: 0.8125rem;
  font-weight: 700;
}

.trace-raw-panel dl {
  display: grid;
  gap: 0.75rem;
  margin: 0;
}

.trace-raw-panel dl > div {
  min-width: 0;
}

.trace-raw-panel dt,
.trace-raw-panel dd {
  min-width: 0;
  margin: 0;
  overflow-wrap: anywhere;
  font-size: 0.75rem;
  line-height: 1.5;
  white-space: pre-wrap;
}

.trace-raw-panel dt {
  color: var(--app-text-muted);
}

.trace-raw-panel dd {
  color: var(--app-text);
}

.trace-raw-panel p {
  color: var(--app-text-muted);
  font-size: 0.8125rem;
}

.trace-debug-facts {
  grid-template-columns: repeat(2, minmax(0, 1fr));
  border: 1px solid var(--app-border-subtle);
  border-radius: 6px;
  background: var(--app-panel-muted);
  padding: 0.75rem;
}

@media (max-width: 640px) {
  .trace-outcome__facts,
  .trace-flow-facts,
  .trace-field-grid,
  .trace-header-grid {
    grid-template-columns: 1fr;
  }

  .trace-outcome__facts > div,
  .trace-flow-facts > div {
    padding: 0.5rem 0;
  }

  .trace-outcome__facts > div + div,
  .trace-flow-facts > div + div {
    border-top: 1px solid var(--app-border-subtle);
    border-left: 0;
  }

  .trace-policy-list > div {
    grid-template-columns: 1fr;
    gap: 0.25rem;
  }
}

@media (pointer: coarse) {
  .trace-disclosure :deep(.n-collapse-item__header-main),
  .trace-details-drawer :deep(.n-base-close) {
    min-height: 44px;
  }
}
</style>
