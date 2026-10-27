import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { fileURLToPath, URL } from "node:url";
export default defineConfig({
  plugins: [react()],
  build: {
    chunkSizeWarningLimit: 1000,
  },
  test: {
    testTimeout: 15000,
    maxWorkers: 4,
    execArgv: process.allowedNodeEnvironmentFlags.has(
      "--no-experimental-webstorage",
    )
      ? ["--no-experimental-webstorage"]
      : [],
  },
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
      "^/api/v1/conversations/[^/]+/typing(?:[/?]|$)": {
        target: process.env.REALTIME_HTTP_ORIGIN || "http://localhost:8083",
      },
      "^/api/v1/workspaces/[^/]+/presence(?:[/?]|$)": {
        target: process.env.REALTIME_HTTP_ORIGIN || "http://localhost:8083",
      },
      "/api/v1/agents": {
        target: process.env.CONTROLPLANE_HTTP_ORIGIN || "http://localhost:8084",
      },
      "^/api/v1/workspaces/[^/]+/agents(?:[/?]|$)": {
        target: process.env.CONTROLPLANE_HTTP_ORIGIN || "http://localhost:8084",
      },
      "/api/v1/admin/model-gateway": {
        target: process.env.GATEWAY_HTTP_ORIGIN || "http://localhost:8086",
      },
      "/api/v1/model-gateway": {
        target: process.env.GATEWAY_HTTP_ORIGIN || "http://localhost:8086",
      },
      "/api": {
        target: process.env.CHAT_HTTP_ORIGIN || "http://localhost:8082",
      },
    },
  },
});
