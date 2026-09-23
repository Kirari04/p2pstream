<script setup lang="ts">
import { computed, inject, ref } from "vue";
import {
  AlertTriangle as AlertIcon,
  ArrowRight as ArrowIcon,
  RefreshCw as RefreshIcon,
} from "@lucide/vue";
import { NButton, NCheckbox, NTag } from "naive-ui";
import DisabledHint from "@/components/DisabledHint.vue";
import {
  isBusyKey,
  runManagementActionKey,
} from "@/composables/managementContextKeys";
import { useConfirmDialog } from "@/composables/useConfirmDialog";
import { useManagementClient } from "@/composables/useManagementClient";
import { BUSY_REASON } from "@/lib/disabledReasons";
import { messageFromError } from "@/lib/errors";
import {
  PublicSiteMigrationHostnameMode,
  PublicSiteMigrationSeverity,
  type GetPublicProxyConfigResponse,
  type PreviewPublicSiteMigrationResponse,
} from "@/gen/proto/p2pstream/v1/management_pb";

const props = defineProps<{ config: GetPublicProxyConfigResponse | null }>();
const emit = defineEmits<{
  (event: "applied"): void;
  (event: "edit-route", id: bigint): void;
}>();
const managementClient = useManagementClient();
const runManagementAction = inject(runManagementActionKey);
const isBusy = inject(
  isBusyKey,
  computed(() => false),
);
const { confirm } = useConfirmDialog();
const preview = ref<PreviewPublicSiteMigrationResponse | null>(null);
const acceptedWarnings = ref<string[]>([]);
const selectedListenerIds = ref<string[] | null>(null);
const previewListenerIds = ref<bigint[]>([]);
const migrationError = ref("");
const legacyRoutes = computed(() =>
  (props.config?.routes ?? []).filter((route) => route.siteId === 0n),
);
const migrationListeners = computed(() =>
  (props.config?.listeners ?? []).filter((listener) =>
    legacyRoutes.value.some((route) => route.listenerId === listener.id),
  ),
);
const chosenListenerIds = computed(
  () =>
    selectedListenerIds.value ??
    migrationListeners.value.map((listener) => listener.id.toString()),
);
const selectedRouteCount = computed(
  () =>
    legacyRoutes.value.filter((route) =>
      chosenListenerIds.value.includes(route.listenerId.toString()),
    ).length,
);
const acknowledgementIssues = computed(() => [
  ...new Map(
    (preview.value?.issues ?? [])
      .filter(
        (issue) =>
          issue.severity === PublicSiteMigrationSeverity.WARNING &&
          issue.requiresAcknowledgement,
      )
      .map((issue) => [issue.code, issue]),
  ).values(),
]);
const requiredWarningCodes = computed(() =>
  acknowledgementIssues.value.map((issue) => issue.code),
);
const blockerIssues = computed(() =>
  (preview.value?.issues ?? []).filter(
    (issue) => issue.severity === PublicSiteMigrationSeverity.BLOCKER,
  ),
);
const orderedIssues = computed(() => [
  ...blockerIssues.value,
  ...(preview.value?.issues ?? []).filter(
    (issue) => issue.severity !== PublicSiteMigrationSeverity.BLOCKER,
  ),
]);
const unacceptedWarning = computed(
  () =>
    requiredWarningCodes.value.find(
      (code) => !acceptedWarnings.value.includes(code),
    ) ?? "",
);
const applyDisabledReason = computed(() => {
  if (isBusy.value) return BUSY_REASON;
  if (!preview.value?.canApply)
    return "Resolve every blocking migration issue before applying.";
  if (unacceptedWarning.value)
    return "Acknowledge every required semantic warning before applying.";
  return "";
});

async function loadPreview() {
  if (!runManagementAction || !chosenListenerIds.value.length) return;
  migrationError.value = "";
  const scope = chosenListenerIds.value.map((id) => BigInt(id));
  let result: PreviewPublicSiteMigrationResponse | undefined;
  const ok = await runManagementAction(
    async () => {
      result = await managementClient.previewPublicSiteMigration({
        listenerIds: scope,
      });
    },
    undefined,
    {
      onError: (error) => {
        migrationError.value = messageFromError(error);
      },
    },
  );
  if (ok && result) {
    preview.value = result;
    previewListenerIds.value = scope;
    acceptedWarnings.value = [];
  }
}

