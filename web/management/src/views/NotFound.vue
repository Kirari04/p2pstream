<script setup lang="ts">
import { RouterLink } from "vue-router";
import { NButton } from "naive-ui";
</script>

<template>
  <section class="not-found-page" aria-labelledby="not-found-title">
    <div class="not-found-message">
      <p class="not-found-code">404</p>
      <h1 id="not-found-title" class="not-found-title">Page not found</h1>
      <p class="not-found-description">This management page does not exist.</p>
      <div class="not-found-actions">
        <RouterLink v-slot="{ href, navigate }" to="/overview" custom>
          <NButton tag="a" :href="href" type="primary" @click="navigate">Go to overview</NButton>
        </RouterLink>
        <RouterLink v-slot="{ href, navigate }" to="/monitor/traffic" custom>
          <NButton tag="a" :href="href" quaternary @click="navigate">Open monitor</NButton>
        </RouterLink>
      </div>
    </div>
  </section>
</template>

<style scoped>
.not-found-page {
  display: grid;
  min-height: min(34rem, 70vh);
  place-items: center;
  padding: 2rem 1rem;
}

.not-found-message {
  display: grid;
  max-width: 28rem;
  justify-items: center;
  gap: 0.5rem;
  text-align: center;
}

.not-found-code {
  color: var(--app-text-muted);
  font-family: var(--font-mono);
  font-size: 0.8125rem;
  font-variant-numeric: tabular-nums;
}

.not-found-title {
  color: var(--app-text);
  font-size: 1.375rem;
  font-weight: 600;
  letter-spacing: -0.015em;
  line-height: 1.25;
}

.not-found-description {
  color: var(--app-text-muted);
  font-size: 0.875rem;
  line-height: 1.55;
}

.not-found-actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  gap: 0.5rem;
  margin-top: 1rem;
}
</style>
