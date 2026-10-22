<script setup lang="ts">
/**
 * The full-width "Exact values" expansion row of a Traffic policy rule list.
 * Render it directly after the data row it belongs to, inside the list's
 * role="rowgroup"; the toggle in the data row points here with aria-controls.
 */
export type PolicyExactValue = {
  label: string;
  /** Readable value. Rendered as text with dir="auto". */
  value?: string;
  /** Machine value (CEL expression, methods, site key). Rendered as a wrapped ltr code block. */
  code?: string;
  /** Span both columns of the two-column list. */
  wide?: boolean;
};

defineProps<{
  id: string;
  /** Accessible name for the panel, e.g. "Exact values for rate-limit". */
  label: string;
  entries: readonly PolicyExactValue[];
}>();
</script>

<template>
  <div :id="id" class="policy-exact-row" role="row">
    <div class="policy-exact-row__cell" role="cell">
      <dl class="policy-exact-values" :aria-label="label">
        <div
          v-for="entry in entries"
          :key="entry.label"
          class="policy-exact-values__item"
          :class="{ 'policy-exact-values__item--wide': entry.wide }"
        >
          <dt>{{ entry.label }}</dt>
          <dd>
            <span v-if="entry.value" class="policy-exact-values__text" dir="auto">{{ entry.value }}</span>
            <code v-if="entry.code" class="policy-exact-values__code" dir="ltr">{{ entry.code }}</code>
          </dd>
        </div>
      </dl>
    </div>
  </div>
</template>

<style scoped>
.policy-exact-row {
  display: grid;
  min-width: 0;
  grid-template-columns: minmax(0, 1fr);
  padding: 0 1rem 1rem;
}

.policy-exact-row__cell {
  grid-column: 1 / -1;
  min-width: 0;
}

.policy-exact-values {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0.75rem 1.5rem;
  margin: 0;
  border-top: 1px dashed var(--app-border-subtle);
  padding-top: 0.875rem;
}

.policy-exact-values__item {
  display: grid;
  min-width: 0;
  align-content: start;
  gap: 0.25rem;
}

.policy-exact-values__item--wide {
  grid-column: 1 / -1;
}

.policy-exact-values dt {
  color: var(--app-text-muted);
  font-size: 0.75rem;
  line-height: 1.35;
}

.policy-exact-values dd {
  display: grid;
  min-width: 0;
  justify-items: start;
  gap: 0.375rem;
  margin: 0;
  color: var(--app-text);
  font-size: 0.8125rem;
  font-variant-numeric: tabular-nums;
  line-height: 1.45;
}

.policy-exact-values__text {
  max-width: 100%;
  overflow-wrap: break-word;
  unicode-bidi: isolate;
}

.policy-exact-values__code {
  display: block;
  box-sizing: border-box;
  max-width: 100%;
  border-radius: 6px;
  padding: 0.5rem 0.625rem;
  background: var(--app-panel-muted);
  color: var(--app-text);
  font-family: var(--font-mono);
  font-size: 0.75rem;
  line-height: 1.5;
  overflow-wrap: anywhere;
  unicode-bidi: isolate;
  white-space: pre-wrap;
}

@container (min-width: 48rem) {
  .policy-exact-row {
    padding: 0 1.25rem 1rem;
  }
}

@media (max-width: 639px) {
  .policy-exact-values {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