async function applyMigration() {
  if (!runManagementAction || !preview.value || applyDisabledReason.value)
    return;
  migrationError.value = "";
  const request = {
    revision: preview.value.revision,
    listenerIds: [...previewListenerIds.value],
    acceptedWarningCodes: [...acceptedWarnings.value],
  };
  if (
    !(await confirm(
      "Apply route migration?",
      `Create ${preview.value.groups.length} published Sites from ${selectedRouteCount.value} standalone routes on ${previewListenerIds.value.length} selected listeners? The reviewed route copies will become independently editable.`,
      "Apply migration",
    ))
  )
    return;
  const ok = await runManagementAction(
    async () => {
      await managementClient.applyPublicSiteMigration(request);
    },
    undefined,
    {
      onError: (error) => {
        migrationError.value = messageFromError(error);
      },
    },
  );
  if (ok) {
    preview.value = null;
    acceptedWarnings.value = [];
    emit("applied");
  }
}

function issueTagType(severity: PublicSiteMigrationSeverity) {
  return severity === PublicSiteMigrationSeverity.BLOCKER
    ? ("error" as const)
    : ("warning" as const);
}
function groupModeLabel(mode: PublicSiteMigrationHostnameMode) {
  return mode === PublicSiteMigrationHostnameMode.DEFAULT
    ? "Any unmatched hostname"
    : "Specific hostnames";
}
function setWarningAccepted(code: string, checked: boolean) {
  acceptedWarnings.value = checked
    ? [...new Set([...acceptedWarnings.value, code])]
    : acceptedWarnings.value.filter((item) => item !== code);
}
function selectListener(id: bigint, checked: boolean) {
  const value = id.toString();
  selectedListenerIds.value = checked
    ? [...new Set([...chosenListenerIds.value, value])]
    : chosenListenerIds.value.filter((item) => item !== value);
  preview.value = null;
  acceptedWarnings.value = [];
  migrationError.value = "";
}
function routeLabel(id: bigint) {
  const route = legacyRoutes.value.find((item) => item.id === id);
  return route?.isDefault ? "Default path" : route?.pathPrefix || "All paths";
}
function routeContext(id: bigint) {
  const route = legacyRoutes.value.find((item) => item.id === id);
  if (!route) return `Route #${id.toString()}`;
  const hostname = route.isDefault || !route.hostPattern
    ? "Any unmatched hostname"
    : route.hostPattern;
  return `${hostname} · ${routeLabel(id)}`;
}
function listenerLabel(id: bigint) {
  return (
    props.config?.listeners.find((listener) => listener.id === id)?.name ??
    `Listener #${id.toString()}`
  );
}
</script>

