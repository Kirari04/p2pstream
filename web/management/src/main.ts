import { createApp } from "vue";
import PrimeVue from "primevue/config";
import ToastService from "primevue/toastservice";
import App from "./App.vue";
import { router } from "./router";
import "./styles.css";

const app = createApp(App);

app.use(PrimeVue, {
  unstyled: true,
});
app.use(ToastService);
app.use(router);

app.mount("#app");
