import { createApp } from "vue";
import PrimeVue from "primevue/config";
import App from "./App.vue";
import { router } from "./router";
import "./styles.css";

const app = createApp(App);

app.use(PrimeVue, {
  unstyled: true,
});
app.use(router);

app.mount("#app");
