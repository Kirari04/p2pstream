<script setup lang="ts">
import { computed } from "vue";
import { NButton, NIcon } from "naive-ui";
import { Moon, Sun } from "@lucide/vue";
import { useThemeMode } from "@/composables/useThemeMode";

const { isDarkTheme, toggleTheme } = useThemeMode();
const label = computed(() => isDarkTheme.value ? "Switch to light theme" : "Switch to dark theme");
</script>

<template>
  <NButton quaternary circle size="small" :aria-label="label" :title="label" @click="toggleTheme">
    <template #icon>
      <NIcon :component="isDarkTheme ? Sun : Moon" />
    </template>
  </NButton>
</template>
