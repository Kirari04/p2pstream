<script setup lang="ts">
import { computed, inject, reactive, ref } from "vue";
import type { ComputedRef } from "vue";
import PublicPolicyMatchEditor from "@/components/editors/PublicPolicyMatchEditor.vue";
import { managementClient } from "@/api/managementClient";
import { BUSY_REASON } from "@/lib/disabledReasons";
import Button from "@/volt/Button.vue";
import Modal from "@/volt/Modal.vue";
import {
  PublicBackendType,
  PublicCacheQueryMode,
  PublicCacheScope,
  PublicCacheTtlMode,
  PublicListenerProtocol,
  PublicRateLimitMatchOperator,
  type GetPublicProxyConfigResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

type Runner = (action: () => Promise<void>) => Promise<boolean>;
type MatcherForm = {
  name: string;
  operator: PublicRateLimitMatchOperator;
  value: string;
};

const props = defineProps<{
  config: GetPublicProxyConfigResponse | null;
}>();

const emit = defineEmits<{
  (event: "saved"): void;
}>();

const runManagementAction = inject<Runner>("runManagementAction");
const isBusy = inject<ComputedRef<boolean>>("isBusy");

const isOpen = ref(false);
const matchEditor = ref<InstanceType<typeof PublicPolicyMatchEditor> | null>(null);
const rules = computed(() => props.config?.cacheRules ?? []);
const routes = computed(() => props.config?.routes ?? []);
const proxyBackends = computed(() => (props.config?.backends ?? []).filter((backend) => backend.backendType === PublicBackendType.PROXY_FORWARD));

const form = reactive({
  id: "",
  name: "",
  enabled: true,
  priority: 100,
  match: {
    methods: ["GET", "HEAD"] as string[],
    protocols: [] as PublicListenerProtocol[],
    hostPatternsText: "",
    pathPrefixesText: "",
    pathSuffixesText: ".css\n.js\n.png\n.jpg\n.jpeg\n.webp\n.svg\n.woff2",
    headers: [] as MatcherForm[],
    cookies: [] as MatcherForm[],
    queryParams: [] as MatcherForm[],
  },
  routeIds: [] as string[],
  backendIds: [] as string[],
  scope: PublicCacheScope.SELECTED_BACKEND,
  ttlMode: PublicCacheTtlMode.FIXED,
  ttlMinutes: 60,
  queryMode: PublicCacheQueryMode.FULL,
  queryParamsText: "",
  varyHeadersText: "Accept-Encoding",
  statusCodesText: "200\n203\n204\n301\n308",
  maxObjectMiB: 100,
  addCacheStatusHeader: true,
});

const ttlModeOptions = [
  { label: "Fixed TTL", value: PublicCacheTtlMode.FIXED },
  { label: "Origin TTL", value: PublicCacheTtlMode.ORIGIN },
];
const scopeOptions = [
  { label: "Selected backend", value: PublicCacheScope.SELECTED_BACKEND },
  { label: "Route", value: PublicCacheScope.ROUTE },
];
const queryModeOptions = [
  { label: "Full query", value: PublicCacheQueryMode.FULL },
  { label: "Ignore query", value: PublicCacheQueryMode.IGNORE },
  { label: "Allowlist", value: PublicCacheQueryMode.ALLOWLIST },
  { label: "Denylist", value: PublicCacheQueryMode.DENYLIST },
];

const submitDisabledReason = computed(() => {
  if (isBusy?.value) return BUSY_REASON;
  if (!form.name.trim()) return "Enter a rule name.";
  if (form.ttlMinutes < 1) return "TTL must be at least 1 minute.";
  if (form.maxObjectMiB < 1) return "Maximum object size must be at least 1 MiB.";
  if (!numberLines(form.statusCodesText).length) return "Add at least one cacheable status code.";
  if ((form.queryMode === PublicCacheQueryMode.ALLOWLIST || form.queryMode === PublicCacheQueryMode.DENYLIST) && !lines(form.queryParamsText).length) {
    return "Query allowlist and denylist modes require query parameters.";
  }
  return "";
});
const submitDisabled = computed(() => Boolean(submitDisabledReason.value));

function resetForm() {
  form.id = "";
  form.name = nextRuleName();
  form.enabled = true;
  form.priority = 100;
  form.match.methods = ["GET", "HEAD"];
  form.match.protocols = [];
  form.match.hostPatternsText = "";
  form.match.pathPrefixesText = "";
  form.match.pathSuffixesText = ".css\n.js\n.png\n.jpg\n.jpeg\n.webp\n.svg\n.woff2";
  form.match.headers = [];
  form.match.cookies = [];
  form.match.queryParams = [];
  form.routeIds = [];
  form.backendIds = [];
  form.scope = PublicCacheScope.SELECTED_BACKEND;
  form.ttlMode = PublicCacheTtlMode.FIXED;
  form.ttlMinutes = 60;
  form.queryMode = PublicCacheQueryMode.FULL;
  form.queryParamsText = "";
  form.varyHeadersText = "Accept-Encoding";
  form.statusCodesText = "200\n203\n204\n301\n308";
  form.maxObjectMiB = 100;
  form.addCacheStatusHeader = true;
}

function openCreate() {
  resetForm();
  isOpen.value = true;
}

function openEdit(ruleId: bigint | string) {
  const id = ruleId.toString();
  const rule = rules.value.find((item) => item.id.toString() === id);
  if (!rule) return;
  form.id = rule.id.toString();
  form.name = rule.name;
  form.enabled = rule.enabled;
  form.priority = Number(rule.priority);
  form.match.methods = [...(rule.match?.methods ?? [])];
  form.match.protocols = [...(rule.match?.protocols ?? [])];
  form.match.hostPatternsText = (rule.match?.hostPatterns ?? []).join("\n");
  form.match.pathPrefixesText = (rule.match?.pathPrefixes ?? []).join("\n");
  form.match.pathSuffixesText = (rule.match?.pathSuffixes ?? []).join("\n");
  form.match.headers = cloneMatchers(rule.match?.headers ?? []);
  form.match.cookies = cloneMatchers(rule.match?.cookies ?? []);
  form.match.queryParams = cloneMatchers(rule.match?.queryParams ?? []);
  form.routeIds = rule.routeIds.map((value) => value.toString());
  form.backendIds = rule.backendIds.map((value) => value.toString());
  form.scope = rule.scope || PublicCacheScope.SELECTED_BACKEND;
  form.ttlMode = rule.ttlMode || PublicCacheTtlMode.FIXED;
  form.ttlMinutes = Math.max(1, Math.round(Number(rule.ttlMillis || 3600000n) / 60000));
  form.queryMode = rule.queryMode || PublicCacheQueryMode.FULL;
  form.queryParamsText = rule.queryParams.join("\n");
  form.varyHeadersText = rule.varyHeaders.join("\n") || "Accept-Encoding";
  form.statusCodesText = rule.cacheStatusCodes.join("\n") || "200\n203\n204\n301\n308";
  form.maxObjectMiB = Math.max(1, Math.round(Number(rule.maxObjectBytes || 104857600n) / 1024 / 1024));
  form.addCacheStatusHeader = rule.addCacheStatusHeader;
  isOpen.value = true;
  requestAnimationFrame(() => matchEditor.value?.setInitialTab());
}

function close() {
  isOpen.value = false;
}

function nextRuleName(): string {
  let index = rules.value.length + 1;
  const used = new Set(rules.value.map((rule) => rule.name));
  while (used.has(`cache-${index.toString()}`)) index += 1;
  return `cache-${index.toString()}`;
}

function lines(value: string): string[] {
  return value.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
}

function numberLines(value: string): bigint[] {
  return lines(value)
    .map((line) => Number.parseInt(line, 10))
    .filter((value) => Number.isFinite(value) && value >= 100 && value <= 599)
    .map((value) => BigInt(value));
}

function cloneMatchers(matchers: readonly MatcherForm[]): MatcherForm[] {
  return matchers.map((matcher) => ({ name: matcher.name, operator: matcher.operator, value: matcher.value }));
}

function matcherPayload(matchers: MatcherForm[]) {
  return matchers
    .map((matcher) => ({
      name: matcher.name.trim(),
      operator: matcher.operator,
      value: matcher.value,
    }))
    .filter((matcher) => matcher.name);
}

async function run(action: () => Promise<void>): Promise<boolean> {
  if (!runManagementAction) return false;
  return runManagementAction(action);
}

async function submitRule() {
  const ok = await run(async () => {
    const payload = {
      name: form.name.trim(),
      priority: BigInt(form.priority || 0),
      enabled: form.enabled,
      match: {
        methods: [...form.match.methods],
        protocols: [...form.match.protocols],
        hostPatterns: lines(form.match.hostPatternsText),
        pathPrefixes: lines(form.match.pathPrefixesText),
        pathSuffixes: lines(form.match.pathSuffixesText),
        headers: matcherPayload(form.match.headers),
        cookies: matcherPayload(form.match.cookies),
        queryParams: matcherPayload(form.match.queryParams),
      },
      routeIds: form.routeIds.map((id) => BigInt(id)),
      backendIds: form.backendIds.map((id) => BigInt(id)),
      scope: form.scope,
      ttlMode: form.ttlMode,
      ttlMillis: BigInt(Math.max(1, form.ttlMinutes) * 60000),
      queryMode: form.queryMode,
      queryParams: lines(form.queryParamsText),
      varyHeaders: lines(form.varyHeadersText),
      cacheStatusCodes: numberLines(form.statusCodesText),
      maxObjectBytes: BigInt(Math.max(1, form.maxObjectMiB) * 1024 * 1024),
      addCacheStatusHeader: form.addCacheStatusHeader,
    };
    if (form.id) {
      await managementClient.updatePublicCacheRule({ id: BigInt(form.id), ...payload });
    } else {
      await managementClient.createPublicCacheRule(payload);
    }
  });
  if (ok) {
    isOpen.value = false;
    emit("saved");
  }
}

defineExpose({ openCreate, openEdit, close });
</script>

<template>
  <Modal v-model="isOpen" :title="form.id ? 'Edit Cache Rule' : 'Add Cache Rule'" max-width="64rem">
    <form class="grid gap-5" @submit.prevent="submitRule">
      <section class="grid gap-4 sm:grid-cols-4">
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888] sm:col-span-2">
          Name
          <input v-model="form.name" class="vercel-input text-sm normal-case tracking-normal" required />
        </label>
        <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
          Priority
          <input v-model.number="form.priority" type="number" class="vercel-input text-sm normal-case tracking-normal" required />
        </label>
        <label class="flex items-center gap-2 self-end text-sm text-[#d4d4d8]">
          <input v-model="form.enabled" type="checkbox" />
          Enabled
        </label>
      </section>

      <PublicPolicyMatchEditor ref="matchEditor" :form="form.match" />

      <section class="grid gap-4 rounded-md border border-[#222] bg-[#050505] p-4">
        <h4 class="text-sm font-semibold text-white">Cache behavior</h4>
        <p class="text-xs leading-5 text-[#777]">
          Requests with Cookie or Authorization headers are always bypassed. Responses with Set-Cookie, no-store, private, or no-cache are never cached.
        </p>
        <div class="grid gap-4 sm:grid-cols-4">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            TTL mode
            <select v-model="form.ttlMode" class="vercel-input text-sm normal-case tracking-normal">
              <option v-for="option in ttlModeOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
            </select>
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Default TTL minutes
            <input v-model.number="form.ttlMinutes" type="number" min="1" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Scope
            <select v-model="form.scope" class="vercel-input text-sm normal-case tracking-normal">
              <option v-for="option in scopeOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
            </select>
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Max object MiB
            <input v-model.number="form.maxObjectMiB" type="number" min="1" class="vercel-input text-sm normal-case tracking-normal" />
          </label>
        </div>
      </section>

      <section class="grid gap-4 rounded-md border border-[#222] bg-[#050505] p-4">
        <h4 class="text-sm font-semibold text-white">Keys and filters</h4>
        <div class="grid gap-4 sm:grid-cols-2">
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Routes
            <select v-model="form.routeIds" multiple class="vercel-input min-h-28 text-sm normal-case tracking-normal">
              <option v-for="route in routes" :key="route.id.toString()" :value="route.id.toString()">#{{ route.id.toString() }} {{ route.hostPattern || '*' }}{{ route.pathPrefix || '/' }}</option>
            </select>
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Backends
            <select v-model="form.backendIds" multiple class="vercel-input min-h-28 text-sm normal-case tracking-normal">
              <option v-for="backend in proxyBackends" :key="backend.id.toString()" :value="backend.id.toString()">{{ backend.name }}</option>
            </select>
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Query mode
            <select v-model="form.queryMode" class="vercel-input text-sm normal-case tracking-normal">
              <option v-for="option in queryModeOptions" :key="option.value" :value="option.value">{{ option.label }}</option>
            </select>
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Query params
            <textarea v-model="form.queryParamsText" class="vercel-input min-h-24 text-sm normal-case tracking-normal" placeholder="v&#10;version" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Vary headers
            <textarea v-model="form.varyHeadersText" class="vercel-input min-h-24 text-sm normal-case tracking-normal" placeholder="Accept-Encoding" />
          </label>
          <label class="grid gap-1.5 text-xs font-medium uppercase tracking-wider text-[#888]">
            Cache status codes
            <textarea v-model="form.statusCodesText" class="vercel-input min-h-24 text-sm normal-case tracking-normal" placeholder="200&#10;301" />
          </label>
        </div>
        <label class="flex items-center gap-2 text-sm text-[#d4d4d8]">
          <input v-model="form.addCacheStatusHeader" type="checkbox" />
          Add X-p2pstream-Cache response header
        </label>
      </section>

      <div class="flex justify-end gap-3 border-t border-[#222] pt-4">
        <Button type="button" severity="secondary" label="Cancel" @click="close" />
        <Button type="submit" :label="form.id ? 'Save Rule' : 'Create Rule'" :disabled="submitDisabled" :title="submitDisabledReason" />
      </div>
    </form>
  </Modal>
</template>
