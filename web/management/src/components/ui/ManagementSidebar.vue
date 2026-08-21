<script setup lang="ts">
import { ref, type Component } from "vue";
import {
  Activity as ActivityIcon,
  Bot as AgentIcon,
  ChevronRight as ChevronRightIcon,
  FileCode2 as TemplateIcon,
  Gauge as OverviewIcon,
  KeyRound as TokenIcon,
  LockKeyhole as TlsIcon,
  Network as ProxyIcon,
  PanelLeftClose as CollapseIcon,
  PanelLeftOpen as ExpandIcon,
  RadioTower as MonitorIcon,
  Settings2 as EnvironmentIcon,
  ShieldCheck as PolicyIcon,
} from "@lucide/vue";
import { useRoute } from "vue-router";
import {
  MANAGEMENT_NAVIGATION,
  isManagementNavigationItemActive,
  type ManagementNavigationItem,
} from "@/lib/managementNavigation";

const props = defineProps<{
  collapsed: boolean;
  mobileOpen: boolean;
  username: string;
}>();

const emit = defineEmits<{
  (event: "close-mobile"): void;
  (event: "toggle-collapsed"): void;
}>();

const route = useRoute();
const sidebarElement = ref<HTMLElement | null>(null);

const icons: Record<string, Component> = {
  overview: OverviewIcon,
  monitor: MonitorIcon,
  proxy: ProxyIcon,
  agents: AgentIcon,
  "traffic-policy": PolicyIcon,
  templates: TemplateIcon,
  tls: TlsIcon,
  environments: EnvironmentIcon,
  "api-tokens": TokenIcon,
  "management-tls": TlsIcon,
};

function itemIcon(item: ManagementNavigationItem): Component {
  return icons[item.key] ?? ActivityIcon;
}

function isActive(item: ManagementNavigationItem): boolean {
  return isManagementNavigationItemActive(item, route.path);
}

function trapMobileFocus(event: KeyboardEvent) {
  if (!props.mobileOpen || event.key !== "Tab" || !sidebarElement.value) return;
  const focusable = Array.from(sidebarElement.value.querySelectorAll<HTMLElement>(
    'a[href], button:not([disabled]), [tabindex]:not([tabindex="-1"])',
  )).filter((element) => element.getClientRects().length > 0 && !element.hasAttribute("inert"));
  const first = focusable[0];
  const last = focusable.at(-1);
  if (!first || !last) return;
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault();
    last.focus();
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault();
    first.focus();
  }
}
</script>

<template>
  <aside
    ref="sidebarElement"
    id="management-navigation"
    class="app-sidebar"
    :class="{
      'app-sidebar--collapsed': collapsed,
      'app-sidebar--mobile-open': mobileOpen,
    }"
    aria-label="Management navigation"
    @keydown="trapMobileFocus"
  >
    <div class="app-sidebar__brand-row">
      <router-link to="/overview" class="app-sidebar__brand" aria-label="p2pstream overview" @click="emit('close-mobile')">
        <span class="app-brand__mark" aria-hidden="true"><span class="app-brand__diamond" /></span>
        <span class="app-sidebar__brand-name">p2pstream</span>
      </router-link>
      <button type="button" class="app-sidebar__mobile-close" aria-label="Close navigation" @click="emit('close-mobile')">
        <ChevronRightIcon aria-hidden="true" />
      </button>
    </div>

    <div v-if="$slots.environment" class="app-sidebar__mobile-environment">
      <slot name="environment" />
    </div>

    <nav class="app-sidebar__nav">
      <section v-for="group in MANAGEMENT_NAVIGATION" :key="group.key" class="app-sidebar__group" :aria-label="group.label">
        <h2 class="app-sidebar__group-label">{{ group.label }}</h2>
        <ul class="app-sidebar__list">
          <li v-for="item in group.items" :key="item.key" class="app-sidebar__item">
            <router-link
              :to="item.path"
              class="app-sidebar__link"
              :class="{ 'app-sidebar__link--active': isActive(item), 'app-nav__link--active': isActive(item) }"
              :aria-current="route.path === item.path ? 'page' : undefined"
              :aria-label="item.label"
              :title="collapsed ? item.label : undefined"
              @click="emit('close-mobile')"
            >
              <component :is="itemIcon(item)" class="app-sidebar__icon" aria-hidden="true" />
              <span class="app-sidebar__label">{{ item.label }}</span>
              <ChevronRightIcon v-if="item.children" class="app-sidebar__chevron" aria-hidden="true" />
            </router-link>

            <ul v-if="item.children" class="app-sidebar__sublist">
              <li v-for="child in item.children" :key="child.key">
                <router-link
                  :to="child.path"
                  class="app-sidebar__sublink"
                  :class="{ 'app-sidebar__sublink--active': route.path === child.path }"
                  :aria-current="route.path === child.path ? 'page' : undefined"
                  @click="emit('close-mobile')"
                >
                  {{ child.label }}
                </router-link>
              </li>
            </ul>
          </li>
        </ul>
      </section>
    </nav>

    <div class="app-sidebar__footer">
      <div class="app-sidebar__operator" :title="collapsed ? username : undefined">
        <span class="app-sidebar__avatar" aria-hidden="true">{{ username.slice(0, 1).toUpperCase() }}</span>
        <span class="app-sidebar__operator-copy">
          <small>Signed in as</small>
          <strong>{{ username }}</strong>
        </span>
      </div>
      <button
        type="button"
        class="app-sidebar__collapse"
        :aria-label="collapsed ? 'Expand navigation' : 'Collapse navigation'"
        :title="collapsed ? 'Expand navigation' : 'Collapse navigation'"
        @click="emit('toggle-collapsed')"
      >
        <component :is="collapsed ? ExpandIcon : CollapseIcon" aria-hidden="true" />
        <span>{{ collapsed ? "Expand" : "Collapse" }}</span>
      </button>
    </div>
  </aside>
</template>
