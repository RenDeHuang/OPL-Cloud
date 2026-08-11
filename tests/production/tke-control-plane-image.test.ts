import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("OPL Cloud product image contains all three control-service binaries", async () => {
  const dockerfile = await readFile("Dockerfile", "utf8");
  const dockerignore = (await readFile(".dockerignore", "utf8"))
    .split("\n")
    .map((line) => line.trim())
    .filter(Boolean);

  for (const [buildTarget, binary] of [
    ["./cmd/control-plane", "opl-control-plane"],
    ["./cmd/fabric", "opl-fabric"],
    ["./cmd/ledger", "opl-ledger"]
  ]) {
    assert.match(dockerfile, new RegExp(`go build -o /out/${binary.replaceAll("-", "\\-")} ${buildTarget.replaceAll("/", "\\/")}`));
    assert.match(dockerfile, new RegExp(`COPY --from=.* /out/${binary.replaceAll("-", "\\-")} /usr/local/bin/${binary.replaceAll("-", "\\-")}`));
  }
  assert.match(dockerfile, /ARG TARGETARCH/);
  assert.match(dockerfile, /linux\/\$\{TARGETARCH\}\/kubectl/);
  for (const ignored of ["node_modules", "dist", ".runtime", ".git", ".env"]) {
    assert.ok(dockerignore.includes(ignored), `.dockerignore missing ${ignored}`);
  }
});
