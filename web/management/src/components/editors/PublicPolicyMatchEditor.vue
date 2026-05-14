<script setup lang="ts">
import { ref } from "vue";
import PlusIcon from "@primevue/icons/plus";
import TrashIcon from "@primevue/icons/trash";
import DisabledHint from "@/components/DisabledHint.vue";
import DangerButton from "@/volt/DangerButton.vue";
import { PublicListenerProtocol, PublicRateLimitMatchOperator } from "@/gen/proto/p2pstream/v1/management_pb";

type MatcherForm = {
  name: string;
  operator: PublicRateLimitMatchOperator;
  value: string;
};
type MatcherGroupKey = "headers" | "cookies" | "queryParams";
type MatchForm = {
  methods: string[];
  protocols: PublicListenerProtocol[];
  hostPatternsText: string;
  pathPrefixesText: string;
  headers: MatcherForm[];
  cookies: MatcherForm[];
  queryParams: MatcherForm[];
};

const props = defineProps<{
  form: MatchForm;
}>();

const activeMatcherGroup = ref<MatcherGroupKey>("headers");
const methodOptions = ["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"];
const matcherOperatorOptions = [
  { label: "Present", value: PublicRateLimitMatchOperator.PRESENT },
  { label: "Equals", value: PublicRateLimitMatchOperator.EQUALS },
  { label: "Prefix", value: PublicRateLimitMatchOperator.PREFIX },
  { label: "Suffix", value: PublicRateLimitMatchOperator.SUFFIX },
  { label: "Contains", value: PublicRateLimitMatchOperator.CONTAINS },
];
const matcherGroups = [
  { key: "headers", label: "Headers", singular: "header", namePlaceholder: "Header" },
  { key: "cookies", label: "Cookies", singular: "cookie", namePlaceholder: "Cookie" },
  { key: "queryParams", label: "Query params", singular: "query param", namePlaceholder: "Param" },
] as const;

function toggleMethod(method: string) {
  props.form.methods = props.form.methods.includes(method)
    ? props.form.methods.filter((item) => item !== method)
    : [...props.form.methods, method];
}

function toggleProtocol(protocol: PublicListenerProtocol) {
  props.form.protocols = props.form.protocols.includes(protocol)
    ? props.form.protocols.filter((item) => item !== protocol)
    : [...props.form.protocols, protocol];
}

function matchersForGroup(group: MatcherGroupKey): MatcherForm[] {
  if (group === "cookies") return props.form.cookies;
  if (group === "queryParams") return props.form.queryParams;
  return props.form.headers;
}

function activeMatcherGroupConfig() {
  return matcherGroups.find((group) => group.key === activeMatcherGroup.value) ?? matcherGroups[0];
}

function activeMatchers(): MatcherForm[] {
  return matchersForGroup(activeMatcherGroup.value);
}

function matcherCount(group: MatcherGroupKey): number {
  return matchersForGroup(group).length;
}

function addActiveMatcher() {
  activeMatchers().push({ name: "", operator: PublicRateLimitMatchOperator.PRESENT, value: "" });
}

function removeActiveMatcher(index: number) {
  activeMatchers().splice(index, 1);
}

function matcherValueDisabledReason(matcher: MatcherForm): string {
  return matcher.operator === PublicRateLimitMatchOperator.PRESENT
    ? "Present only checks that the value exists, so no comparison value is used."
    : "";
}

defineExpose({
  setInitialTab() {
    activeMatcherGroup.value =
      props.form.headers.length ? "headers" :
        props.form.cookies.length ? "cookies" :
          props.form.queryParams.length ? "queryParams" :
            "headers";
  },
});
</script>

