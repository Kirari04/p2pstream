<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { NAlert, NButton, NTag } from "naive-ui";
import { ArrowUpRight, Check, CircleAlert, Copy, LoaderCircle, RotateCcw, ShieldCheck } from "@lucide/vue";
import type { ServerUpdateOperation } from "@/gen/proto/p2pstream/v1/management_pb";
import { serverUpdateDiagnostics, serverUpdateElapsed, serverUpdateExplanation, serverUpdatePaused, serverUpdatePhaseLabel, serverUpdateStages, serverUpdateTone } from "@/lib/serverUpdates";

const props = defineProps<{ operation: ServerUpdateOperation; now: number; environment: string; instanceId: string; stale: boolean }>();
const tone = computed(() => serverUpdateTone(props.operation.phase));
const paused = computed(() => serverUpdatePaused(props.operation.phase));
const activeIndex = computed(() => serverUpdateStages.findIndex((stage) => stage.phase === props.operation.phase));
const showStages = computed(() => activeIndex.value >= 0 || props.operation.phase === "succeeded");
const elapsed = computed(() => serverUpdateElapsed(props.operation, props.now));
const diagnostics = computed(() => serverUpdateDiagnostics(props.environment, props.instanceId, props.operation));
const details = ref<HTMLDetailsElement>();
const copyState = ref("");
watch(() => props.operation.id, () => { copyState.value = ""; });
async function copyDiagnostics() {
  try { await navigator.clipboard.writeText(diagnostics.value); copyState.value = "Diagnostics copied"; }
  catch { copyState.value = "Clipboard unavailable. Select and copy the diagnostic text below."; if (details.value) details.value.open = true; }
}
function stageState(index: number): string {
  if (props.operation.phase === "succeeded" || index < activeIndex.value) return "complete";
  return index === activeIndex.value ? "active" : "waiting";
}
</script>

