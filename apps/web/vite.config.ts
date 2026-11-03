import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

const proxyTarget = process.env.METRIC_PROXY_TARGET || "http://localhost:8080";
const websocketTarget = proxyTarget.replace(/^http/, "ws");
const proxy = {
  "/api": { target: proxyTarget },
  "/ingest": { target: proxyTarget },
  "/ws": { target: websocketTarget, ws: true },
};

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    proxy,
  },
  preview: { proxy },
  build: {
    sourcemap: true,
  },
});
