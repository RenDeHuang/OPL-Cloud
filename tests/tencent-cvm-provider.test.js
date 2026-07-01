import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { TencentCvmProvider } from "../services/api/src/runtime-providers/tencent-cvm.js";

const requiredEnv = {
  TENCENTCLOUD_SECRET_ID: "sid",
  TENCENTCLOUD_SECRET_KEY: "skey",
  TENCENTCLOUD_REGION: "ap-guangzhou",
  OPL_WORKSPACE_DOMAIN: "oplcloud.cn",
  OPL_VPC_ID: "vpc-123",
  OPL_SUBNET_ID: "subnet-123",
  OPL_SECURITY_GROUP_ID: "sg-123",
  OPL_AVAILABILITY_ZONE: "ap-guangzhou-6",
  OPL_IMAGE_ID: "img-123",
  OPL_SSH_KEY_ID: "skey-123",
  OPL_WORKSPACE_IMAGE: "harbor.example.com/opl/one-person-lab-webui:latest"
};

test("Tencent CVM provider default infraDir points at repository infra/tencent-cvm", () => {
  const provider = new TencentCvmProvider({ env: requiredEnv });

  assert.equal(provider.infraDir, join(process.cwd(), "infra", "tencent-cvm"));
});

test("Tencent CVM provider executes OpenTofu and Ansible, then maps outputs to Workspace runtime", async () => {
  const stateRootDir = await mkdtemp(join(tmpdir(), "opl-cloud-tencent-state-"));
  const calls = [];
  const runner = async ({ command, args, cwd, env }) => {
    calls.push({ command, args, cwd, env });
    if (command === "tofu" && args[0] === "output") {
      return JSON.stringify({
        server_id: { value: "ins-opl001" },
        disk_id: { value: "disk-opl001" },
        public_ip: { value: "203.0.113.10" },
        workspace_url: { value: "https://grant-lab-loud001.oplcloud.cn/?token=share_cloud" }
      });
    }
    return "";
  };

  const provider = new TencentCvmProvider({
    env: requiredEnv,
    runner,
    commandExists: () => true,
    infraDir: "/repo/infra/tencent-cvm",
    stateRootDir
  });

  try {
    const runtime = await provider.createWorkspaceRuntime({
      workspaceId: "ws-cloud001",
      ownerAccountId: "pi-alpha",
      workspaceName: "Grant Lab",
      packagePlan: { id: "basic", server: "2c4g", diskGb: 10 },
      token: "share_cloud"
    });

    assert.equal(runtime.provider, "tencent-cvm");
    assert.equal(runtime.server.id, "ins-opl001");
    assert.equal(runtime.server.status, "running");
    assert.equal(runtime.server.spec, "2c4g");
    assert.equal(runtime.docker.image, requiredEnv.OPL_WORKSPACE_IMAGE);
    assert.equal(runtime.docker.status, "running");
    assert.equal(runtime.disk.id, "disk-opl001");
    assert.equal(runtime.disk.sizeGb, 10);
    assert.equal(runtime.disk.mountPath, "/data");
    assert.equal(runtime.url, "https://grant-lab-loud001.oplcloud.cn/?token=share_cloud");
    assert.equal(runtime.slug, "grant-lab-loud001");

    const stateFile = join(stateRootDir, "ws-cloud001", "terraform.tfstate");
    assert.deepEqual(calls.map((call) => `${call.command} ${call.args.join(" ")}`), [
      "tofu init -input=false",
      `tofu apply -auto-approve -input=false -state=${stateFile} -state-out=${stateFile} -backup=${stateFile}.backup -var workspace_id=ws-cloud001 -var workspace_slug=grant-lab-loud001 -var workspace_token=share_cloud -var workspace_domain=oplcloud.cn -var owner_account_id=pi-alpha -var package_id=basic -var opl_image=harbor.example.com/opl/one-person-lab-webui:latest -var region=ap-guangzhou -var availability_zone=ap-guangzhou-6 -var image_id=img-123 -var vpc_id=vpc-123 -var subnet_id=subnet-123 -var security_group_id=sg-123 -var key_id=skey-123`,
      `tofu output -json -state=${stateFile} -show-sensitive`,
      "ansible-playbook -i 203.0.113.10, ansible/workspace.yml -u root --extra-vars workspace_id=ws-cloud001 workspace_slug=grant-lab-loud001 workspace_token=share_cloud workspace_domain=oplcloud.cn opl_image=harbor.example.com/opl/one-person-lab-webui:latest"
    ]);
  } finally {
    await rm(stateRootDir, { recursive: true, force: true });
  }
});

