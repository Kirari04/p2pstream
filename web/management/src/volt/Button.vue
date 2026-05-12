<template>
    <Button
        unstyled
        :pt="theme"
        :ptOptions="{
            mergeProps: ptViewMerge
        }"
    >
        <template v-for="(_, slotName) in $slots" #[slotName]="slotProps">
            <slot :name="slotName" v-bind="slotProps ?? {}" />
        </template>
    </Button>
</template>

<script setup lang="ts">
import Button, { type ButtonPassThroughOptions, type ButtonProps } from 'primevue/button';
import { ref } from 'vue';
import { ptViewMerge } from './utils';

interface Props extends /* @vue-ignore */ ButtonProps {}
defineProps<Props>();

const theme = ref<ButtonPassThroughOptions>({
    root: `inline-flex cursor-pointer select-none items-center justify-center overflow-hidden relative
        px-3 py-2 gap-2 rounded-md disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-45 disabled:saturate-0 disabled:shadow-none transition-colors duration-200
        bg-white text-black border border-white
        enabled:hover:bg-[#e4e4e7] enabled:hover:border-[#e4e4e7]
        enabled:active:bg-[#d4d4d8] enabled:active:border-[#d4d4d8]
        focus-visible:outline focus-visible:outline-1 focus-visible:outline-offset-2 focus-visible:outline-white
        p-vertical:flex-col p-fluid:w-full p-fluid:p-icon-only:w-10
        p-icon-only:w-10 p-icon-only:px-0 p-icon-only:gap-0
        p-icon-only:p-rounded:rounded-full p-icon-only:p-rounded:h-10
        p-small:text-sm p-small:px-[0.625rem] p-small:py-[0.375rem]
        p-large:text-[1.125rem] p-large:px-[0.875rem] p-large:py-[0.625rem]
        p-raised:shadow-sm p-rounded:rounded-[2rem]
        p-outlined:bg-transparent p-outlined:border-[#333] p-outlined:text-white
        enabled:hover:p-outlined:bg-[#111] enabled:hover:p-outlined:border-[#555]
        enabled:active:p-outlined:bg-[#1a1a1a] enabled:active:p-outlined:border-[#666]
        p-text:bg-transparent p-text:border-transparent p-text:text-white
        enabled:hover:p-text:bg-[#111] enabled:hover:p-text:border-transparent
        enabled:active:p-text:bg-[#1a1a1a] enabled:active:p-text:border-transparent
    `,
    loadingIcon: `animate-spin`,
    icon: `p-right:order-1 p-bottom:order-2`,
    label: `font-medium p-icon-only:invisible p-icon-only:w-0
        p-small:text-sm p-large:text-[1.125rem]`,
    pcBadge: {
        root: `min-w-4 h-4 leading-4 bg-black rounded-full text-white text-xs font-bold`
    }
});
</script>
