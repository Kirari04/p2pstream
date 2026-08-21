<script setup lang="ts">
import { computed } from "vue";
import { Globe2 as GlobeIcon, ShieldAlert as ShieldAlertIcon } from "@lucide/vue";
import { NAlert, NButton, NButtonGroup, NSelect, NTag } from "naive-ui";
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
  <section class="geo-editor layout-grid space-lg round-md framed frame-standard muted-bg pad-lg" aria-labelledby="waf-geo-heading">
    <div class="layout-row align-start spread-items space-lg">
      <div class="layout-grid space-xs min-width-zero">
        <div class="layout-row align-center space-sm">
          <GlobeIcon class="icon-sm accent-text" aria-hidden="true" />
          <h4 id="waf-geo-heading" class="copy-sm weight-semibold base-text">Geographic targeting</h4>
        </div>
        <p class="copy-xs line-normal muted-text">
          Country is combined with the request match using AND. It narrows when this rule can act; it never skips later rules.
        </p>
      </div>
      <NTag v-if="enabled" size="small" :bordered="false" type="info">{{ selectedCountLabel }}</NTag>
    </div>

    <div class="layout-grid space-xs">
      <span class="copy-xs weight-medium label-case letter-wide muted-text">Apply action by country</span>
      <NButtonGroup class="geo-editor__modes layout-grid cols-one mq-sm-cols-three" size="small" role="group" aria-label="Geographic restriction mode">
        <NButton
          v-for="option in modeOptions"
          :key="option.value"
          attr-type="button"
          :disabled="disabled"
          :type="modelValue.mode === option.value ? 'primary' : 'default'"
          :aria-pressed="modelValue.mode === option.value"
          @click="updateMode(option.value)"
        >
          {{ option.label }}
        </NButton>
      </NButtonGroup>
      <p class="copy-xs line-normal muted-text">{{ modeDescription }}</p>
    </div>

    <template v-if="enabled">
      <NAlert v-if="geoIpEnabled === false" type="warning" :show-icon="false">
        GeoIP is disabled. Enable it and load a valid country database before enabling this rule.
      </NAlert>
      <NAlert v-else-if="geoIpReady === false" type="warning" :show-icon="false">
        No usable country database is loaded. This rule will treat every visitor as unknown until refresh succeeds.
      </NAlert>

      <label class="layout-grid space-xs copy-xs weight-medium label-case letter-wide muted-text">
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
        <span class="normal-text letter-normal weight-normal line-normal muted-text">
          Uses MaxMind's <span class="mono-text">country.iso_code</span>. GeoIP is approximate and is not proof of physical location.
        </span>
      </label>

      <div class="layout-grid space-sm">
        <div class="layout-grid space-2xs">
          <span class="copy-xs weight-medium label-case letter-wide muted-text">Unknown visitor</span>
          <p class="copy-xs line-normal muted-text">
            Includes unresolved trusted-proxy headers, private or special-use addresses, absent database records, and an unavailable database.
          </p>
        </div>
        <NButtonGroup class="geo-editor__unknown layout-grid cols-one mq-sm-cols-two" size="small" role="group" aria-label="Unknown country behavior">
          <NButton
            attr-type="button"
            :disabled="disabled"
            :type="modelValue.unknownBehavior === PublicWafGeoUnknownBehavior.APPLY_RULE ? 'primary' : 'default'"
            :aria-pressed="modelValue.unknownBehavior === PublicWafGeoUnknownBehavior.APPLY_RULE"
            @click="updateUnknownBehavior(PublicWafGeoUnknownBehavior.APPLY_RULE)"
          >
            Apply action (fail closed)
          </NButton>
          <NButton
            attr-type="button"
            :disabled="disabled"
            :type="modelValue.unknownBehavior === PublicWafGeoUnknownBehavior.BYPASS_RULE ? 'warning' : 'default'"
            :aria-pressed="modelValue.unknownBehavior === PublicWafGeoUnknownBehavior.BYPASS_RULE"
            @click="updateUnknownBehavior(PublicWafGeoUnknownBehavior.BYPASS_RULE)"
          >
            Bypass rule (fail open)
          </NButton>
        </NButtonGroup>
        <div v-if="modelValue.unknownBehavior === PublicWafGeoUnknownBehavior.BYPASS_RULE" class="geo-editor__fail-open layout-row align-start space-sm">
          <ShieldAlertIcon class="icon-sm warning-text no-shrink" aria-hidden="true" />
          <p class="copy-xs line-normal warning-text">
            Unknown visitors bypass this rule completely. Choose this only when availability is more important than enforcing the action during lookup failures.
          </p>
        </div>
      </div>
    </template>
  </section>
</template>

<style scoped>
.geo-editor__modes,
.geo-editor__unknown {
  width: 100%;
}

.geo-editor__modes :deep(.n-button),
.geo-editor__unknown :deep(.n-button) {
  width: 100%;
}

.geo-editor__fail-open {
  border: 1px solid color-mix(in srgb, var(--app-warning) 40%, var(--app-border));
  border-radius: 6px;
  background: color-mix(in srgb, var(--app-warning) 9%, var(--app-panel));
  padding: 0.75rem;
}
</style>
