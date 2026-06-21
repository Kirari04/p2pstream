import { createRouter, createWebHashHistory } from 'vue-router';
import Overview from './views/Overview.vue';
import Diagnostics from './views/Diagnostics.vue';
import Traffic from './views/Traffic.vue';
import Monitor from './views/Monitor.vue';
import AgentHealth from './views/AgentHealth.vue';
import Settings from './views/Settings.vue';
import SettingsApiTokens from './views/SettingsApiTokens.vue';
import SettingsEnvironments from './views/SettingsEnvironments.vue';
import ProxyConfig from './views/ProxyConfig.vue';
import TrafficPolicies from './views/TrafficPolicies.vue';
import ResponseTemplates from './views/ResponseTemplates.vue';
import TlsConfig from './views/TlsConfig.vue';
import NotFound from './views/NotFound.vue';

const routes = [
  { path: '/', redirect: '/overview' },
  { path: '/overview', name: 'overview', component: Overview },
  {
    path: '/monitor',
    component: Monitor,
    redirect: '/monitor/traffic',
    children: [
      { path: 'traffic', name: 'monitor-traffic', component: Traffic },
      { path: 'diagnostics', name: 'monitor-diagnostics', component: Diagnostics },
      { path: ':pathMatch(.*)*', redirect: '/monitor/traffic' },
    ],
  },
  { path: '/agent/:section(fleet|activity)?', name: 'agent', component: AgentHealth },
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
  { path: '/proxy', redirect: '/proxy/routes' },
  { path: '/proxy/:section(routes|listeners)', name: 'proxy', component: ProxyConfig },
  { path: '/proxy/:pathMatch(.*)*', redirect: '/proxy/routes' },
  { path: '/policies', redirect: '/policies/rate-limits' },
  { path: '/policies/:section(rate-limits|waf|cache|traffic-shaper)', name: 'policies', component: TrafficPolicies },
  { path: '/policies/:pathMatch(.*)*', redirect: '/policies/rate-limits' },
  { path: '/templates', name: 'templates', component: ResponseTemplates },
  { path: '/tls', name: 'tls', component: TlsConfig },
  { path: '/management', redirect: '/proxy/routes' },
  { path: '/:pathMatch(.*)*', name: 'not-found', component: NotFound },
];

export const router = createRouter({
  history: createWebHashHistory(),
  routes,
});
