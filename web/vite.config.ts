/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

// The Go server listens here during `make dev`.
const apiTarget = process.env.PGDOCK_API_URL ?? "http://127.0.0.1:8080";

export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: "dist",
    // dist/placeholder.html is committed; `make build` clears the rest.
    emptyOutDir: false,
    assetsDir: "assets",
  },
  server: {
    proxy: {
      "/api": apiTarget,
      "/healthz": apiTarget,
      "/readyz": apiTarget,
    },
  },
  test: {
    environment: "node",
  },
});