test("Tencent CVM provider fails closed when required tools are missing", async () => {
  const provider = new TencentCvmProvider({
    env: requiredEnv,
    commandExists: () => false
  });

  await assert.rejects(
    provider.createWorkspaceRuntime({
      workspaceId: "ws-cloud002",
      workspaceName: "No Tools Lab",
      packagePlan: { id: "basic", server: "2c4g", diskGb: 10 },
      token: "share_no_tools"
    }),
    /tencent_cvm_provider_missing_tools:tofu,ansible-playbook/
  );
});

test("Tencent CVM provider reports complete readiness gaps before cloud execution", async () => {
  const provider = new TencentCvmProvider({
    env: {},
    commandExists: (command) => command === "tofu"
  });

  const readiness = await provider.readiness();

  assert.deepEqual(readiness, {
    provider: "tencent-cvm",
    ready: false,
    missingEnv: [
      "TENCENTCLOUD_SECRET_ID",
      "TENCENTCLOUD_SECRET_KEY",
      "TENCENTCLOUD_REGION",
      "OPL_WORKSPACE_DOMAIN",
      "OPL_VPC_ID",
      "OPL_SUBNET_ID",
      "OPL_SECURITY_GROUP_ID",
      "OPL_AVAILABILITY_ZONE",
      "OPL_IMAGE_ID",
      "OPL_SSH_KEY_ID",
      "OPL_WORKSPACE_IMAGE"
    ],
    missingTools: ["ansible-playbook"]
  });
});

test("Tencent CVM provider isolates OpenTofu state outside infra source for each Workspace", async () => {
  const stateRootDir = await mkdtemp(join(tmpdir(), "opl-cloud-tencent-state-"));
  const calls = [];
  const runner = async ({ command, args }) => {
    calls.push({ command, args });
    if (command === "tofu" && args[0] === "output") {
      return JSON.stringify({
        server_id: { value: "ins-opl003" },
        disk_id: { value: "disk-opl003" },
        public_ip: { value: "203.0.113.30" },
        workspace_url: { value: "https://isolated-lab-loud003.oplcloud.cn/?token=share_isolated" }
      });
    }
    return "";
  };

  const provider = new TencentCvmProvider({
    env: requiredEnv,
    runner,
    commandExists: () => true,
    infraDir: "/repo/infra/tencent-cvm",
    stateRootDir
  });

  try {
    await provider.createWorkspaceRuntime({
      workspaceId: "ws-cloud003",
      ownerAccountId: "pi-alpha",
      workspaceName: "Isolated Lab",
      packagePlan: { id: "basic", server: "2c4g", diskGb: 10 },
      token: "share_isolated"
    });

    const stateFile = join(stateRootDir, "ws-cloud003", "terraform.tfstate");
    assert.deepEqual(calls.filter((call) => call.command === "tofu").map((call) => call.args), [
      ["init", "-input=false"],
      [
        "apply",
        "-auto-approve",
        "-input=false",
        `-state=${stateFile}`,
        `-state-out=${stateFile}`,
        `-backup=${stateFile}.backup`,
        "-var",
        "workspace_id=ws-cloud003",
        "-var",
        "workspace_slug=isolated-lab-loud003",
        "-var",
        "workspace_token=share_isolated",
        "-var",
        "workspace_domain=oplcloud.cn",
        "-var",
        "owner_account_id=pi-alpha",
        "-var",
        "package_id=basic",
        "-var",
        "opl_image=harbor.example.com/opl/one-person-lab-webui:latest",
        "-var",
        "region=ap-guangzhou",
        "-var",
        "availability_zone=ap-guangzhou-6",
        "-var",
        "image_id=img-123",
        "-var",
        "vpc_id=vpc-123",
        "-var",
        "subnet_id=subnet-123",
        "-var",
        "security_group_id=sg-123",
        "-var",
        "key_id=skey-123"
      ],
      ["output", "-json", `-state=${stateFile}`, "-show-sensitive"]
    ]);
  } finally {
    await rm(stateRootDir, { recursive: true, force: true });
  }
});
