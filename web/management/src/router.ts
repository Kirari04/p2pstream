import { createRouter, createWebHashHistory } from 'vue-router';
import Overview from './views/Overview.vue';
import Traffic from './views/Traffic.vue';
import AgentHealth from './views/AgentHealth.vue';
import ProxyConfig from './views/ProxyConfig.vue';
import TrafficPolicies from './views/TrafficPolicies.vue';
import TlsConfig from './views/TlsConfig.vue';

const routes = [
  { path: '/', redirect: '/overview' },
  { path: '/overview', name: 'overview', component: Overview },
  { path: '/traffic', name: 'traffic', component: Traffic },
  { path: '/agent', name: 'agent', component: AgentHealth },
  { path: '/proxy', name: 'proxy', component: ProxyConfig },
  { path: '/policies', name: 'policies', component: TrafficPolicies },
  { path: '/tls', name: 'tls', component: TlsConfig },
  { path: '/management', redirect: '/proxy' },
];

export const router = createRouter({
  history: createWebHashHistory(),
  routes,
});
