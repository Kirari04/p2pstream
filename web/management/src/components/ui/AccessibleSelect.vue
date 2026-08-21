<script setup lang="ts">
import { NSelect, type SelectNodeProps } from "naive-ui";
import { computed, nextTick, onBeforeUnmount, onMounted, onUpdated, ref, useId } from "vue";
import type { HTMLAttributes, InputHTMLAttributes } from "vue";

defineOptions({ inheritAttrs: false });

type SelectValue = string | number | Array<string | number> | null;

const props = defineProps<{
  accessibleLabel: string;
  value?: SelectValue;
  inputProps?: InputHTMLAttributes;
  menuProps?: HTMLAttributes;
  nodeProps?: SelectNodeProps;
}>();

const emit = defineEmits<{
  "update:value": [value: any, option?: any];
  "update:show": [show: boolean];
}>();

const selectRef = ref<InstanceType<typeof NSelect> | null>(null);
const open = ref(false);
const instanceId = useId().replaceAll(":", "");
const menuId = computed(() => String(props.menuProps?.id ?? `accessible-select-${instanceId}-menu`));
let observedMenu: HTMLElement | null = null;
let menuObserver: MutationObserver | null = null;

const mergedInputProps = computed<InputHTMLAttributes>(() => ({
  ...props.inputProps,
  role: "combobox",
  "aria-label": props.inputProps?.["aria-label"] ?? props.accessibleLabel,
  "aria-controls": menuId.value,
  "aria-expanded": open.value,
  "aria-haspopup": "listbox",
  "aria-autocomplete": "list",
}));

const mergedMenuProps = computed<HTMLAttributes>(() => ({
  ...props.menuProps,
  id: menuId.value,
  role: "listbox",
  "aria-label": props.menuProps?.["aria-label"] ?? `${props.accessibleLabel} options`,
}));

const mergedNodeProps: SelectNodeProps = (option) => {
  const inherited = props.nodeProps?.(option) ?? {};
  if (option.type === "group") {
    return { ...inherited, role: "presentation" } as HTMLAttributes & Record<string, unknown>;
  }

  const selected = Array.isArray(props.value)
    ? props.value.includes(option.value as string | number)
    : props.value === option.value;

  return {
    ...inherited,
    id: inherited.id ?? optionId(option.value),
    role: "option",
    "aria-selected": selected,
    "aria-disabled": option.disabled || undefined,
  } as HTMLAttributes & Record<string, unknown>;
};

function optionId(value: unknown): string {
  const source = String(value ?? "option");
  let hash = 0x811c9dc5;
  for (let index = 0; index < source.length; index += 1) {
    hash ^= source.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return `${menuId.value}-option-${(hash >>> 0).toString(36)}`;
}

function selectRoot(): HTMLElement | undefined {
  return (selectRef.value as (InstanceType<typeof NSelect> & { $el?: HTMLElement }) | null)?.$el as HTMLElement | undefined;
}

function syncActiveDescendant(root = selectRoot()) {
  const trigger = root?.querySelector<HTMLElement>(".n-base-selection-label, .n-base-selection-tags");
  const filterInput = trigger?.querySelector<HTMLInputElement>(
    "input.n-base-selection-input, input.n-base-selection-input-tag__input",
  );
  const combobox = filterInput ?? trigger;
  if (!combobox) return;

  if (!open.value) {
    combobox.removeAttribute("aria-activedescendant");
    return;
  }

  const pendingOption = document.getElementById(menuId.value)?.querySelector<HTMLElement>(
    ".n-base-select-option--pending[id]",
  );
  const pendingId = pendingOption?.id;
  if (pendingId) {
    combobox.setAttribute("aria-activedescendant", pendingId);
  } else {
    combobox.removeAttribute("aria-activedescendant");
  }
}

function syncMenuObserver() {
  const menu = open.value ? document.getElementById(menuId.value) : null;
  if (menu !== observedMenu) {
    menuObserver?.disconnect();
    observedMenu = menu;
    if (menu) {
      menuObserver = new MutationObserver(() => syncActiveDescendant());
      menuObserver.observe(menu, {
        attributes: true,
        attributeFilter: ["class"],
        childList: true,
        subtree: true,
      });
    } else {
      menuObserver = null;
    }
  }
  syncActiveDescendant();
}

function syncTriggerSemantics() {
  // Naive UI puts keyboard focus on an internal div without exposing its select
  // semantics. Keep that actual focus target named until the upstream component
  // supplies the ARIA combobox contract itself.
  const root = selectRoot();
  const trigger = root?.querySelector<HTMLElement>(".n-base-selection-label, .n-base-selection-tags");
  if (!trigger) return;

  const filterInput = trigger.querySelector<HTMLInputElement>(
    "input.n-base-selection-input, input.n-base-selection-input-tag__input",
  );
  if (filterInput) {
    // Filterable selects already contain the correct combobox element, but Naive
    // UI leaves an unnamed wrapper in the tab order. Make the named input the
    // single keyboard target instead of exposing nested comboboxes.
    trigger.removeAttribute("role");
    trigger.removeAttribute("aria-label");
    trigger.removeAttribute("aria-haspopup");
    trigger.removeAttribute("aria-expanded");
    trigger.removeAttribute("aria-controls");
    trigger.tabIndex = -1;
    filterInput.tabIndex = filterInput.disabled ? -1 : 0;
    syncActiveDescendant(root);
    return;
  }

  trigger.setAttribute("role", "combobox");
  trigger.setAttribute("aria-label", props.accessibleLabel);
  trigger.setAttribute("aria-haspopup", "listbox");
  trigger.setAttribute("aria-expanded", String(open.value));
  trigger.setAttribute("aria-controls", menuId.value);
  syncActiveDescendant(root);
}

function scheduleSemanticsSync() {
  void nextTick(() => window.requestAnimationFrame(() => {
    syncTriggerSemantics();
    syncMenuObserver();
  }));
}

function handleValueUpdate(value: any, option?: any) {
  emit("update:value", value, option);
  scheduleSemanticsSync();
}

function handleShowUpdate(show: boolean) {
  open.value = show;
  emit("update:show", show);
  scheduleSemanticsSync();
}

onMounted(scheduleSemanticsSync);
onUpdated(scheduleSemanticsSync);
onBeforeUnmount(() => menuObserver?.disconnect());
</script>

<template>
  <NSelect
    ref="selectRef"
    v-bind="$attrs"
    :value="value"
    :input-props="mergedInputProps"
    :menu-props="mergedMenuProps"
    :node-props="mergedNodeProps"
    @focus="scheduleSemanticsSync"
    @blur="scheduleSemanticsSync"
    @update:value="handleValueUpdate"
    @update:show="handleShowUpdate"
  />
</template>
