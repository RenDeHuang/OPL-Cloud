import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { TencentCvmProvider } from "../../services/api/src/runtime-providers/tencent-cvm.js";

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
    const commandLines = calls.map((call) => `${call.command} ${call.args.join(" ")}`);
    assert.equal(commandLines.join("\n").includes("share_cloud"), false);
    assert.deepEqual(commandLines, [
      "tofu init -input=false",
      `tofu apply -auto-approve -input=false -state=${stateFile} -state-out=${stateFile} -backup=${stateFile}.backup -var-file=${join(stateRootDir, "ws-cloud001", "workspace.auto.tfvars.json")}`,
      `tofu output -json -state=${stateFile} -show-sensitive`,
      `ansible-playbook -i 203.0.113.10, ansible/workspace.yml -u root --extra-vars @${join(stateRootDir, "ws-cloud001", "ansible-vars.json")}`
    ]);

    const tfvars = JSON.parse(await readFile(join(stateRootDir, "ws-cloud001", "workspace.auto.tfvars.json"), "utf8"));
    assert.equal(tfvars.workspace_token, "share_cloud");
    assert.equal(tfvars.workspace_id, "ws-cloud001");
    const ansibleVars = JSON.parse(await readFile(join(stateRootDir, "ws-cloud001", "ansible-vars.json"), "utf8"));
    assert.equal(ansibleVars.workspace_token, "share_cloud");
    assert.equal(ansibleVars.workspace_slug, "grant-lab-loud001");
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
    /tencent_cvm_provider_missing_tools:tofu,ansible-playbook,tccli/
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
    missingTools: ["ansible-playbook", "tccli"]
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
    const commandLines = calls.map((call) => `${call.command} ${call.args.join(" ")}`);
    assert.equal(commandLines.join("\n").includes("share_isolated"), false);
    assert.deepEqual(calls.filter((call) => call.command === "tofu").map((call) => call.args), [
      ["init", "-input=false"],
      [
        "apply",
        "-auto-approve",
        "-input=false",
        `-state=${stateFile}`,
        `-state-out=${stateFile}`,
        `-backup=${stateFile}.backup`,
        `-var-file=${join(stateRootDir, "ws-cloud003", "workspace.auto.tfvars.json")}`
      ],
      ["output", "-json", `-state=${stateFile}`, "-show-sensitive"]
    ]);
  } finally {
    await rm(stateRootDir, { recursive: true, force: true });
  }
});

test("Tencent CVM provider stops and starts server billing without touching retained disk", async () => {
  const calls = [];
  const provider = new TencentCvmProvider({
    env: requiredEnv,
    runner: async ({ command, args }) => {
      calls.push({ command, args });
      return "";
    },
    commandExists: () => true
  });
  const workspace = {
    server: { id: "ins-opl101", status: "running", billingStatus: "active", spec: "2c4g" },
    disk: { id: "disk-opl101", status: "attached_retained", billingStatus: "active" }
  };

  const stopped = await provider.stopServer({ workspace });
  const restarted = await provider.restartServer({ workspace: { ...workspace, server: stopped } });

  assert.deepEqual(calls.map((call) => `${call.command} ${call.args.join(" ")}`), [
    'tccli cvm StopInstances --region ap-guangzhou --InstanceIds ["ins-opl101"] --StoppedMode STOP_CHARGING',
    'tccli cvm StartInstances --region ap-guangzhou --InstanceIds ["ins-opl101"]'
  ]);
  assert.equal(stopped.status, "stopped");
  assert.equal(stopped.billingStatus, "stopped");
  assert.equal(restarted.status, "running");
  assert.equal(restarted.billingStatus, "active");
});

test("Tencent CVM provider destroys server while retaining CBS disk", async () => {
  const calls = [];
  const provider = new TencentCvmProvider({
    env: requiredEnv,
    runner: async ({ command, args }) => {
      calls.push({ command, args });
      return "";
    },
    commandExists: () => true
  });

  const server = await provider.destroyServer({
    workspace: {
      server: { id: "ins-opl201", status: "running", billingStatus: "active", spec: "8c16g" },
      disk: { id: "disk-opl201", status: "attached_retained", billingStatus: "active" }
    }
  });

  assert.deepEqual(calls.map((call) => `${call.command} ${call.args.join(" ")}`), [
    'tccli cvm StopInstances --region ap-guangzhou --InstanceIds ["ins-opl201"] --StoppedMode STOP_CHARGING',
    'tccli cbs DetachDisks --region ap-guangzhou --DiskIds ["disk-opl201"]',
    'tccli cvm TerminateInstances --region ap-guangzhou --InstanceIds ["ins-opl201"]'
  ]);
  assert.equal(server.status, "destroyed");
  assert.equal(server.billingStatus, "stopped");
});