<template>
  <section class="update-progress" :class="`update-progress--${tone}`" aria-label="Server update operation">
    <header class="progress-header">
      <div>
        <div class="progress-eyebrow">{{ paused ? 'HOST OUTCOME' : stale ? 'LAST CONFIRMED HOST STAGE' : 'HOST UPDATE IN PROGRESS' }}</div>
        <h3 role="status" aria-live="polite" aria-atomic="true">
          <ShieldCheck v-if="operation.phase === 'succeeded'" :size="23" />
          <RotateCcw v-else-if="operation.phase === 'rolled_back' || operation.phase === 'rolling_back'" :size="23" />
          <CircleAlert v-else-if="tone === 'error'" :size="23" />
          <LoaderCircle v-else :size="23" :class="{ 'progress-spinner': !stale }" />
          {{ serverUpdatePhaseLabel(operation.phase) }}
        </h3>
        <div class="progress-versions"><span>{{ operation.previousVersion || 'Previous release' }}</span><ArrowUpRight :size="16" aria-label="to" /><strong>{{ operation.targetVersion || 'Selected release' }}</strong></div>
      </div>
      <div class="progress-timing">
        <span>{{ paused ? 'Recorded duration' : 'Elapsed since request' }}</span>
        <strong data-testid="update-total-elapsed">{{ elapsed.total }}</strong>
        <small v-if="paused">{{ operation.phase === 'recovery_required' ? 'Until recovery paused' : 'At the confirmed outcome' }}</small>
        <small v-else>No fixed completion estimate</small>
      </div>
    </header>

    <div class="progress-body" :class="{ 'progress-body--with-stages': showStages }">
      <ol v-if="showStages" class="progress-stages" aria-label="Update stages">
        <li v-for="(stage, index) in serverUpdateStages" :key="stage.phase" :class="`progress-stage--${stageState(index)}`" :aria-current="stageState(index) === 'active' ? 'step' : undefined">
          <span class="stage-marker" aria-hidden="true"><Check v-if="stageState(index) === 'complete'" :size="13" /><span v-else>{{ String(index + 1).padStart(2, '0') }}</span></span>
          <span>{{ stage.label }}</span><span class="sr-only"> · {{ stageState(index) === 'complete' ? 'complete' : stageState(index) === 'active' ? 'current stage' : 'pending' }}</span>
        </li>
      </ol>
      <div class="progress-context">
        <div v-if="!paused" class="progress-stage-meta"><NTag :type="tone" :bordered="false" size="small">{{ operation.phase === 'pulling' ? 'Download before restart' : operation.phase === 'validating' ? 'Stability + reconnection checks' : operation.phase === 'rolling_back' ? 'Recovery in progress' : 'Host controlled' }}</NTag><span v-if="elapsed.stage !== 'Unavailable'">{{ elapsed.stage }} since stage reported</span></div>
        <p class="progress-explanation">{{ serverUpdateExplanation(operation.phase) }}</p>
        <p v-if="stale && paused" class="progress-note">This is the last confirmed host outcome. An unavailable management or updater connection does not change this recorded state.</p>
        <p v-else-if="stale" class="progress-note">Showing the last confirmed host state. A connection interruption does not establish whether this update succeeded or failed.</p>
        <p v-else-if="!paused" class="progress-note">The updater continues on the Docker host if you close this page or lose the management connection.</p>

        <div v-if="operation.phase === 'rolled_back'" class="progress-next-step"><strong>Next step</strong><p>Review the failure below before trying again. Preview a release when the cause is resolved.</p></div>
        <div v-else-if="operation.phase === 'failed'" class="progress-next-step"><strong>Next step</strong><p>Resolve the reported cause on {{ environment }}’s Docker host, then preview the release again.</p></div>
        <div v-else-if="operation.phase === 'recovery_required'" class="progress-next-step"><strong>Action required on {{ environment }}’s Docker host</strong><p>Inspect <code>sudo /etc/p2pstream-server-updater/manage status</code> and resolve the reported host problem first. Then run <code>sudo /etc/p2pstream-server-updater/manage recover-update</code> to retry data recovery once.</p></div>
        <p v-else-if="operation.phase === 'succeeded'" class="progress-note">Use your normal route checks to verify public traffic. Management connectivity alone does not establish route health.</p>

        <NAlert v-if="operation.detail" :type="operation.phase === 'succeeded' ? 'warning' : tone === 'info' ? 'info' : tone" :title="operation.phase === 'succeeded' ? 'Update completed with a maintenance warning' : paused || operation.phase === 'rolling_back' ? 'Host-reported failure details' : 'Host-reported details'" class="progress-detail"><span>{{ operation.detail }}</span></NAlert>
      </div>
    </div>

    <footer class="progress-footer"><span class="progress-operation-id">Operation <code>{{ operation.id }}</code></span><NButton size="small" @click="copyDiagnostics"><template #icon><Check v-if="copyState === 'Diagnostics copied'" :size="14" /><Copy v-else :size="14" /></template>Copy diagnostics</NButton></footer>
    <p v-if="copyState" class="progress-copy-state" role="status">{{ copyState }}</p>
    <details ref="details" class="progress-diagnostics"><summary>Operation details</summary><pre>{{ diagnostics }}</pre></details>
  </section>
</template>

