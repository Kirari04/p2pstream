<script setup lang="ts">
import { computed } from "vue";
import { RouterView, useRoute, useRouter } from "vue-router";
import { NTab, NTabs } from "naive-ui";

const route = useRoute();
const router = useRouter();

const monitorSections = [
  {
    key: "traffic",
    label: "Traffic Flow",
    path: "/monitor/traffic",
  },
  {
    key: "diagnostics",
    label: "Diagnostics",
    path: "/monitor/diagnostics",
  },
] as const;

type MonitorSectionKey = typeof monitorSections[number]["key"];

const activeSection = computed<MonitorSectionKey>(() =>
  route.path.includes("/diagnostics") ? "diagnostics" : "traffic",
);

async function selectSection(value: string | number) {
  const section = monitorSections.find((item) => item.key === value);
  if (!section || section.key === activeSection.value) return;
  await router.push(section.path);
}
</script>

<template>
  <div class="stack-xl monitor-page">
    <NTabs
      class="monitor-tabs"
      type="line"
      animated
      :value="activeSection"
      @update:value="selectSection"
    >
      <NTab
        v-for="section in monitorSections"
        :key="section.key"
        :name="section.key"
        :tab="section.label"
      />
    </NTabs>

    <RouterView />
  </div>
</template>

<style scoped>
.monitor-page {
  gap: 1.25rem;
}

.monitor-tabs {
  min-width: 0;
}

.monitor-tabs :deep(.n-tabs-nav) {
  margin-bottom: 0;
}
</style>
