<script setup lang="ts">
withDefaults(defineProps<{
  title?: string;
  description?: string;
  /** Remove body padding so a table or list can run edge to edge. */
  flush?: boolean;
  /** "card" is a bordered panel; "plain" is a headed group with no frame, for use inside another surface. */
  variant?: "card" | "plain";
  headingLevel?: 2 | 3 | 4;
}>(), {
  flush: false,
  variant: "card",
  headingLevel: 2,
});
</script>

<template>
  <section class="section-card" :class="[`section-card--${variant}`, { 'section-card--flush': flush }]">
    <header v-if="title || description || $slots.actions || $slots.badge" class="section-card__header">
      <div class="section-card__heading">
        <div class="section-card__title-row">
          <component :is="`h${headingLevel}`" v-if="title" class="section-card__title">{{ title }}</component>
          <slot name="badge" />
        </div>
        <p v-if="description || $slots.description" class="section-card__description">
          <slot name="description">{{ description }}</slot>
        </p>
      </div>
      <div v-if="$slots.actions" class="section-card__actions">
        <slot name="actions" />
      </div>
    </header>
    <div class="section-card__body">
      <slot />
    </div>
    <footer v-if="$slots.footer" class="section-card__footer">
      <slot name="footer" />
    </footer>
  </section>
</template>

<style scoped>
.section-card {
  display: grid;
  min-width: 0;
  color: var(--app-text);
}

.section-card--card {
  overflow: hidden;
  border: 1px solid var(--app-border-subtle);
  border-radius: 10px;
  background: var(--app-panel);
}

.section-card__header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 0.75rem 1rem;
}

.section-card--card > .section-card__header {
  padding: 1rem 1.25rem 0;
}

.section-card--card.section-card--flush > .section-card__header {
  padding-bottom: 0.875rem;
  border-bottom: 1px solid var(--app-border-subtle);
}

.section-card--plain {
  gap: 0.75rem;
}

.section-card__heading {
  display: grid;
  min-width: 0;
  gap: 0.2rem;
}

.section-card__title-row {
  display: flex;
  min-width: 0;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.375rem 0.625rem;
}

.section-card__title {
  color: var(--app-text);
  font-size: 0.9375rem;
  font-weight: 600;
  line-height: 1.35;
}

.section-card__description {
  max-width: 72ch;
  color: var(--app-text-muted);
  font-size: 0.8125rem;
  line-height: 1.5;
}

.section-card__actions {
  display: flex;
  flex: 0 0 auto;
  flex-wrap: wrap;
  align-items: center;
  justify-content: flex-end;
  gap: 0.5rem;
}

.section-card--card > .section-card__body {
  min-width: 0;
  padding: 1rem 1.25rem 1.25rem;
}

.section-card--card.section-card--flush > .section-card__body {
  padding: 0;
}

.section-card__footer {
  border-top: 1px solid var(--app-border-subtle);
  padding: 0.75rem 1.25rem;
}

@media (max-width: 639px) {
  .section-card__header {
    flex-direction: column;
  }

  .section-card--card > .section-card__header,
  .section-card--card > .section-card__body {
    padding-inline: 1rem;
  }
}
</style>
