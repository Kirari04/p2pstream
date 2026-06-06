import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";
import tailwindcss from "@tailwindcss/vite";

const managementProxyTarget = process.env.VITE_MANAGEMENT_PROXY_TARGET ?? "https://127.0.0.1:8081";
const managementProxySecure = process.env.VITE_MANAGEMENT_PROXY_SECURE === "true";
const hmrProtocol = process.env.VITE_HMR_PROTOCOL ?? "wss";
const hmrHost = process.env.VITE_HMR_HOST ?? "localhost";
const hmrClientPort = Number.parseInt(process.env.VITE_HMR_CLIENT_PORT ?? "8081", 10);

export default defineConfig({
  plugins: [vue(), tailwindcss()],
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  server: {
    host: "127.0.0.1",
    port: 5173,
    strictPort: true,
    proxy: {
      "/p2pstream.v1.AgentManagementService": {
        target: managementProxyTarget,
        changeOrigin: true,
        secure: managementProxySecure,
      },
      "/environments/": {
        target: managementProxyTarget,
        changeOrigin: true,
        secure: managementProxySecure,
      },
    },
    hmr: {
      protocol: hmrProtocol,
      host: hmrHost,
      clientPort: Number.isFinite(hmrClientPort) ? hmrClientPort : 8081,
    },
  },
});