<style scoped>
.update-progress{--progress-color:var(--app-accent);border:1px solid var(--app-border);border-top:3px solid var(--progress-color);border-radius:8px;background:var(--app-panel);overflow:hidden}.update-progress--success{--progress-color:var(--app-success)}.update-progress--warning{--progress-color:var(--app-warning)}.update-progress--error{--progress-color:var(--app-error)}
.progress-header{display:flex;justify-content:space-between;gap:1.5rem;padding:1.5rem;border-bottom:1px solid var(--app-border-subtle);background:linear-gradient(110deg,color-mix(in srgb,var(--progress-color) 5%,var(--app-panel)),var(--app-panel) 75%)}.progress-eyebrow{font-size:.65rem;font-weight:600;letter-spacing:.1em;color:var(--app-text-muted)}.progress-header h3{font-size:1.25rem;font-weight:600;display:flex;align-items:center;gap:.65rem;margin:.5rem 0 .7rem;line-height:1.35}.progress-header h3 svg{color:var(--progress-color);flex-shrink:0}.progress-versions{display:flex;align-items:center;gap:.6rem;font-family:var(--font-mono);font-size:.75rem;flex-wrap:wrap;color:var(--app-text-muted)}.progress-versions strong{font-weight:500;color:var(--app-text)}.progress-timing{display:grid;align-content:start;justify-items:end;gap:.25rem;flex-shrink:0}.progress-timing>span{font-size:.72rem;color:var(--app-text-muted)}.progress-timing strong{font:500 1.5rem/1.4 var(--font-mono)}.progress-timing small{font-size:.66rem;color:var(--app-text-muted)}
.progress-body{padding:1.5rem}.progress-body--with-stages{display:grid;grid-template-columns:220px minmax(0,1fr);gap:2rem}.progress-stages{margin:0;padding:0;list-style:none;display:grid;gap:0;align-content:start}.progress-stages li{position:relative;display:flex;align-items:center;gap:.75rem;min-height:2.3rem;font-size:.78rem;color:var(--app-text-muted)}.progress-stages li:not(:last-child)::after{content:'';position:absolute;top:calc(50% + 12px);bottom:calc(-50% + 12px);left:11px;width:1px;background:var(--app-border)}.stage-marker{display:grid;place-items:center;width:24px;height:24px;border:1px solid var(--app-border);border-radius:50%;font:400 .6rem var(--font-mono);flex-shrink:0;background:var(--app-panel)}.progress-stages .progress-stage--active{font-weight:600;color:var(--app-text)}.progress-stage--active .stage-marker{border-color:var(--progress-color);background:var(--app-accent-soft);color:var(--progress-color);box-shadow:0 0 0 3px color-mix(in srgb,var(--progress-color) 9%,transparent)}.progress-stage--complete .stage-marker{border-color:color-mix(in srgb,var(--app-success) 35%,var(--app-border));color:var(--app-success);background:color-mix(in srgb,var(--app-success) 6%,var(--app-panel))}
.progress-context{min-width:0}.progress-stage-meta{display:flex;align-items:center;flex-wrap:wrap;gap:.65rem}.progress-stage-meta>span{font-family:var(--font-mono);font-size:.65rem;color:var(--app-text-muted)}.progress-explanation{font-size:.9rem;line-height:1.65;margin:.8rem 0}.progress-note{font-size:.78rem;line-height:1.65;color:var(--app-text-muted);margin:.6rem 0}.progress-next-step{border-left:2px solid var(--progress-color);padding-left:1rem;margin:1.25rem 0}.progress-next-step>strong{font-size:.8rem}.progress-next-step p{font-size:.8rem;line-height:1.65;margin:.4rem 0}.progress-next-step code{font-size:.72rem;overflow-wrap:anywhere}.progress-detail{margin-top:1.25rem}.progress-detail span{white-space:pre-wrap;overflow-wrap:anywhere;font-size:.78rem}.progress-footer{border-top:1px solid var(--app-border-subtle);padding:.8rem 1.5rem;display:flex;align-items:center;justify-content:space-between;gap:1rem}.progress-operation-id{font-size:.68rem;color:var(--app-text-muted);overflow-wrap:anywhere;min-width:0}.progress-operation-id code{font-size:.65rem}.progress-copy-state{font-size:.75rem;margin:.3rem 1.5rem 1rem;color:var(--app-text-muted)}.progress-diagnostics{padding:.7rem 1.5rem;border-top:1px solid var(--app-border-subtle);font-size:.72rem;color:var(--app-text-muted)}.progress-diagnostics summary{cursor:pointer}.progress-diagnostics pre{font-size:.68rem;white-space:pre-wrap;overflow-wrap:anywhere;max-height:22rem;overflow:auto}.progress-spinner{animation:progress-rotate 2s linear infinite}.sr-only{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap}@keyframes progress-rotate{to{transform:rotate(360deg)}}
@media(prefers-reduced-motion:reduce){.progress-spinner{animation:none}}@media(max-width:760px){.progress-body--with-stages{grid-template-columns:175px minmax(0,1fr);gap:1rem}.progress-header{padding:1.2rem}.progress-header h3{font-size:1.1rem}.progress-timing strong{font-size:1.2rem}.progress-timing small{max-width:110px;text-align:right}.progress-body{padding:1.2rem}}@media(max-width:540px){.progress-header{flex-direction:column;gap:1rem}.progress-timing{justify-items:start;grid-template-columns:1fr auto;align-items:center;width:100%;gap:.1rem 1rem;border-top:1px solid var(--app-border-subtle);padding-top:.8rem}.progress-timing strong{grid-column:2;grid-row:1/3}.progress-timing small{max-width:none;text-align:left}.progress-body--with-stages{grid-template-columns:1fr;gap:1.25rem}.progress-stages{grid-template-columns:1fr 1fr;gap:.35rem 1rem}.progress-stages li{font-size:.7rem;min-height:1.9rem}.progress-stages li::after{display:none}.progress-stages li{gap:.5rem}.progress-footer{align-items:flex-start;flex-direction:column;padding:.8rem 1.2rem}.progress-diagnostics{padding:.7rem 1.2rem}.progress-header h3{font-size:1.1rem}.progress-versions{font-size:.68rem}}
</style>
