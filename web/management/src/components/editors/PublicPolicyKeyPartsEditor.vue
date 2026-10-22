<script setup lang="ts">
import { Trash2 as TrashIcon } from "@lucide/vue";
import { NButton, NInput } from "naive-ui";
import DisabledHint from "@/components/DisabledHint.vue";
import AccessibleSelect from "@/components/ui/AccessibleSelect.vue";
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
  <section class="editor-group layout-grid space-lg">
    <div class="layout-row align-center spread-items space-md">
      <div class="layout-grid space-2xs min-width-zero">
        <h3 class="copy-sm weight-semibold base-text">Key parts</h3>
        <p v-if="disabledReason" class="copy-xs line-normal muted-text">{{ disabledReason }}</p>
      </div>
      <DisabledHint :disabled="Boolean(disabledReason)" :reason="disabledReason || ''">
        <NButton secondary size="small" attr-type="button" :disabled="Boolean(disabledReason)" @click="addKeyPart">
          Add key
        </NButton>
      </DisabledHint>
    </div>
    <div class="layout-grid space-sm">
      <div v-for="(part, index) in keyParts" :key="index" class="layout-grid space-sm mq-sm-two-auto">
        <AccessibleSelect
          v-model:value="part.source"
          :accessible-label="`Key part ${index + 1} source`"
          size="small"
          :options="keySourceOptions"
          :disabled="Boolean(disabledReason)"
        />
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
            secondary
            size="small"
            attr-type="button"
            class="row-remove-button"
            aria-label="Remove key part"
            title="Remove key part"
            :disabled="Boolean(removeDisabledReason())"
            @click="removeKeyPart(index)"
          >
            <template #icon><TrashIcon class="icon-sm" /></template>
            Remove
          </NButton>
        </DisabledHint>
      </div>
    </div>
  </section>
</template>

<style scoped>
/* Field labels stack from the top so helper text in a sibling column never stretches the input row. */
label.layout-grid {
  align-content: start;
}

/* Segmented controls keep their natural width inside stacked field groups. */
.segmented {
  justify-self: start;
}

:deep(.disabled-hint-full > .n-input-number) {
  flex: 1 1 auto;
}

</style>
