<script setup lang="ts">
import { computed } from "vue";
import {
  darkTheme,
  NConfigProvider,
  NDialogProvider,
  NGlobalStyle,
  NMessageProvider,
  NNotificationProvider,
} from "naive-ui";
import ManagementApp from "./ManagementApp.vue";
import { useThemeMode } from "@/composables/useThemeMode";
import { darkThemeOverrides, lightThemeOverrides } from "@/theme/naive";

const { themeMode } = useThemeMode();
const activeTheme = computed(() => themeMode.value === "dark" ? darkTheme : undefined);
const activeThemeOverrides = computed(() => themeMode.value === "dark" ? darkThemeOverrides : lightThemeOverrides);
</script>

<template>
  <NConfigProvider :theme="activeTheme" :theme-overrides="activeThemeOverrides">
    <NGlobalStyle />
    <NDialogProvider>
      <NMessageProvider placement="top-right">
        <NNotificationProvider placement="top-right">
          <ManagementApp />
        </NNotificationProvider>
      </NMessageProvider>
    </NDialogProvider>
  </NConfigProvider>
</template>
