import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

export default defineConfig({
  plugins: [vue()],
  server: {
    host: "127.0.0.1",
    port: 5173,
    strictPort: true,
    proxy: {
      "/p2pstream.v1.AgentManagementService": "http://127.0.0.1:8081",
    },
    hmr: {
      protocol: "ws",
      host: "localhost",
      clientPort: 8081,
    },
  },
});
