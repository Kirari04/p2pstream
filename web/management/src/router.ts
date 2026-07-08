import { createRouter, createWebHashHistory } from 'vue-router';

const Overview = () => import('./views/Overview.vue');
const Diagnostics = () => import('./views/Diagnostics.vue');
const Traffic = () => import('./views/Traffic.vue');
const Monitor = () => import('./views/Monitor.vue');
const AgentHealth = () => import('./views/AgentHealth.vue');
const Settings = () => import('./views/Settings.vue');
const SettingsApiTokens = () => import('./views/SettingsApiTokens.vue');
const SettingsEnvironments = () => import('./views/SettingsEnvironments.vue');
const ProxyConfig = () => import('./views/ProxyConfig.vue');
const TrafficPolicies = () => import('./views/TrafficPolicies.vue');
const ResponseTemplates = () => import('./views/ResponseTemplates.vue');
const TlsConfig = () => import('./views/TlsConfig.vue');
const NotFound = () => import('./views/NotFound.vue');

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