<template>
  <section
    v-if="legacyRoutes.length"
    class="migration-panel"
    aria-labelledby="migration-panel-title"
  >
    <div class="migration-panel__banner">
      <span class="migration-panel__icon" aria-hidden="true"
        ><AlertIcon class="icon-md"
      /></span>
      <div class="grow-fill">
        <h2
          id="migration-panel-title"
          class="copy-sm weight-semibold base-text"
        >
          {{ legacyRoutes.length }} standalone
          {{ legacyRoutes.length === 1 ? "route needs" : "routes need" }} a Site
        </h2>
        <p class="margin-top-xs copy-xs line-normal muted-text">
          Review how the current routes will become Sites. The preview expires
          when its configuration changes.
        </p>
      </div>
      <NButton
        secondary
        size="small"
        :loading="isBusy"
        :disabled="isBusy || !chosenListenerIds.length"
        @click="loadPreview"
        ><template #icon><RefreshIcon class="icon-sm" /></template
        >{{ preview ? "Refresh preview" : "Preview migration" }}</NButton
      >
    </div>

    <fieldset class="migration-panel__scope" :disabled="isBusy">
      <legend class="copy-xs weight-semibold base-text">
        Listeners to migrate
      </legend>
      <div class="layout-row wrap-items space-lg">
        <NCheckbox
          v-for="listener in migrationListeners"
          :key="listener.id.toString()"
          :checked="chosenListenerIds.includes(listener.id.toString())"
          :disabled="isBusy"
          @update:checked="selectListener(listener.id, Boolean($event))"
          >{{ listener.name }}</NCheckbox
        >
      </div>
      <p class="margin-top-sm copy-xs muted-text">
        {{ selectedRouteCount }} selected routes. You can migrate listeners
        separately while resolving blocked configurations.
      </p>
    </fieldset>

    <div v-if="preview" class="migration-panel__preview">
      <div class="migration-panel__meta">
        <div>
          <p class="copy-xs weight-semibold label-case letter-wide muted-text">
            Migration preview
          </p>
          <p class="margin-top-xs copy-xs muted-text">
            {{ preview.groups.length }} proposed Sites on
            {{ previewListenerIds.length }} selected listeners
          </p>
        </div>
        <NTag
          size="small"
          :bordered="false"
          :type="preview.canApply ? 'success' : 'error'"
          >{{ preview.canApply ? "Can apply" : "Blocked" }}</NTag
        >
      </div>
      <div
        v-if="blockerIssues.length"
        class="migration-readiness"
        role="alert"
      >
        <span class="migration-readiness__mark" aria-hidden="true"
          ><AlertIcon class="icon-md"
        /></span>
        <div>
          <p class="copy-sm weight-semibold base-text">
            {{ blockerIssues.length }}
            {{ blockerIssues.length === 1 ? "blocker" : "blockers" }} must be
            resolved
          </p>
          <p class="margin-top-xs copy-xs line-normal muted-text">
            No configuration will change until every item below is fixed and a
            fresh preview reports that migration can apply.
          </p>
        </div>
      </div>
      <div v-if="orderedIssues.length" class="migration-panel__issues-wrap">
        <div class="migration-panel__section-heading">
          <div>
            <p class="copy-xs weight-semibold label-case letter-wide muted-text">
              {{ blockerIssues.length ? "Resolve before migration" : "Behavior review" }}
            </p>
            <p class="margin-top-xs copy-xs muted-text">
              Affected hostnames and routes are listed with the required next step.
            </p>
          </div>
          <NTag
            v-if="blockerIssues.length"
            size="small"
            :bordered="false"
            type="error"
            >{{ blockerIssues.length }} blocking</NTag
          >
        </div>
        <div class="migration-panel__issues">
          <article
            v-for="(issue, index) in orderedIssues"
            :key="`${issue.code}-${issue.listenerId}-${index}`"
            class="migration-issue"
          >
            <NTag
              size="small"
              :bordered="false"
              :type="issueTagType(issue.severity)"
              >{{
                issue.severity === PublicSiteMigrationSeverity.BLOCKER
                  ? "Blocker"
                  : "Warning"
              }}</NTag
            >
            <div class="grow-fill">
              <p class="copy-xs weight-semibold base-text">
                {{ issue.summary }} · {{ listenerLabel(issue.listenerId) }}
              </p>
              <p class="margin-top-xs copy-xs line-normal muted-text">
                {{ issue.detail }}
              </p>
              <div
                v-if="issue.routeIds.length"
                class="migration-issue__routes margin-top-sm"
              >
                <div
                  v-for="routeId in issue.routeIds"
                  :key="routeId.toString()"
                  class="migration-issue__route"
                >
                  <div>
                    <p class="copy-xs weight-semibold base-text">
                      Route #{{ routeId.toString() }}
                    </p>
                    <p class="margin-top-xs mono-text copy-xs muted-text">
                      {{ routeContext(routeId) }}
                    </p>
                  </div>
                  <NButton
                    secondary
                    size="tiny"
                    @click="emit('edit-route', routeId)"
                    >Edit route</NButton
                  >
                </div>
              </div>
            </div>
          </article>
        </div>
      </div>
      <div class="migration-panel__groups">
        <article
          v-for="group in preview.groups"
          :key="group.key"
          class="migration-group"
        >
          <div>
            <p class="copy-sm weight-semibold base-text">
              {{ group.proposedSiteName }}
            </p>
            <p class="margin-top-xs copy-xs muted-text">
              {{ group.listenerName }} ·
              {{ groupModeLabel(group.hostnameMode) }}
            </p>
          </div>
          <div class="migration-group__facts">
            <span
              >{{ group.sourceRouteIds.length }}
              {{ group.sourceRouteIds.length === 1 ? "route" : "routes" }}
              retained</span
            ><span v-if="group.routeCopies.length"
              >{{ group.routeCopies.length }}
              {{ group.routeCopies.length === 1 ? "copy" : "copies" }}</span
            >
          </div>
          <p
            v-if="group.hostnamePatterns.length"
            class="mono-text copy-xs muted-text"
          >
            {{ group.hostnamePatterns.join(" · ") }}
          </p>
          <details class="migration-group__routes">
            <summary class="copy-xs weight-semibold base-text">
              Review routes and copies
            </summary>
            <ul class="migration-route-list">
              <li v-for="id in group.sourceRouteIds" :key="`retained-${id}`">
                <span class="copy-xs base-text"
                  >Keep route #{{ id.toString() }} · {{ routeLabel(id) }}</span
                ><NButton text size="tiny" @click="emit('edit-route', id)"
                  >Edit route #{{ id.toString() }}</NButton
                >
              </li>
              <li
                v-for="copy in group.routeCopies"
                :key="`copy-${copy.sourceRouteId}`"
              >
                <div>
                  <p class="copy-xs weight-semibold base-text">
                    Copy route #{{ copy.sourceRouteId.toString() }} ·
                    {{ routeLabel(copy.sourceRouteId) }}
                  </p>
                  <p class="margin-top-xs copy-xs muted-text">
                    {{ copy.reason }}. The copy can be edited independently in
                    this Site.
                  </p>
                </div>
              </li>
            </ul>
          </details>
        </article>
      </div>
      <fieldset
        v-if="acknowledgementIssues.length"
        class="migration-panel__acknowledgements"
      >
        <legend class="copy-xs weight-semibold base-text">
          Acknowledge behavior changes for these listeners
        </legend>
        <NCheckbox
          v-for="issue in acknowledgementIssues"
          :key="issue.code"
          :checked="acceptedWarnings.includes(issue.code)"
          @update:checked="setWarningAccepted(issue.code, Boolean($event))"
          >{{ issue.summary }}</NCheckbox
        >
      </fieldset>
      <div class="migration-panel__actions">
        <p class="copy-xs line-normal muted-text">
          Applying creates published Sites, preserves or maps route identity as
          shown, and removes standalone eligibility in one operation.
        </p>
        <DisabledHint
          :disabled="Boolean(applyDisabledReason)"
          :reason="applyDisabledReason"
          ><NButton
            type="primary"
            size="small"
            :disabled="Boolean(applyDisabledReason)"
            @click="applyMigration"
            >Apply migration<template #icon
              ><ArrowIcon class="icon-sm" /></template></NButton
        ></DisabledHint>
      </div>
    </div>
    <p
      v-if="migrationError"
      role="alert"
      class="migration-panel__error copy-xs line-normal error-text"
    >
      {{ migrationError }} Refresh the preview before retrying.
    </p>
  </section>
