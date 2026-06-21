<script setup lang="ts">
import { Trash2 as TrashIcon } from "@lucide/vue";
import { NButton, NInput, NSelect } from "naive-ui";
import DisabledHint from "@/components/DisabledHint.vue";
import { PublicRateLimitKeySource } from "@/gen/proto/p2pstream/v1/management_pb";

type KeyPartForm = {
  source: PublicRateLimitKeySource;
  name: string;
};

const props = defineProps<{
  keyParts: KeyPartForm[];
  disabledReason?: string;
}>();

const keySourceOptions = [
  { label: "Remote IP", value: PublicRateLimitKeySource.REMOTE_IP },
  { label: "Host", value: PublicRateLimitKeySource.HOST },
  { label: "Method", value: PublicRateLimitKeySource.METHOD },
  { label: "Path", value: PublicRateLimitKeySource.PATH },
  { label: "Protocol", value: PublicRateLimitKeySource.PROTOCOL },
  { label: "Header", value: PublicRateLimitKeySource.HEADER },
  { label: "Cookie", value: PublicRateLimitKeySource.COOKIE },
  { label: "Query param", value: PublicRateLimitKeySource.QUERY_PARAM },
];

function addKeyPart() {
  props.keyParts.push({ source: PublicRateLimitKeySource.REMOTE_IP, name: "" });
}

function removeKeyPart(index: number) {
  props.keyParts.splice(index, 1);
  if (!props.keyParts.length) addKeyPart();
}

function keyPartNeedsName(source: PublicRateLimitKeySource): boolean {
  return source === PublicRateLimitKeySource.HEADER ||
    source === PublicRateLimitKeySource.COOKIE ||
    source === PublicRateLimitKeySource.QUERY_PARAM;
}

function keyPartNameDisabledReason(source: PublicRateLimitKeySource): string {
  return keyPartNeedsName(source) ? "" : "This key source does not need a name.";
}

function removeDisabledReason(): string {
  if (props.disabledReason) return props.disabledReason;
  return props.keyParts.length <= 1 ? "At least one key part is required." : "";
}
</script>

<template>
  <section class="layout-grid space-lg round-md framed frame-standard muted-bg pad-lg">
    <div class="layout-row align-center spread-items space-md">
      <h4 class="copy-sm weight-semibold base-text">Key parts</h4>
      <DisabledHint :disabled="Boolean(disabledReason)" :reason="disabledReason || ''">
        <NButton secondary size="small" :disabled="Boolean(disabledReason)" @click="addKeyPart">
          Add Key
        </NButton>
      </DisabledHint>
    </div>
    <div class="layout-grid space-sm">
      <div v-for="(part, index) in keyParts" :key="index" class="layout-grid space-sm mq-sm-two-auto">
        <NSelect v-model:value="part.source" size="small" :options="keySourceOptions" :disabled="Boolean(disabledReason)" />
        <DisabledHint full-width :disabled="Boolean(disabledReason || keyPartNameDisabledReason(part.source))" :reason="disabledReason || keyPartNameDisabledReason(part.source)">
          <NInput
            v-model:value="part.name"
            size="small"
            placeholder="Name"
            :disabled="Boolean(disabledReason || keyPartNameDisabledReason(part.source))"
          />
        </DisabledHint>
        <DisabledHint :disabled="Boolean(removeDisabledReason())" :reason="removeDisabledReason()">
          <NButton
            type="error"
            size="small"
            class="row-remove-button"
            aria-label="Remove key part"
            title="Remove key part"
            :disabled="Boolean(removeDisabledReason())"
            @click="removeKeyPart(index)"
          >
            <template #icon><TrashIcon class="icon-sm icon-sm" /></template>
          </NButton>
        </DisabledHint>
      </div>
    </div>
  </section>
</template>

<style scoped>
.row-remove-button {
  width: 2.25rem;
  height: 2.25rem;
  padding: 0 !important;
}
</style>