<template>
  <section class="grid gap-4 rounded-md border border-[#222] bg-[#050505] p-4">
    <h4 class="text-sm font-semibold text-white">Match</h4>
    <div class="grid gap-4 lg:grid-cols-2">
      <div class="grid gap-2">
        <span class="text-xs font-medium uppercase tracking-wider text-[#888]">Methods</span>
        <div class="flex flex-wrap gap-2">
          <button
            v-for="method in methodOptions"
            :key="method"
            type="button"
            class="rounded border px-2.5 py-1 text-xs font-medium transition"
            :class="form.methods.includes(method) ? 'border-white bg-white text-black' : 'border-[#333] bg-black text-[#d4d4d8] hover:border-[#666]'"
            @click="toggleMethod(method)"
          >
            {{ method }}
          </button>
        </div>
      </div>
      <div class="grid gap-2">
        <span class="text-xs font-medium uppercase tracking-wider text-[#888]">Protocols</span>
        <div class="flex flex-wrap gap-2">
          <button
            type="button"
            class="rounded border px-2.5 py-1 text-xs font-medium transition"
            :class="form.protocols.includes(PublicListenerProtocol.HTTP) ? 'border-white bg-white text-black' : 'border-[#333] bg-black text-[#d4d4d8] hover:border-[#666]'"
            @click="toggleProtocol(PublicListenerProtocol.HTTP)"
          >
            HTTP
          </button>
          <button
            type="button"
            class="rounded border px-2.5 py-1 text-xs font-medium transition"
            :class="form.protocols.includes(PublicListenerProtocol.HTTPS) ? 'border-white bg-white text-black' : 'border-[#333] bg-black text-[#d4d4d8] hover:border-[#666]'"
            @click="toggleProtocol(PublicListenerProtocol.HTTPS)"
          >
            HTTPS
          </button>
        </div>
      </div>
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
        Host patterns
        <textarea v-model="form.hostPatternsText" class="vercel-input min-h-20 text-sm normal-case tracking-normal" placeholder="api.example.com&#10;*.example.com" />
      </label>
      <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
        Path prefixes
        <textarea v-model="form.pathPrefixesText" class="vercel-input min-h-20 text-sm normal-case tracking-normal" placeholder="/api&#10;/login" />
      </label>
    </div>

    <div class="matcher-editor">
      <div class="matcher-editor-header">
        <div>
          <p class="matcher-eyebrow">Request attributes</p>
          <h5 class="matcher-heading">{{ activeMatcherGroupConfig().label }}</h5>
        </div>
        <button type="button" class="matcher-add-button" @click="addActiveMatcher">
          <PlusIcon class="h-3.5 w-3.5" />
          <span>Add {{ activeMatcherGroupConfig().singular }}</span>
        </button>
      </div>

      <div class="matcher-tabs" role="tablist" aria-label="Matcher type">
        <button
          v-for="group in matcherGroups"
          :key="group.key"
          type="button"
          role="tab"
          class="matcher-tab"
          :class="{ 'matcher-tab-active': activeMatcherGroup === group.key }"
          :aria-selected="activeMatcherGroup === group.key"
          @click="activeMatcherGroup = group.key"
        >
          <span>{{ group.label }}</span>
          <span class="matcher-tab-count">{{ matcherCount(group.key) }}</span>
        </button>
      </div>

      <div class="matcher-list-shell">
        <div v-if="!activeMatchers().length" class="matcher-empty">
          <p>No {{ activeMatcherGroupConfig().singular }} matchers configured.</p>
          <button type="button" @click="addActiveMatcher">
            <PlusIcon class="h-3.5 w-3.5" />
            <span>Add {{ activeMatcherGroupConfig().singular }}</span>
          </button>
        </div>
        <div v-else class="matcher-list">
          <div class="matcher-row matcher-row-head" aria-hidden="true">
            <span>Name</span>
            <span>Operator</span>
            <span>Value</span>
            <span />
          </div>
          <div v-for="(matcher, index) in activeMatchers()" :key="`${activeMatcherGroup}-${index}`" class="matcher-row">
            <input v-model="matcher.name" class="vercel-input matcher-input" :placeholder="activeMatcherGroupConfig().namePlaceholder" />
            <select v-model="matcher.operator" class="vercel-input matcher-input">
              <option v-for="option in matcherOperatorOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
            </select>
            <DisabledHint full-width :disabled="Boolean(matcherValueDisabledReason(matcher))" :reason="matcherValueDisabledReason(matcher)">
              <input
                v-model="matcher.value"
                class="vercel-input matcher-input"
                :placeholder="matcher.operator === PublicRateLimitMatchOperator.PRESENT ? 'Ignored for Present' : 'Value'"
                :disabled="Boolean(matcherValueDisabledReason(matcher))"
              />
            </DisabledHint>
            <DangerButton
              size="small"
              class="row-remove-button"
              type="button"
              :aria-label="`Remove ${activeMatcherGroupConfig().singular} matcher`"
              :title="`Remove ${activeMatcherGroupConfig().singular} matcher`"
              @click="removeActiveMatcher(index)"
            >
              <template #icon><TrashIcon class="h-3.5 w-3.5" /></template>
            </DangerButton>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.matcher-editor {
  display: grid;
  gap: 0.85rem;
  min-width: 0;
  border: 1px solid #222;
  border-radius: 6px;
  background: #080808;
  padding: 0.85rem;
}

