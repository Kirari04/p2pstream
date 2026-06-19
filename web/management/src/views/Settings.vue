<script setup lang="ts">
import { useRoute } from "vue-router";

const route = useRoute();

function sectionClass(path: string): string {
  return route.path === path ? "border-[var(--app-accent)] text-[var(--app-text)]" : "border-transparent text-[var(--app-text-muted)] hover:text-[var(--app-text)]";
}
</script>

<template>
  <div class="space-y-8">
    <div class="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
      <div>
        <h3 class="mb-2 text-xl font-bold">Settings</h3>
        <p class="text-sm text-[var(--app-text-muted)]">Instance configuration, environment registry, and API access.</p>
      </div>
    </div>

    <nav class="flex gap-2 overflow-x-auto border-b border-[var(--app-border)]" aria-label="Settings sections">
      <router-link
        to="/settings/environments"
        class="border-b-2 px-1 pb-3 text-sm font-medium transition-colors"
        :class="sectionClass('/settings/environments')"
      >
        Environments
      </router-link>
      <router-link
        to="/settings/api-tokens"
        class="border-b-2 px-1 pb-3 text-sm font-medium transition-colors"
        :class="sectionClass('/settings/api-tokens')"
      >
        API Tokens
      </router-link>
    </nav>

    <router-view />
  </div>
</template>
