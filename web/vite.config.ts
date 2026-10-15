import path from "path";
import { fileURLToPath } from "url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

// https://vite.dev/config/
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    rollupOptions: {
      output: {
        // Rarely changes between releases, so browsers keep it cached across updates.
        manualChunks(id) {
          if (/node_modules[\/](react|react-dom|react-router|react-router-dom|scheduler)[\/]/.test(id)) return "react";
        },
      },
    },
  },
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "src"),
    },
  },
  server: {
    // `npm run dev` talks to a locally running `ops-server serve`.
    proxy: {
      "/api": "http://127.0.0.1:8090",
      "/ws": { target: "ws://127.0.0.1:8090", ws: true },
    },
  },
});