.matcher-editor-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
}

.matcher-eyebrow {
  color: #777;
  font-size: 0.68rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.matcher-heading {
  margin-top: 0.15rem;
  color: #fff;
  font-size: 0.92rem;
  font-weight: 650;
}

.matcher-tabs {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  overflow: hidden;
  border: 1px solid #333;
  border-radius: 6px;
  background: #050505;
  padding: 0.2rem;
}

.matcher-tab {
  display: flex;
  min-width: 0;
  height: 2.25rem;
  align-items: center;
  justify-content: center;
  gap: 0.45rem;
  border-radius: 4px;
  color: #a1a1aa;
  font-size: 0.78rem;
  font-weight: 650;
  transition: background 140ms ease, color 140ms ease;
}

.matcher-tab:hover {
  background: #141414;
  color: #fff;
}

.matcher-tab-active {
  background: #fff;
  color: #000;
}

.matcher-tab-count {
  min-width: 1.25rem;
  border-radius: 999px;
  background: rgb(255 255 255 / 10%);
  padding: 0.1rem 0.35rem;
  font-size: 0.68rem;
  line-height: 1.1;
  text-align: center;
}

.matcher-tab-active .matcher-tab-count {
  background: rgb(0 0 0 / 12%);
}

.matcher-list-shell {
  min-height: 13.5rem;
  max-height: 18rem;
  overflow-y: auto;
  overscroll-behavior: contain;
  border: 1px solid #222;
  border-radius: 6px;
  background: #030303;
}

.matcher-list {
  display: grid;
  gap: 0.45rem;
  padding: 0.6rem;
}

.matcher-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 9rem minmax(0, 1.15fr) 2.25rem;
  gap: 0.5rem;
  align-items: center;
  min-height: 2.5rem;
}

.matcher-row-head {
  min-height: 1.4rem;
  color: #666;
  font-size: 0.68rem;
  font-weight: 700;
  letter-spacing: 0.06em;
  text-transform: uppercase;
}

.matcher-input {
  min-width: 0;
  height: 2.25rem;
  font-size: 0.8rem;
  text-transform: none;
  letter-spacing: 0;
}

.matcher-add-button,
.matcher-empty button {
  display: inline-flex;
  height: 2rem;
  align-items: center;
  gap: 0.4rem;
  border: 1px solid #333;
  border-radius: 5px;
  background: #050505;
  color: #d4d4d8;
  padding: 0 0.65rem;
  font-size: 0.72rem;
  font-weight: 650;
  transition: border-color 140ms ease, color 140ms ease, background 140ms ease;
}

.matcher-add-button:hover,
.matcher-empty button:hover {
  border-color: #666;
  background: #0f0f0f;
  color: #fff;
}

.matcher-empty {
  display: grid;
  min-height: 13.5rem;
  place-items: center;
  align-content: center;
  gap: 0.75rem;
  color: #777;
  font-size: 0.82rem;
  text-align: center;
}

.row-remove-button {
  width: 2.25rem;
  height: 2.25rem;
  padding: 0 !important;
}

@media (max-width: 720px) {
  .matcher-editor-header {
    align-items: stretch;
    flex-direction: column;
  }

  .matcher-add-button {
    justify-content: center;
    width: 100%;
  }

  .matcher-tabs {
    grid-template-columns: 1fr;
  }

  .matcher-row,
  .matcher-row-head {
    grid-template-columns: 1fr;
  }

  .matcher-row-head {
    display: none;
  }

  .row-remove-button {
    width: 100%;
  }
}
</style>
