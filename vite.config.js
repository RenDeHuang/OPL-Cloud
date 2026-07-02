import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react()],
  build: {
    modulePreload: false,
    chunkSizeWarningLimit: 1500,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes("node_modules")) return undefined;
          if (id.includes("/react/") || id.includes("/react-dom/") || id.includes("scheduler")) return "react-vendor";
          if (id.includes("@ant-design/pro-components") || id.includes("@ant-design/pro-")) return "antd-pro";
          if (
            id.includes("/antd/")
            || id.includes("@ant-design/")
            || id.includes("@rc-component/")
            || id.includes("/rc-")
            || id.includes("antd-style")
            || id.includes("/dayjs/")
          ) return "antd";
          if (id.includes("lucide-react") || id.includes("lucide")) return "icons";
          return "vendor";
        }
      }
    }
  },
  server: {
    port: 5173,
    proxy: {
      "/api": "http://127.0.0.1:8787"
    }
  }
});
