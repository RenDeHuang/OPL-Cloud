import { mkdir } from "node:fs/promises";
import { dirname, join } from "node:path";
import { spawn } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("..", import.meta.url));

export const defaultProvisionerBin = join(root, ".runtime", "opl-tencent-provisioner");

function run(command, args, cwd) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd, stdio: "pipe" });
    let stderr = "";
    child.stderr.on("data", (chunk) => {
      stderr += chunk.toString();
    });
    child.on("error", reject);
    child.on("close", (code) => {
      if (code === 0) resolve();
      else reject(new Error(`${command} ${args.join(" ")} failed:${stderr.trim()}`));
    });
  });
}

export async function buildTencentProvisioner({ binPath = defaultProvisionerBin } = {}) {
  await mkdir(dirname(binPath), { recursive: true });
  await run("go", ["build", "-o", binPath, "./cmd/opl-tencent-provisioner"], join(root, "services", "fabric"));
  return binPath;
}
