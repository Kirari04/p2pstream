<script setup lang="ts">
import { NButton, NIcon } from "naive-ui";
import { Monitor, Moon, Sun } from "@lucide/vue";
import { useThemeMode } from "@/composables/useThemeMode";

const { themeMode, setThemeMode } = useThemeMode();

const themeOptions = [
  { mode: "system", label: "System theme", icon: Monitor },
  { mode: "light", label: "Light theme", icon: Sun },
  { mode: "dark", label: "Dark theme", icon: Moon },
] as const;
</script>

<template>
  <div class="theme-toggle" role="group" aria-label="Color theme">
    <NButton
      v-for="option in themeOptions"
      :key="option.mode"
      size="small"
      :type="themeMode === option.mode ? 'primary' : 'default'"
      :secondary="themeMode === option.mode"
      :quaternary="themeMode !== option.mode"
      :aria-label="option.label"
      :aria-pressed="themeMode === option.mode"
      :title="option.label"
      @click="setThemeMode(option.mode)"
    >
      <template #icon>
        <NIcon :component="option.icon" aria-hidden="true" />
      </template>
    </NButton>
  </div>
</template>

<style scoped>
.theme-toggle {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  padding: 2px;
  border: 1px solid var(--app-border);
  border-radius: 8px;
  background: var(--app-panel-muted);
}

.theme-toggle :deep(.n-button) {
  min-width: 1.75rem;
  padding-inline: 0.375rem;
  border-radius: 6px;
}

@media (max-width: 639px), (pointer: coarse) {
  .theme-toggle :deep(.n-button) {
    min-width: 2.75rem;
    min-height: 2.75rem;
  }
}
</style>
