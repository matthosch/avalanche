import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The browser only ever talks to `api`; in dev, Vite proxies /api to it.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: { "/api": process.env.API_URL ?? "http://localhost:8080" },
  },
});
