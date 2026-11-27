import { createHash } from "node:crypto";
import { existsSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";
import { fileURLToPath, URL } from "node:url";

function stampServiceWorkerVersion() {
  let outDir = "dist";
  return {
    name: "teamie-stamp-sw-version",
    configResolved(config: { build: { outDir: string } }) {
      outDir = config.build.outDir;
    },
    closeBundle() {
      const swPath = join(outDir, "sw.js");
      if (!existsSync(swPath)) return;
      const hash = createHash("sha256");
      const indexPath = join(outDir, "index.html");
      if (existsSync(indexPath)) {
        hash.update(readFileSync(indexPath));
      }
      const assetsDir = join(outDir, "assets");
      const precacheAssets: string[] = [];
      if (existsSync(assetsDir)) {
        for (const name of readdirSync(assetsDir).sort()) {
          hash.update(name);
          if (name.endsWith(".js") || name.endsWith(".css")) {
            precacheAssets.push(`/assets/${name}`);
          }
        }
      }
      const version = hash.digest("hex").slice(0, 12);
      const source = readFileSync(swPath, "utf8");
      const stamped = source
        .replace("__TEAMIE_SW_VERSION__", version)
        .replace(
          "/*__TEAMIE_SW_ASSETS__*/ []",
          JSON.stringify(precacheAssets),
        );
      writeFileSync(swPath, stamped, "utf8");
    },
  };
}

export default defineConfig({
  plugins: [react(), stampServiceWorkerVersion()],
  build: {
    chunkSizeWarningLimit: 500,
    rolldownOptions: {
      output: {
        manualChunks(id) {
          const normalized = id.replaceAll("\\", "/");
          if (normalized.includes("/node_modules/katex/")) {
            return "katex-vendor";
          }
          if (normalized.includes("/node_modules/lucide-react/")) {
            return "icons-vendor";
          }
          if (
            /\/node_modules\/(react|react-dom|react-router|react-router-dom|scheduler|@tanstack\/react-query)\//.test(
              normalized,
            )
          ) {
            return "react-vendor";
          }
        },
      },
    },
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
