<script setup lang="ts">
import { watch, onMounted, onUnmounted } from 'vue';
import TimesIcon from '@primevue/icons/times';

const props = defineProps<{
  modelValue: boolean;
  title: string;
  maxWidth?: string;
}>();

const emit = defineEmits<{
  (e: 'update:modelValue', value: boolean): void;
}>();

function close() {
  emit('update:modelValue', false);
}

function handleKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape' && props.modelValue) {
    close();
  }
}

onMounted(() => {
  document.addEventListener('keydown', handleKeydown);
});

onUnmounted(() => {
  document.removeEventListener('keydown', handleKeydown);
});

watch(() => props.modelValue, (isOpen) => {
  if (isOpen) {
    document.body.style.overflow = 'hidden';
  } else {
    document.body.style.overflow = '';
  }
});
</script>

<template>
  <Teleport to="body">
    <Transition name="modal">
      <div v-if="modelValue" class="fixed inset-0 z-50 flex items-center justify-center p-4 sm:p-6" @click="close">
        <div class="fixed inset-0 bg-black/60 backdrop-blur-sm transition-opacity" aria-hidden="true"></div>
        
        <div 
          class="vercel-card relative z-10 w-full max-h-[90vh] overflow-hidden flex flex-col bg-[#0a0a0a] shadow-2xl transition-all"
          :style="{ maxWidth: maxWidth || '42rem' }"
          @click.stop
        >
          <div class="flex items-center justify-between border-b border-[#333] px-5 py-4 bg-black">
            <h3 class="text-sm font-semibold uppercase tracking-widest text-[#888]">{{ title }}</h3>
            <button 
              type="button" 
              class="rounded-md p-1.5 text-[#888] hover:bg-[#1f1f1f] hover:text-white transition"
              @click="close"
            >
              <TimesIcon class="h-4 w-4" />
            </button>
          </div>
          
          <div class="overflow-y-auto p-5">
            <slot />
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
.modal-enter-active,
.modal-leave-active {
  transition: opacity 0.2s ease, transform 0.2s ease;
}

.modal-enter-from,
.modal-leave-to {
  opacity: 0;
}

.modal-enter-from .vercel-card,
.modal-leave-to .vercel-card {
  transform: scale(0.98) translateY(10px);
}
</style>