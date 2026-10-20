import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  server: {
    port: 5173,
    proxy: {
      "/api": { target: "http://localhost:8080" },
      "/ingest": { target: "http://localhost:8080" },
      "/ws": { target: "ws://localhost:8080", ws: true },
    },
  },
  build: {
    sourcemap: true,
  },
});
