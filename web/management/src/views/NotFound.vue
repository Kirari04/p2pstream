<script setup lang="ts">
import { RouterLink } from "vue-router";
import { NButton, NResult } from "naive-ui";
</script>

<template>
  <div class="not-found-page">
    <NResult
      status="404"
      title="Page not found"
      description="This management page does not exist."
    >
      <template #footer>
        <div class="not-found-actions">
          <RouterLink to="/overview" custom>
            <template #default="{ navigate }">
              <NButton type="primary" @click="navigate">Go to overview</NButton>
            </template>
          </RouterLink>
          <RouterLink to="/monitor/traffic" custom>
            <template #default="{ navigate }">
              <NButton secondary @click="navigate">Open monitor</NButton>
            </template>
          </RouterLink>
        </div>
      </template>
    </NResult>
  </div>
</template>

<style scoped>
.not-found-page {
  display: grid;
  min-height: min(34rem, 70vh);
  place-items: center;
  padding: 2rem 1rem;
}

.not-found-actions {
  display: flex;
  flex-wrap: wrap;
  justify-content: center;
  gap: 0.75rem;
}
</style>
