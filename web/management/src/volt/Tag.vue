<template>
    <Tag
        unstyled
        :pt="theme"
        :ptOptions="{
            mergeProps: ptViewMerge
        }"
    >
        <template v-for="(_, slotName) in $slots" #[slotName]="slotProps">
            <slot :name="slotName" v-bind="slotProps ?? {}" />
        </template>
    </Tag>
</template>

<script setup lang="ts">
import Tag, { type TagPassThroughOptions, type TagProps } from 'primevue/tag';
import { ref } from 'vue';
import { ptViewMerge } from './utils';

interface Props extends /* @vue-ignore */ TagProps {}
defineProps<Props>();

const theme = ref<TagPassThroughOptions>({
    root: `inline-flex items-center justify-center text-sm font-bold py-1 px-2 rounded-md gap-1 p-rounded:rounded-2xl
        bg-[#111] border border-[#333] text-white
        p-success:text-green-400
        p-info:text-sky-400
        p-warn:text-amber-400
        p-danger:text-red-400
        p-secondary:text-[#888]
        p-contrast:bg-white p-contrast:text-black p-contrast:border-white`,
    icon: `text-xs w-3 h-3`
});
</script>
