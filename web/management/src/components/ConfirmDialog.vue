<script setup lang="ts">
import Modal from "@/volt/Modal.vue";
import SecondaryButton from "@/volt/SecondaryButton.vue";
import DangerButton from "@/volt/DangerButton.vue";
import type { ConfirmDialogState } from "@/composables/useConfirmDialog";

defineProps<{
  state: ConfirmDialogState;
}>();

defineEmits<{
  (event: "confirm"): void;
  (event: "cancel"): void;
}>();
</script>

<template>
  <Modal :model-value="state.open" :title="state.title" max-width="28rem" @update:model-value="$emit('cancel')">
    <div class="grid gap-5">
      <p class="text-sm leading-6 text-[#888]">{{ state.description }}</p>
      <div class="flex justify-end gap-3">
        <SecondaryButton type="button" label="Cancel" @click="$emit('cancel')" />
        <DangerButton type="button" :label="state.confirmLabel" @click="$emit('confirm')" />
      </div>
    </div>
  </Modal>
</template>
