import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

const consoleApiOrigin = process.env.OPL_CONSOLE_API_ORIGIN || "http://127.0.0.1:8787";

export default defineConfig({
  plugins: [react()],
  build: {
    chunkSizeWarningLimit: 1500,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes("node_modules")) return undefined;
          if (id.includes("react") || id.includes("@openai/apps-sdk-ui")) return "react-ui";
          if (id.includes("lucide-react")) return "icons";
          return "vendor";
        }
      }
    }
  },
  server: {
    port: 5173,
    proxy: {
      "/api": consoleApiOrigin
    }
  }
});