</template>

<style scoped>
.migration-panel__error {
  margin: 0;
  padding: 1rem 1.25rem;
  overflow-wrap: anywhere;
}
.migration-panel__scope,
.migration-panel__acknowledgements {
  display: grid;
  gap: 0.5rem;
  min-width: 0;
  margin: 0 1.25rem 1rem;
  border: 0;
  padding: 0;
}
.migration-panel__scope legend,
.migration-panel__acknowledgements legend {
  margin-bottom: 0.5rem;
}
.migration-panel__acknowledgements {
  margin: 0;
}
.migration-readiness {
  display: flex;
  align-items: flex-start;
  gap: 0.75rem;
  border: 1px solid color-mix(in srgb, var(--app-error) 38%, var(--app-border));
  border-radius: 0.5rem;
  padding: 0.875rem;
  background: color-mix(in srgb, var(--app-error) 7%, var(--app-panel));
}
.migration-readiness__mark {
  display: grid;
  width: 2rem;
  height: 2rem;
  flex: 0 0 auto;
  place-items: center;
  border-radius: 0.375rem;
  background: color-mix(in srgb, var(--app-error) 14%, var(--app-panel));
  color: var(--app-error);
}
.migration-panel__issues-wrap {
  display: grid;
  gap: 0.625rem;
}
.migration-panel__section-heading {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 1rem;
}
.migration-issue__routes {
  display: grid;
  gap: 0.5rem;
}
.migration-issue__route {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  border-left: 2px solid color-mix(in srgb, var(--app-error) 55%, var(--app-border));
  padding: 0.5rem 0.625rem;
  background: var(--app-panel-muted);
}
.migration-group__routes {
  grid-column: 1/-1;
  min-width: 0;
}
.migration-group__routes summary {
  cursor: pointer;
  padding: 0.25rem 0;
}
.migration-route-list {
  display: grid;
  gap: 0.5rem;
  margin: 0.5rem 0 0;
  padding: 0;
  list-style: none;
}
.migration-route-list li {
  display: flex;
  justify-content: space-between;
  gap: 0.75rem;
  padding: 0.5rem;
  background: var(--app-panel-muted);
  border-radius: 0.25rem;
}
.migration-route-list p {
  overflow-wrap: anywhere;
}
.migration-panel {
  overflow: hidden;
  border: 1px solid
    color-mix(in srgb, var(--app-warning) 45%, var(--app-border));
  border-radius: 0.625rem;
  background: color-mix(in srgb, var(--app-warning) 6%, var(--app-panel));
}
.migration-panel__banner,
.migration-panel__meta,
.migration-panel__actions,
.migration-issue {
  display: flex;
  align-items: center;
  gap: 0.875rem;
}
.migration-panel__banner {
  padding: 1rem 1.25rem;
}
.migration-panel__icon {
  display: grid;
  width: 2rem;
  height: 2rem;
  flex: 0 0 auto;
  place-items: center;
  border-radius: 50%;
  background: color-mix(in srgb, var(--app-warning) 14%, var(--app-panel));
  color: var(--app-warning);
}
.migration-panel__preview {
  display: grid;
  gap: 1rem;
  border-top: 1px solid var(--app-border-subtle);
  padding: 1rem 1.25rem;
  background: var(--app-panel);
}
.migration-panel__meta,
.migration-panel__actions {
  justify-content: space-between;
}
.migration-panel__groups,
.migration-panel__issues {
  overflow: hidden;
  border: 1px solid var(--app-border-subtle);
  border-radius: 0.5rem;
}
.migration-group {
  display: grid;
  grid-template-columns: minmax(10rem, 1fr) auto;
  gap: 0.5rem 1rem;
  padding: 0.75rem;
}
.migration-group + .migration-group,
.migration-issue + .migration-issue {
  border-top: 1px solid var(--app-border-subtle);
}
.migration-group > p {
  grid-column: 1/-1;
}
.migration-group__facts {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 0.375rem;
}
.migration-group__facts span {
  border-radius: 0.25rem;
  padding: 0.125rem 0.375rem;
  background: var(--app-panel-muted);
  color: var(--app-text-muted);
  font-size: 0.6875rem;
}
.migration-issue {
  align-items: flex-start;
  padding: 0.75rem;
}
.migration-panel__actions > p {
  max-width: 44rem;
}
@media (max-width: 640px) {
  .migration-panel__banner,
  .migration-panel__meta,
  .migration-panel__section-heading,
  .migration-panel__actions,
  .migration-readiness,
  .migration-issue {
    align-items: stretch;
    flex-direction: column;
  }
  .migration-issue__route {
    align-items: stretch;
    flex-direction: column;
  }
  .migration-issue__route :deep(.n-button) {
    width: 100%;
  }
  .migration-panel__banner > :deep(.n-button),
  .migration-panel__actions > :deep(.disabled-hint),
  .migration-panel__actions :deep(.n-button) {
    width: 100%;
  }
  .migration-group {
    grid-template-columns: minmax(0, 1fr);
  }
  .migration-group__facts {
    justify-content: flex-start;
  }
}
</style>
