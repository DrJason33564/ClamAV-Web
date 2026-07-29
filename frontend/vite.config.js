import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";

const webRoot = fileURLToPath(new URL("../server/web", import.meta.url));

export default defineConfig({
  root: webRoot,
  publicDir: "public",
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      "/api": {
        target: process.env.VITE_API_PROXY_TARGET || "http://127.0.0.1:8080",
        changeOrigin: true
      }
    }
  },
  build: {
    outDir: "dist",
    emptyOutDir: true
  }
});