test("Tencent CVM provider recreates a destroyed server and reattaches the retained CBS disk", async () => {
  const stateRootDir = await mkdtemp(join(tmpdir(), "opl-cloud-tencent-state-"));
  const calls = [];
  const runner = async ({ command, args }) => {
    calls.push({ command, args });
    if (command === "tccli" && args[1] === "RunInstances") {
      return JSON.stringify({ InstanceIdSet: ["ins-opl202"] });
    }
    if (command === "tccli" && args[1] === "DescribeInstances") {
      return JSON.stringify({ InstanceSet: [{ InstanceId: "ins-opl202", PublicIpAddresses: ["203.0.113.202"] }] });
    }
    return "";
  };
  const provider = new TencentCvmProvider({
    env: requiredEnv,
    runner,
    commandExists: () => true,
    stateRootDir
  });

  try {
    const server = await provider.recreateServer({
      workspace: {
        id: "ws-cloud202",
        ownerAccountId: "pi-alpha",
        packageId: "pro",
        slug: "recreate-lab-cloud202",
        access: { token: "share_recreate" },
        server: { id: "ins-old202", status: "destroyed", billingStatus: "stopped", spec: "8c16g" },
        docker: { image: requiredEnv.OPL_WORKSPACE_IMAGE },
        disk: { id: "disk-opl202", status: "detached_retained", billingStatus: "active" }
      }
    });

    const commandLines = calls.map((call) => `${call.command} ${call.args.join(" ")}`);
    assert.equal(commandLines.join("\n").includes("share_recreate"), false);
    assert.deepEqual(commandLines, [
      'tccli cvm RunInstances --region ap-guangzhou --Placement {"Zone":"ap-guangzhou-6"} --ImageId img-123 --InstanceType SA5.2XLARGE16 --InstanceChargeType POSTPAID_BY_HOUR --VirtualPrivateCloud {"VpcId":"vpc-123","SubnetId":"subnet-123"} --SecurityGroupIds ["sg-123"] --InternetAccessible {"InternetMaxBandwidthOut":5,"PublicIpAssigned":true} --SystemDisk {"DiskType":"CLOUD_BSSD","DiskSize":50} --InstanceName opl-recreate-lab-cloud202 --LoginSettings {"KeyIds":["skey-123"]}',
      'tccli cbs AttachDisks --region ap-guangzhou --DiskIds ["disk-opl202"] --InstanceId ins-opl202',
      'tccli cvm DescribeInstances --region ap-guangzhou --InstanceIds ["ins-opl202"]',
      `ansible-playbook -i 203.0.113.202, ansible/workspace.yml -u root --extra-vars @${join(stateRootDir, "ws-cloud202", "ansible-vars.json")}`
    ]);
    assert.equal(server.id, "ins-opl202");
    assert.equal(server.status, "running");
    assert.equal(server.billingStatus, "active");
    assert.equal(server.publicIp, "203.0.113.202");
  } finally {
    await rm(stateRootDir, { recursive: true, force: true });
  }
});

test("Tencent CVM provider destroys CBS disk only through explicit disk lifecycle action", async () => {
  const calls = [];
  const provider = new TencentCvmProvider({
    env: requiredEnv,
    runner: async ({ command, args }) => {
      calls.push({ command, args });
      return "";
    },
    commandExists: () => true
  });

  const disk = await provider.destroyDisk({
    workspace: {
      server: { id: "ins-opl301", status: "destroyed", billingStatus: "stopped" },
      disk: { id: "disk-opl301", status: "detached_retained", billingStatus: "active", sizeGb: 100 }
    }
  });

  assert.deepEqual(calls.map((call) => `${call.command} ${call.args.join(" ")}`), [
    'tccli cbs TerminateDisks --region ap-guangzhou --DiskIds ["disk-opl301"]'
  ]);
  assert.equal(disk.status, "destroyed");
  assert.equal(disk.billingStatus, "stopped");
});
