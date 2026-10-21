<script setup lang="ts">
import { computed } from "vue";
import { ShieldAlert as ShieldAlertIcon } from "@lucide/vue";
import { NAlert, NSelect, NTag } from "naive-ui";
import SegmentedControl from "@/components/ui/SegmentedControl.vue";
import { countryOptions, isIsoCountryCode } from "@/lib/countryCatalog";
import type { WafGeoRestrictionForm } from "@/lib/publicWafGeoRestriction";
import {
  PublicWafGeoRestrictionMode,
  PublicWafGeoUnknownBehavior,
  PublicWafRuleAction,
} from "@/gen/proto/p2pstream/v1/management_pb";

const props = defineProps<{
  modelValue: WafGeoRestrictionForm;
  action: PublicWafRuleAction;
  geoIpEnabled?: boolean;
  geoIpReady?: boolean;
  disabled?: boolean;
}>();

const emit = defineEmits<{
  "update:modelValue": [value: WafGeoRestrictionForm];
}>();

const enabled = computed(() => props.modelValue.mode !== PublicWafGeoRestrictionMode.DISABLED);
const selectedCountryOptions = computed(() => {
  const options = [...countryOptions("en")];
  for (const code of props.modelValue.countryCodes) {
    if (isIsoCountryCode(code)) continue;
    options.push({ label: `Unrecognized assignment · ${code}`, value: code });
  }
  return options;
});
const selectedCountLabel = computed(() => {
  const count = props.modelValue.countryCodes.length;
  return `${count.toString()} ${count === 1 ? "country" : "countries"}`;
});
const actionVerb = computed(() => {
  switch (props.action) {
    case PublicWafRuleAction.CAPTCHA: return "challenge";
    case PublicWafRuleAction.WAITING_ROOM: return "queue";
    default: return "block";
  }
});
const modeOptions = computed(() => [
  {
    value: PublicWafGeoRestrictionMode.DISABLED,
    label: "All locations",
  },
  {
    value: PublicWafGeoRestrictionMode.SELECTED_COUNTRIES,
    label: `${titleCase(actionVerb.value)} selected`,
  },
  {
    value: PublicWafGeoRestrictionMode.OUTSIDE_SELECTED_COUNTRIES,
    label: props.action === PublicWafRuleAction.BLOCK
      ? "Allow selected only"
      : `${titleCase(actionVerb.value)} outside`,
  },
]);
const modeSegmentOptions = computed(() => modeOptions.value.map((option) => ({ ...option, disabled: props.disabled })));
const unknownBehaviorOptions = computed(() => [
  { label: "Apply action (fail closed)", value: PublicWafGeoUnknownBehavior.APPLY_RULE, disabled: props.disabled },
  { label: "Bypass rule (fail open)", value: PublicWafGeoUnknownBehavior.BYPASS_RULE, disabled: props.disabled },
]);
const modeDescription = computed(() => {
  switch (props.modelValue.mode) {
    case PublicWafGeoRestrictionMode.SELECTED_COUNTRIES:
      return `The rule can ${actionVerb.value} a request only when its known country is selected below.`;
    case PublicWafGeoRestrictionMode.OUTSIDE_SELECTED_COUNTRIES:
      return props.action === PublicWafRuleAction.BLOCK
        ? "Known countries in the selection pass this rule. Every other known country can be blocked. Later WAF rules still run."
        : `The rule can ${actionVerb.value} a request only when its known country is outside the selection.`;
    default:
      return "Country does not add another condition to this rule.";
  }
});

function updateMode(mode: PublicWafGeoRestrictionMode) {
  emitValue({ mode });
}

function updateCountryCodes(countryCodes: string[]) {
  emitValue({ countryCodes });
}

function updateUnknownBehavior(unknownBehavior: PublicWafGeoUnknownBehavior) {
  emitValue({ unknownBehavior });
}

function emitValue(patch: Partial<WafGeoRestrictionForm>) {
  emit("update:modelValue", {
    ...props.modelValue,
    ...patch,
    countryCodes: patch.countryCodes ? [...patch.countryCodes] : [...props.modelValue.countryCodes],
  });
}

