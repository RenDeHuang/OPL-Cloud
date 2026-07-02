import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

export default defineConfig({
  plugins: [react()],
  build: {
    modulePreload: {
      resolveDependencies(filename, deps, context) {
        if (context.hostType !== "html") return deps;
        return deps.filter((dep) => dep.includes("react-vendor"));
      }
    },
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes("node_modules")) return undefined;
          const packagePath = id.split("node_modules/").pop() || id;
          if (id.includes("/@ant-design/pro-")) return "pro-components";
          if (id.includes("/react/") || id.includes("/react-dom/") || id.includes("/scheduler/")) return "react-vendor";
          if (packagePath) return "vendor";
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
