import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("Tencent Ansible config installs and enforces the Caddy token-gated Workspace proxy", async () => {
  const [playbook, route] = await Promise.all([
    readFile("infra/tencent-cvm/ansible/workspace.yml", "utf8"),
    readFile("infra/tencent-cvm/ansible/Caddyfile.j2", "utf8")
  ]);

  assert.match(playbook, /- caddy/);
  assert.match(playbook, /dest: \/etc\/caddy\/Caddyfile/);
  assert.match(playbook, /import \/etc\/caddy\/conf\.d\/\*\.caddy/);
  assert.match(playbook, /systemctl enable --now caddy/);
  assert.doesNotMatch(playbook, /failed_when: false/);
  assert.match(route, /@missingToken not query token={{ workspace_token }}/);
  assert.match(route, /respond @missingToken "workspace token invalid" 403/);
  assert.match(route, /reverse_proxy 127\.0\.0\.1:3000/);
});
