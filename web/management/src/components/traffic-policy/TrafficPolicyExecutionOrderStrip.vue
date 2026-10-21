<script setup lang="ts">
import { computed, type Component } from "vue";
import {
  Database as DatabaseIcon,
  Gauge as GaugeIcon,
  ListOrdered as ListOrderedIcon,
  Network as NetworkIcon,
  Route as RouteIcon,
  RotateCcw as RetryIcon,
  Send as SendIcon,
  Server as ServerIcon,
  ShieldCheck as ShieldCheckIcon,
  SlidersHorizontal as SlidersIcon,
} from "@lucide/vue";
import { NTag } from "naive-ui";

type ExecutionStageState = "match" | "unknown" | "no-match";
type AttentionTone = "neutral" | "info" | "success" | "warning" | "error";
type NaiveTagType = "default" | "info" | "success" | "warning" | "error";
type ExecutionStageIcon =
  | "listener"
  | "waf"
  | "rate-limit"
  | "traffic-shaper"
  | "route"
  | "cache"
  | "retry"
  | "target"
  | "response";

type AttentionTag = {
  label: string;
  tone?: AttentionTone;
  title?: string;
};

type ExecutionStage = {
  key: string;
  label: string;
  description?: string;
  state?: ExecutionStageState;
  tags?: readonly AttentionTag[];
  icon?: ExecutionStageIcon;
  disabled?: boolean;
};

defineOptions({
  name: "TrafficPolicyExecutionOrderStrip",
});

const props = withDefaults(defineProps<{
  stages?: readonly ExecutionStage[];
  title?: string;
  description?: string;
  ariaLabel?: string;
}>(), {
  title: "Execution order",
  description: "",
  ariaLabel: "Traffic policy execution order",
});

const defaultExecutionStages: readonly ExecutionStage[] = [
  {
    key: "listener",
    label: "Listener",
    description: "Protocol, host, path, IP",
    icon: "listener",
  },
  {
    key: "waf",
    label: "WAF",
    description: "Block, challenge, queue",
    icon: "waf",
  },
  {
    key: "rate-limit",
    label: "Rate Limits",
    description: "Request budgets",
    icon: "rate-limit",
  },
  {
    key: "traffic-shaper",
    label: "Traffic Shaper",
    description: "Bandwidth budgets",
    icon: "traffic-shaper",
  },
  {
    key: "route",
    label: "Route",
    description: "Public route match",
    icon: "route",
  },
  {
    key: "cache",
    label: "Cache",
    description: "Lookup and store",
    icon: "cache",
  },
  {
    key: "target",
    label: "Target",
    description: "Backend selection",
    icon: "target",
  },
  {
    key: "response",
    label: "Response",
    description: "Final handling",
    icon: "response",
  },
];

const visibleStages = computed(() => props.stages?.length ? props.stages : defaultExecutionStages);

const iconByName: Record<ExecutionStageIcon, Component> = {
  listener: NetworkIcon,
  waf: ShieldCheckIcon,
  "rate-limit": GaugeIcon,
  "traffic-shaper": SlidersIcon,
  route: RouteIcon,
  cache: DatabaseIcon,
  retry: RetryIcon,
  target: ServerIcon,
  response: SendIcon,
};

function stageClasses(stage: ExecutionStage): Array<string | Record<string, boolean>> {
  return [
    stage.state ? `tp-order-strip__stage--${stage.state}` : "",
    {
      "tp-order-strip__stage--disabled": stage.disabled === true,
    },
  ];
}

function iconFor(stage: ExecutionStage): Component {
  return stage.icon ? iconByName[stage.icon] : ListOrderedIcon;
}

function stateLabel(state?: ExecutionStageState): string {
  switch (state) {
    case "match":
      return "Match";
    case "no-match":
      return "No match";
    case "unknown":
      return "Unknown";
    default:
      return "";
  }
}

function stateTagType(state?: ExecutionStageState): NaiveTagType {
  switch (state) {
    case "match":
      return "success";
    case "unknown":
      return "warning";
    case "no-match":
      return "default";
    default:
      return "default";
  }
}

function attentionTagType(tone?: AttentionTone): NaiveTagType {
  switch (tone) {
    case "success":
      return "success";
    case "warning":
      return "warning";
    case "error":
      return "error";
    case "info":
      return "info";
    default:
      return "default";
  }
}
</script>

