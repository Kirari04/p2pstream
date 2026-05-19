import { createRouter, createWebHashHistory } from 'vue-router';
import Overview from './views/Overview.vue';
import Traffic from './views/Traffic.vue';
import AgentHealth from './views/AgentHealth.vue';
import Settings from './views/Settings.vue';
import SettingsApiTokens from './views/SettingsApiTokens.vue';
import SettingsEnvironments from './views/SettingsEnvironments.vue';
import ProxyConfig from './views/ProxyConfig.vue';
import TrafficPolicies from './views/TrafficPolicies.vue';
import ResponseTemplates from './views/ResponseTemplates.vue';
import TlsConfig from './views/TlsConfig.vue';

const routes = [
  { path: '/', redirect: '/overview' },
  { path: '/overview', name: 'overview', component: Overview },
  { path: '/traffic', name: 'traffic', component: Traffic },
  { path: '/agent', name: 'agent', component: AgentHealth },
  {
    path: '/settings',
    component: Settings,
    redirect: '/settings/environments',
    children: [
      { path: 'environments', name: 'settings-environments', component: SettingsEnvironments },
      { path: 'api-tokens', name: 'settings-api-tokens', component: SettingsApiTokens },
    ],
  },
  { path: '/environments', redirect: '/settings/environments' },
  { path: '/proxy', name: 'proxy', component: ProxyConfig },
  { path: '/policies', name: 'policies', component: TrafficPolicies },
  { path: '/templates', name: 'templates', component: ResponseTemplates },
  { path: '/tls', name: 'tls', component: TlsConfig },
  { path: '/management', redirect: '/proxy' },
];

export const router = createRouter({
  history: createWebHashHistory(),
  routes,
});
