import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath, URL } from "node:url";
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@teamie/api-client": fileURLToPath(
        new URL("../../packages/api-client/src/index.ts", import.meta.url),
      ),
    },
  },
  server: {
    proxy: {
      "/api/v1/auth": {
        target: process.env.IDENTITY_HTTP_ORIGIN || "http://localhost:8081",
      },
      "/api/v1/ws": {
        target: process.env.REALTIME_HTTP_ORIGIN || "http://localhost:8083",
        ws: true,
      },
      "/api": {
        target: process.env.CHAT_HTTP_ORIGIN || "http://localhost:8082",
      },
    },
  },
});
