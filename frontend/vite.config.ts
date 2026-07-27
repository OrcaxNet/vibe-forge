import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The shared contract lives at the repo root (contracts/contract.json) and is
// imported directly by src/contract.ts. Allow the Vite dev server to serve
// files from the repo root so that cross-root JSON import works in dev.
export default defineConfig({
  plugins: [react()],
  server: {
    host: true,
    port: 5173,
    fs: {
      allow: [".."],
    },
    proxy: {
      "/api": {
        target: process.env.VITE_BACKEND_URL ?? "http://127.0.0.1:8787",
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: "dist",
    sourcemap: true,
  },
});