function titleCase(value: string): string {
  return value.charAt(0).toUpperCase() + value.slice(1);
}
</script>

<template>
  <section class="geo-editor editor-group layout-grid space-lg" aria-labelledby="waf-geo-heading">
    <div class="layout-row align-start spread-items space-lg">
      <div class="layout-grid space-2xs min-width-zero">
        <h3 id="waf-geo-heading" class="copy-sm weight-semibold base-text">Geographic targeting</h3>
        <p class="copy-xs line-normal muted-text">
          Country is combined with the request match using AND. It narrows when this rule can act; it never skips later rules.
        </p>
      </div>
      <NTag v-if="enabled" size="small" :bordered="false">{{ selectedCountLabel }}</NTag>
    </div>

    <div class="layout-grid space-xs copy-xs weight-medium muted-text">
      Apply action by country
      <SegmentedControl
        :model-value="modelValue.mode"
        :options="modeSegmentOptions"
        label="Geographic restriction mode"
        @update:model-value="updateMode"
      />
      <span class="weight-normal line-normal muted-text">{{ modeDescription }}</span>
    </div>

    <template v-if="enabled">
      <NAlert v-if="geoIpEnabled === false" type="warning" :show-icon="false" :bordered="false">
        GeoIP is disabled. Enable it and load a valid country database before enabling this rule.
      </NAlert>
      <NAlert v-else-if="geoIpReady === false" type="warning" :show-icon="false" :bordered="false">
        No usable country database is loaded. This rule will treat every visitor as unknown until refresh succeeds.
      </NAlert>

      <label class="layout-grid space-xs copy-xs weight-medium muted-text">
        Countries
        <NSelect
          :value="modelValue.countryCodes"
          :input-props="{ 'aria-label': 'Countries for geographic targeting' }"
          :options="selectedCountryOptions"
          multiple
          filterable
          clearable
          max-tag-count="responsive"
          size="small"
          placeholder="Search countries or country codes"
          :disabled="disabled"
          @update:value="updateCountryCodes"
        />
        <span class="weight-normal line-normal muted-text">
          Uses MaxMind's <span class="mono-text">country.iso_code</span>. GeoIP is approximate and is not proof of physical location.
        </span>
      </label>

      <div class="layout-grid space-xs copy-xs weight-medium muted-text">
        Unknown visitor
        <SegmentedControl
          :model-value="modelValue.unknownBehavior"
          :options="unknownBehaviorOptions"
          label="Unknown country behavior"
          @update:model-value="updateUnknownBehavior"
        />
        <span class="weight-normal line-normal muted-text">
          Includes unresolved trusted-proxy headers, private or special-use addresses, absent database records, and an unavailable database.
        </span>
        <div v-if="modelValue.unknownBehavior === PublicWafGeoUnknownBehavior.BYPASS_RULE" class="geo-editor__fail-open layout-row align-start space-sm">
          <ShieldAlertIcon class="icon-sm warning-text no-shrink" aria-hidden="true" />
          <p class="copy-xs weight-normal line-normal warning-text">
            Unknown visitors bypass this rule completely. Choose this only when availability is more important than enforcing the action during lookup failures.
          </p>
        </div>
      </div>
    </template>
  </section>
</template>

<style scoped>
.editor-group {
  border-top: 1px solid var(--app-border-subtle);
  padding-top: 1.25rem;
}

/* Field labels stack from the top so helper text in a sibling column never stretches the input row. */
label.layout-grid {
  align-content: start;
}

/* Segmented controls keep their natural width inside stacked field groups. */
.segmented {
  justify-self: start;
}

:deep(.disabled-hint-full > .n-input-number) {
  flex: 1 1 auto;
}

.geo-editor__fail-open {
  margin-top: 0.25rem;
  border-radius: 8px;
  background: color-mix(in srgb, var(--app-warning) 9%, var(--app-panel));
  padding: 0.625rem 0.75rem;
}
</style>