<template>
  <section class="tp-order-strip" :aria-label="ariaLabel">
    <div v-if="title || description" class="tp-order-strip__header">
      <div class="tp-order-strip__heading">
        <h3 v-if="title" class="tp-order-strip__title">{{ title }}</h3>
        <p v-if="description" class="tp-order-strip__description">{{ description }}</p>
      </div>
    </div>

    <ol class="tp-order-strip__rail">
      <li
        v-for="(stage, index) in visibleStages"
        :key="stage.key"
        class="tp-order-strip__item"
        :class="{ 'tp-order-strip__item--last': index === visibleStages.length - 1 }"
      >
        <div class="tp-order-strip__stage" :class="stageClasses(stage)">
          <span class="tp-order-strip__icon" aria-hidden="true">
            <component :is="iconFor(stage)" class="tp-order-strip__icon-svg" />
          </span>
          <span class="tp-order-strip__content">
            <span class="tp-order-strip__label">{{ stage.label }}</span>
            <span v-if="stage.description" class="tp-order-strip__stage-description">
              {{ stage.description }}
            </span>
          </span>
          <span class="tp-order-strip__status">
            <NTag v-if="stage.state" size="small" :bordered="false" :type="stateTagType(stage.state)">
              {{ stateLabel(stage.state) }}
            </NTag>
            <NTag
              v-for="tag in stage.tags || []"
              :key="`${stage.key}-${tag.label}`"
              size="small"
              :bordered="false"
              :type="attentionTagType(tag.tone)"
              :title="tag.title || tag.label"
            >
              {{ tag.label }}
            </NTag>
          </span>
        </div>
      </li>
    </ol>
  </section>
</template>

<style scoped>
.tp-order-strip {
  display: grid;
  min-width: 0;
  gap: 0.75rem;
  padding-bottom: 1rem;
}

.tp-order-strip__header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 1rem;
}

.tp-order-strip__heading {
  display: grid;
  min-width: 0;
  gap: 0.25rem;
}

.tp-order-strip__title {
  margin: 0;
  color: var(--app-text);
  font-size: 0.9375rem;
  font-weight: 600;
}

.tp-order-strip__description {
  margin: 0;
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  line-height: 1.5;
}

.tp-order-strip__rail {
  display: grid;
  grid-auto-columns: minmax(9rem, 1fr);
  grid-auto-flow: column;
  gap: 0;
  margin: 0;
  overflow-x: auto;
  border-radius: 8px;
  background: var(--app-panel-muted);
  padding: 0;
  list-style: none;
}

.tp-order-strip__item {
  position: relative;
  min-width: 0;
}

.tp-order-strip__item + .tp-order-strip__item {
  border-left: 1px solid var(--app-border-subtle);
}

.tp-order-strip__stage {
  position: relative;
  display: grid;
  grid-template-columns: 1rem minmax(0, 1fr);
  grid-template-rows: auto auto;
  gap: 0.5rem;
  width: 100%;
  height: 100%;
  min-height: 5.5rem;
  color: var(--app-text);
  padding: 0.75rem 0.875rem;
  text-align: left;
}

.tp-order-strip__stage--disabled {
  opacity: 0.58;
}

.tp-order-strip__icon {
  display: inline-flex;
  padding-top: 0.0625rem;
  color: var(--app-text-muted);
}

.tp-order-strip__stage--match .tp-order-strip__icon {
  color: var(--app-success);
}

.tp-order-strip__stage--unknown .tp-order-strip__icon {
  color: var(--app-warning);
}

.tp-order-strip__icon-svg {
  width: 1rem;
  height: 1rem;
}

.tp-order-strip__content {
  display: grid;
  min-width: 0;
  align-content: start;
  gap: 0.125rem;
}

.tp-order-strip__label {
  overflow: hidden;
  color: var(--app-text);
  font-size: 0.8125rem;
  font-weight: 600;
  line-height: 1.3;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tp-order-strip__stage-description {
  display: -webkit-box;
  overflow: hidden;
  color: var(--app-text-muted);
  font-size: 0.75rem;
  line-height: 1.35;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}

.tp-order-strip__status {
  display: flex;
  grid-column: 2 / -1;
  min-width: 0;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.375rem;
}

.tp-order-strip__status:empty {
  display: none;
}

.tp-order-strip__status :deep(.n-tag) {
  max-width: 100%;
}

.tp-order-strip__status :deep(.n-tag__content) {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

@media (max-width: 760px) {
  .tp-order-strip__rail {
    grid-auto-columns: minmax(11rem, 72vw);
  }
}
</style>
