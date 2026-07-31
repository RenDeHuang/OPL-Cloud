import react from "@vitejs/plugin-react";
import type { Plugin as PostCSSPlugin } from "postcss";
import { defineConfig } from "vite";

const consoleApiOrigin = process.env.OPL_CONSOLE_API_ORIGIN || "http://127.0.0.1:8787";
const appsSdkKatexStyles = "/@openai/apps-sdk-ui/dist/es/styles/katex.min.css";

// This Console does not render Apps SDK Markdown/math, so its global CDN font CSS is not part of the production bundle.
function omitUnusedAppsSdkKatex(): PostCSSPlugin {
  return {
    postcssPlugin: "omit-unused-apps-sdk-katex",
    Once(root) {
      root.walk((node) => {
        const source = node.source?.input?.file?.replaceAll("\\", "/");
        if (source?.endsWith(appsSdkKatexStyles)) node.remove();
      });
    }
  };
}

export default defineConfig({
  plugins: [react()],
  css: {
    postcss: {
      plugins: [omitUnusedAppsSdkKatex()]
    }
  },
  build: {
    chunkSizeWarningLimit: 1500,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (id.includes("node_modules")) return "vendor";
          return undefined;
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
