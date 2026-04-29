import { createRouter, createWebHashHistory } from 'vue-router';
import Overview from './views/Overview.vue';
import Traffic from './views/Traffic.vue';
import AgentHealth from './views/AgentHealth.vue';
import Management from './views/Management.vue';

const routes = [
  { path: '/', redirect: '/overview' },
  { path: '/overview', name: 'overview', component: Overview },
  { path: '/traffic', name: 'traffic', component: Traffic },
  { path: '/agent', name: 'agent', component: AgentHealth },
  { path: '/management', name: 'management', component: Management },
];

export const router = createRouter({
  history: createWebHashHistory(),
  routes,
});
