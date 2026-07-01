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

test("Tencent Ansible config mounts the retained CBS disk before starting Docker", async () => {
  const playbook = await readFile("infra/tencent-cvm/ansible/workspace.yml", "utf8");

  assert.match(playbook, /Find attached CBS data disk/);
  assert.match(playbook, /lsblk -ndo NAME,TYPE,MOUNTPOINT/);
  assert.match(playbook, /ansible\.builtin\.filesystem/);
  assert.match(playbook, /fstype: ext4/);
  assert.match(playbook, /dev: "\{\{ cbs_data_device\.stdout \}\}"/);
  assert.match(playbook, /ansible\.builtin\.mount/);
  assert.match(playbook, /path: \/data\/opl/);
  assert.match(playbook, /state: mounted/);
  assert.ok(playbook.indexOf("Find attached CBS data disk") < playbook.indexOf("Start OPL Docker runtime"));
});

test("Tencent Ansible config preserves one-person-lab-app WebUI data and projects directories", async () => {
  const playbook = await readFile("infra/tencent-cvm/ansible/workspace.yml", "utf8");

  assert.match(playbook, /AIONUI_DATA_DIR: \/data/);
  assert.match(playbook, /OPL_PROJECTS_DIR: \/projects/);
  assert.match(playbook, /- \/data\/opl\/data:\/data/);
  assert.match(playbook, /- \/data\/opl\/projects:\/projects/);
  assert.match(playbook, /"127\.0\.0\.1:3000:3000"/);
  assert.ok(playbook.indexOf("Mount CBS data disk for OPL Workspace data") < playbook.indexOf("Write Docker Compose file"));
});
