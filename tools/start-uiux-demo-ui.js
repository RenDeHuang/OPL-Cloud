import { spawn } from "node:child_process";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("..", import.meta.url));
const viteBin = join(root, "node_modules", "vite", "bin", "vite.js");
const port = process.env.PORT || "5178";
const apiOrigin = process.env.OPL_CONSOLE_API_ORIGIN || "http://127.0.0.1:8791";

const child = spawn(process.execPath, [
  viteBin,
  "--host",
  "0.0.0.0",
  "--port",
  String(port)
], {
  cwd: root,
  stdio: "inherit",
  env: {
    ...process.env,
    OPL_CONSOLE_API_ORIGIN: apiOrigin
  }
});

child.on("exit", (code, signal) => {
  if (signal) process.kill(process.pid, signal);
  process.exit(code ?? 0);
});
