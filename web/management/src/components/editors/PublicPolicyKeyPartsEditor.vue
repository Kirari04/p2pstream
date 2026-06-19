<script setup lang="ts">
import { Trash2 as TrashIcon } from "@lucide/vue";
import DisabledHint from "@/components/DisabledHint.vue";
import DangerButton from "@/components/ui/DangerButton.vue";
import SecondaryButton from "@/components/ui/SecondaryButton.vue";
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
  <section class="grid gap-4 rounded-md border border-[#222] bg-[#050505] p-4">
    <div class="flex items-center justify-between gap-3">
      <h4 class="text-sm font-semibold text-white">Key parts</h4>
      <DisabledHint :disabled="Boolean(disabledReason)" :reason="disabledReason || ''">
        <SecondaryButton type="button" size="small" label="Add Key" :disabled="Boolean(disabledReason)" @click="addKeyPart" />
      </DisabledHint>
    </div>
    <div class="grid gap-2">
      <div v-for="(part, index) in keyParts" :key="index" class="grid gap-2 sm:grid-cols-[1fr_1fr_auto]">
        <select v-model="part.source" class="app-control text-sm" :disabled="Boolean(disabledReason)">
          <option v-for="option in keySourceOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
        </select>
        <DisabledHint full-width :disabled="Boolean(disabledReason || keyPartNameDisabledReason(part.source))" :reason="disabledReason || keyPartNameDisabledReason(part.source)">
          <input
            v-model="part.name"
            class="app-control text-sm"
            placeholder="Name"
            :disabled="Boolean(disabledReason || keyPartNameDisabledReason(part.source))"
          />
        </DisabledHint>
        <DisabledHint :disabled="Boolean(removeDisabledReason())" :reason="removeDisabledReason()">
          <DangerButton
            size="small"
            class="row-remove-button"
            aria-label="Remove key part"
            title="Remove key part"
            type="button"
            :disabled="Boolean(removeDisabledReason())"
            @click="removeKeyPart(index)"
          >
            <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
          </DangerButton>
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
