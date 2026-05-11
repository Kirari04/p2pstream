<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref, watch } from "vue";
import type { CSSProperties } from "vue";

type DisabledHintPlacement = "top" | "bottom" | "left" | "right";

const props = withDefaults(defineProps<{
  disabled?: boolean;
  reason?: string;
  placement?: DisabledHintPlacement;
  inline?: boolean;
  fullWidth?: boolean;
}>(), {
  placement: "top",
  inline: true,
  fullWidth: false,
});

let hideTouchTimer: number | undefined;
const wrapperRef = ref<HTMLElement | null>(null);
const popoverRef = ref<HTMLElement | null>(null);
const isVisible = ref(false);
const popoverStyle = ref<CSSProperties>({});
const hintId = `disabled-hint-${Math.random().toString(36).slice(2)}`;

const normalizedReason = computed(() => props.reason?.trim() ?? "");
const isActive = computed(() => Boolean(props.disabled && normalizedReason.value));
const wrapperClass = computed(() => ({
  "disabled-hint": true,
  "disabled-hint-inline": props.inline && !props.fullWidth,
  "disabled-hint-full": props.fullWidth,
  "disabled-hint-active": isActive.value,
}));

function showHint() {
  if (!isActive.value) return;
  isVisible.value = true;
  addGlobalListeners();
  void nextTick(updatePosition);
}

function hideHint() {
  isVisible.value = false;
  removeGlobalListeners();
  if (hideTouchTimer !== undefined) {
    window.clearTimeout(hideTouchTimer);
    hideTouchTimer = undefined;
  }
}

function handlePointerDown(event: PointerEvent) {
  if (!isActive.value || event.pointerType === "mouse") return;
  if (isVisible.value) {
    hideHint();
    return;
  }
  showHint();
  hideTouchTimer = window.setTimeout(hideHint, 2800);
}

function handleKeydown(event: KeyboardEvent) {
  if (event.key === "Escape") hideHint();
}

function handleDocumentPointerDown(event: PointerEvent) {
  const target = event.target as Node | null;
  if (!target) return;
  if (wrapperRef.value?.contains(target) || popoverRef.value?.contains(target)) return;
  hideHint();
}

function updatePosition() {
  if (!isVisible.value || !wrapperRef.value || !popoverRef.value) return;
  const trigger = wrapperRef.value.getBoundingClientRect();
  const popover = popoverRef.value.getBoundingClientRect();
  const gap = 10;
  const margin = 10;
  let top = 0;
  let left = 0;

  switch (props.placement) {
    case "bottom":
      top = trigger.bottom + gap;
      left = trigger.left + (trigger.width - popover.width) / 2;
      break;
    case "left":
      top = trigger.top + (trigger.height - popover.height) / 2;
      left = trigger.left - popover.width - gap;
      break;
    case "right":
      top = trigger.top + (trigger.height - popover.height) / 2;
      left = trigger.right + gap;
      break;
    default:
      top = trigger.top - popover.height - gap;
      left = trigger.left + (trigger.width - popover.width) / 2;
      break;
  }

  if (top < margin) top = trigger.bottom + gap;
  if (top + popover.height > window.innerHeight - margin) {
    top = Math.max(margin, trigger.top - popover.height - gap);
  }
  left = Math.min(
    Math.max(margin, left),
    Math.max(margin, window.innerWidth - popover.width - margin),
  );

  popoverStyle.value = {
    top: `${top}px`,
    left: `${left}px`,
  };
}

function addGlobalListeners() {
  window.addEventListener("resize", updatePosition);
  window.addEventListener("scroll", updatePosition, true);
  window.addEventListener("keydown", handleKeydown);
  document.addEventListener("pointerdown", handleDocumentPointerDown);
}

function removeGlobalListeners() {
  window.removeEventListener("resize", updatePosition);
  window.removeEventListener("scroll", updatePosition, true);
  window.removeEventListener("keydown", handleKeydown);
  document.removeEventListener("pointerdown", handleDocumentPointerDown);
}

onBeforeUnmount(() => {
  hideHint();
});

watch(isActive, (active) => {
  if (!active) hideHint();
});
</script>

<template>
  <span
    ref="wrapperRef"
    :class="wrapperClass"
    :tabindex="isActive ? 0 : undefined"
    :aria-disabled="isActive ? 'true' : undefined"
    :aria-describedby="isActive && isVisible ? hintId : undefined"
    @mouseenter="showHint"
    @mouseleave="hideHint"
    @focusin="showHint"
    @focusout="hideHint"
    @pointerdown="handlePointerDown"
  >
    <slot />
  </span>

  <Teleport to="body">
    <div
      v-if="isActive && isVisible"
      :id="hintId"
      ref="popoverRef"
      class="disabled-hint-popover"
      role="tooltip"
      :style="popoverStyle"
    >
      {{ normalizedReason }}
    </div>
  </Teleport>
</template>

<style scoped>
.disabled-hint {
  min-width: 0;
}

.disabled-hint-inline {
  display: inline-flex;
}

.disabled-hint-full {
  display: flex;
  width: 100%;
}

.disabled-hint-active {
  cursor: not-allowed;
}

.disabled-hint-active :deep(:disabled) {
  pointer-events: none;
}

.disabled-hint-active:focus-visible {
  border-radius: 6px;
  outline: 1px solid #777;
  outline-offset: 2px;
}

.disabled-hint-popover {
  position: fixed;
  z-index: 1400;
  max-width: 260px;
  border: 1px solid #333;
  border-radius: 6px;
  background: #050505;
  box-shadow: 0 16px 40px rgb(0 0 0 / 55%);
  color: #d4d4d8;
  font-size: 0.75rem;
  font-weight: 500;
  line-height: 1.45;
  padding: 0.55rem 0.65rem;
  pointer-events: none;
}
</style>
