<template>
    <Toast
        unstyled
        :pt="theme"
        :ptOptions="{
            mergeProps: ptViewMerge
        }"
    />
</template>

<script setup lang="ts">
import Toast, { type ToastPassThroughOptions } from 'primevue/toast';
import { ref } from 'vue';
import { ptViewMerge } from './utils';

const theme = ref<ToastPassThroughOptions>({
    root: `fixed z-[1500] w-96 max-w-[calc(100vw-2rem)]`,
    message: `flex items-start gap-3 my-2 p-4 rounded-md border border-[#333] bg-[#0a0a0a] shadow-2xl shadow-black/60
        p-info:border-sky-900/50
        p-success:border-green-900/50
        p-warn:border-amber-900/50
        p-error:border-red-900/50`,
    messageContent: `flex-1 min-w-0`,
    messageText: `text-sm text-[#ededed]`,
    summary: `font-semibold text-sm text-white`,
    detail: `mt-1 text-xs text-[#888]`,
    messageIcon: `flex-shrink-0 w-5 h-5 mt-0.5
        p-info:text-sky-400
        p-success:text-green-400
        p-warn:text-amber-400
        p-error:text-red-400`,
    closeButton: `flex-shrink-0 rounded-md p-1 text-[#888] hover:text-white hover:bg-[#1f1f1f] transition`,
    closeIcon: `w-3.5 h-3.5`,
    transition: {
        enterFromClass: 'opacity-0 translate-x-4',
        enterActiveClass: 'transition duration-200 ease-out',
        leaveActiveClass: 'transition duration-150 ease-in',
        leaveToClass: 'opacity-0 translate-x-4',
    },
});
</script>
