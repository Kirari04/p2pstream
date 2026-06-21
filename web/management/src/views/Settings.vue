<script setup lang="ts">
import { useRoute } from "vue-router";

const route = useRoute();

function sectionClass(path: string): string {
  return route.path === path ? "accent-frame base-text" : "frame-transparent muted-text hover-base-text";
}
</script>

<template>
  <div class="stack-xl">
    <div class="layout-row layout-column space-lg mq-md-row mq-md-align-end mq-md-spread">
      <div>
        <h3 class="margin-bottom-sm copy-xl weight-bold">Settings</h3>
        <p class="copy-sm muted-text">Instance configuration, environment registry, and API access.</p>
      </div>
    </div>

    <nav class="layout-row space-sm scroll-x divider-bottom frame-standard" aria-label="Settings sections">
      <router-link
        to="/settings/environments"
        class="base-border-bottom pad-x-2xs pad-bottom-md copy-sm weight-medium layout-transition"
        :class="sectionClass('/settings/environments')"
      >
        Environments
      </router-link>
      <router-link
        to="/settings/api-tokens"
        class="base-border-bottom pad-x-2xs pad-bottom-md copy-sm weight-medium layout-transition"
        :class="sectionClass('/settings/api-tokens')"
      >
        API Tokens
      </router-link>
    </nav>

    <router-view />
  </div>
</template>
