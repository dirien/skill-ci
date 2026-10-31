// Vite builds the UI into the controller's embed directory. `npm run dev` serves
// the fixtures under /api/v1/ (mock/fixtures.ts), or proxies /api and the stored
// report files to a controller when SKILLCI_API is set, for example
// SKILLCI_API=http://localhost:8082 after `make reports-ui`.
import react from "@vitejs/plugin-react";
import { loadEnv } from "vite";
import { defineConfig } from "vitest/config";

import { fixtureMock } from "./mock/fixtures.ts";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, ".", "SKILLCI_");
  const api = env.SKILLCI_API;
  return {
    plugins: [react(), ...(api ? [] : [fixtureMock()])],
    build: {
      outDir: "../controller/internal/reportsite/web/dist",
      emptyOutDir: true,
      assetsDir: "assets",
      sourcemap: false,
      // No inline scripts or styles: the controller's CSP allows only 'self'.
      assetsInlineLimit: 0,
    },
    server: {
      port: 5173,
      strictPort: true,
      proxy: api
        ? {
            "/api": { target: api, changeOrigin: true },
            "^/runs/[^/]+/(report|result\\.json)$": { target: api, changeOrigin: true },
          }
        : undefined,
    },
    test: {
      environment: "jsdom",
      setupFiles: ["./src/test/setup.ts"],
      include: ["src/**/*.test.{ts,tsx}"],
      css: false,
    },
  };
});
