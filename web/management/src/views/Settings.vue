<script setup lang="ts">
import { computed } from "vue";
import { NTab, NTabs } from "naive-ui";
import { RouterView, useRoute, useRouter } from "vue-router";

const route = useRoute();
const router = useRouter();

const settingsSections = [
  { key: "environments", label: "Environments", path: "/settings/environments" },
  { key: "api-tokens", label: "API Tokens", path: "/settings/api-tokens" },
  { key: "management-tls", label: "Management TLS", path: "/settings/management-tls" },
] as const;

type SettingsSectionKey = typeof settingsSections[number]["key"];

const activeSection = computed<SettingsSectionKey>(() =>
  route.path.includes("/management-tls")
    ? "management-tls"
    : route.path.includes("/api-tokens") ? "api-tokens" : "environments",
);

async function selectSection(value: string | number) {
  const section = settingsSections.find((item) => item.key === value);
  if (!section || section.key === activeSection.value) return;
  await router.push(section.path);
}
</script>

<template>
  <div class="stack-xl settings-page">
    <div class="layout-row layout-column space-lg mq-md-row mq-md-align-end mq-md-spread">
      <div>
        <h1 class="margin-bottom-sm copy-xl weight-bold">Settings</h1>
        <p class="copy-sm muted-text">Instance configuration, environment registry, and API access.</p>
      </div>
    </div>

    <NTabs
      class="settings-tabs"
      type="line"
      :value="activeSection"
      aria-label="Settings sections"
      @update:value="selectSection"
    >
      <NTab
        v-for="section in settingsSections"
        :key="section.key"
        :name="section.key"
        :tab="section.label"
      />
    </NTabs>

    <RouterView />
  </div>
</template>

<style scoped>
.settings-page {
  gap: 1.25rem;
}

.settings-page > * + * {
  margin-top: 1.25rem;
}

.settings-tabs {
  min-width: 0;
}

.settings-tabs :deep(.n-tabs-nav) {
  margin-bottom: 0;
}

.settings-tabs :deep(.n-tabs-tab) {
  padding: 0.625rem 0.125rem 0.75rem;
  font-size: 0.875rem;
}

.settings-tabs :deep(.n-tabs-tab + .n-tabs-tab) {
  margin-left: 1.25rem;
}

@media (pointer: coarse) {
  .settings-tabs :deep(.n-tabs-tab) {
    min-height: 2.75rem;
  }
}
</style>
