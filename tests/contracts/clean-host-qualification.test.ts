import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { parse } from "yaml";

const workflowPath = new URL("../../.github/workflows/clean-host-qualification.yml", import.meta.url);

test("clean-host qualification gate is fail-closed and bound to the exact merged ref", async () => {
  const workflow = parse(await readFile(workflowPath, "utf8"));
  const trigger = workflow.on ?? workflow["on"];
  const job = workflow.jobs?.clean_host_qualification;
  const steps = job?.steps as Array<{
    name?: string;
    run?: string;
    uses?: string;
    with?: Record<string, unknown>;
    if?: string;
  }>;
  const stepRun = (name: string) => {
    const run = steps?.find((step) => step.name === name)?.run;
    assert.ok(run, `missing run script for ${name}`);
    return run;
  };

  assert.equal(job?.["runs-on"], "ubuntu-latest");
  assert.equal(job?.if, "${{ github.ref == 'refs/heads/main' }}");
  assert.equal(job?.environment, "preproduction");
  assert.equal(job?.env?.OPL_SUB2API_BASE_URL, "${{ vars.OPL_SUB2API_BASE_URL }}");
  assert.equal(job?.env?.OPL_SUB2API_ADMIN_EMAIL, "${{ secrets.OPL_SUB2API_ADMIN_EMAIL }}");
  assert.equal(job?.env?.OPL_SUB2API_ADMIN_PASSWORD, "${{ secrets.OPL_SUB2API_ADMIN_PASSWORD }}");
  assert.deepEqual(Object.keys(workflow.jobs), ["clean_host_qualification"]);
  assert.equal(trigger?.workflow_dispatch?.inputs?.ref?.required, true);
  assert.equal(trigger?.workflow_dispatch?.inputs?.confirmation?.required, true);
  assert.equal(trigger?.workflow_dispatch?.inputs?.sub2api_base_url, undefined);
  assert.equal(trigger?.workflow_dispatch?.inputs?.sub2api_admin_email, undefined);
  assert.equal(steps?.find((step) => step.name === "Checkout exact product source")?.with?.ref, "${{ inputs.ref }}");
  assert.equal(steps?.find((step) => step.name === "Checkout exact product source")?.with?.["persist-credentials"], false);

  const guardRun = stepRun("Validate clean-host inputs");
  const runtimeRun = stepRun("Qualify clean host workspace runtime");
  const residualCleanupStep = steps?.find((step) => step.name === "Remove exact clean-host workspace residuals");
  assert.ok(residualCleanupStep?.run, "missing exact Workspace residual cleanup step");
  assert.match(residualCleanupStep.if || "", /always\(\)/);
  const residualCleanupRun = residualCleanupStep.run;
  const quiesceCommand = 'docker compose --project-name "$COMPOSE_PROJECT_NAME" --env-file "$OPL_ENV_FILE" -f compose.yaml -f deploy/portable/compose.local-workspace.yaml -f "$OPL_COMPOSE_OVERRIDE_FILE" stop --timeout 30 control-plane fabric';
  const quiesceFallbackCommand = 'docker compose --project-name "$COMPOSE_PROJECT_NAME" --env-file "$OPL_ENV_FILE" -f compose.yaml -f deploy/portable/compose.local-workspace.yaml -f "$OPL_COMPOSE_OVERRIDE_FILE" kill control-plane fabric';
  const quiesceReadbackCommand = 'docker compose --project-name "$COMPOSE_PROJECT_NAME" --env-file "$OPL_ENV_FILE" -f compose.yaml -f deploy/portable/compose.local-workspace.yaml -f "$OPL_COMPOSE_OVERRIDE_FILE" ps --status running --services control-plane fabric';
  const quiesceIndex = residualCleanupRun.indexOf(quiesceCommand);
  const quiesceFallbackIndex = residualCleanupRun.indexOf(quiesceFallbackCommand);
  const quiesceReadbackIndex = residualCleanupRun.indexOf(quiesceReadbackCommand);
  assert.ok(quiesceIndex >= 0, "cleanup must quiesce exact-project control-plane and fabric services");
  assert.ok(quiesceFallbackIndex > quiesceIndex, "cleanup must use an exact-project fallback after graceful stop");
  assert.ok(quiesceReadbackIndex > quiesceFallbackIndex, "cleanup must authoritatively read back quiescence after the fallback");

  assert.match(guardRun, /BLOCKED_ENV/);
  assert.match(guardRun, /BLOCKED_PERMISSION/);
  assert.match(guardRun, /UPSTREAM_HANDOFF/);
  assert.match(
    guardRun,
    /handoff='UPSTREAM_HANDOFF: provide workflow_dispatch ref, cloud_image_ref, workspace_image_ref, and confirmation=RUN_ONE_REAL_BASIC_CLEAN_HOST_QUALIFICATION; configure preproduction variable OPL_SUB2API_BASE_URL and preproduction Secrets OPL_SUB2API_ADMIN_EMAIL and OPL_SUB2API_ADMIN_PASSWORD; rerun clean-host qualification from refs\/heads\/main with exact merged main artifacts only\.'/
  );
  assert.match(guardRun, /confirmation=RUN_ONE_REAL_BASIC_CLEAN_HOST_QUALIFICATION/);
  assert.match(guardRun, /\[ "\$QUALIFICATION_CONFIRMATION" != "RUN_ONE_REAL_BASIC_CLEAN_HOST_QUALIFICATION" \]/);
  assert.match(guardRun, /inputs\.ref/);
  assert.match(guardRun, /ghcr\.io\/\$PRODUCT_REPOSITORY@sha256:/);
  assert.match(guardRun, /git merge-base --is-ancestor "\$PRODUCT_SHA" refs\/remotes\/clean-host-source\/main/);
  assert.match(guardRun, /docker buildx imagetools inspect "\$CLOUD_IMAGE_REF"/);
  assert.match(guardRun, /docker pull "\$CLOUD_IMAGE_REF"/);
  assert.match(guardRun, /org\.opencontainers\.image\.revision/);
  assert.match(guardRun, /cloud_revision/);
  assert.match(guardRun, /docker buildx imagetools inspect "\$WORKSPACE_IMAGE_REF"/);
  assert.ok(guardRun.includes("\"$WORKSPACE_IMAGE_REF\" =~ ^.+@sha256:[0-9a-f]{64}$"));
  assert.match(guardRun, /COMPOSE_PROJECT_NAME=/);
  assert.match(guardRun, /OPL_LEDGER_CAPABILITY_KEY=/);
  assert.match(guardRun, /OPL_SUB2API_ADMIN_PASSWORD/);
  assert.match(guardRun, /OPL_COMPOSE_OVERRIDE_FILE=/);
  assert.match(guardRun, /OPL_WORKSPACE_ID_FILE=/);
  assert.match(guardRun, /umask 077/);
  assert.match(guardRun, /createHash\("sha1"\)/);
  assert.match(guardRun, /workspace-launch-v2/);
  assert.match(guardRun, /printf '%s\\n' "\$workspace_id" > "\$workspace_id_file"/);
  assert.match(guardRun, /cat > "\$override_file"/);
  assert.match(guardRun, /OPL_CONTROLLED_BASIC_PILOT_ENABLED: "1"/);
  assert.match(guardRun, /OPL_CONTROLLED_BASIC_PILOT_ACCOUNT_IDS: "acct-admin"/);
  assert.match(guardRun, /OPL_CONTROLLED_BASIC_PILOT_MAX_IN_FLIGHT: "1"/);
  assert.match(guardRun, /-f "\$override_file" config --quiet/);
  assert.doesNotMatch(guardRun, /# if ! \[\[ "\$WORKSPACE_IMAGE_REF"/);

  assert.match(runtimeRun, /docker compose --project-name "\$COMPOSE_PROJECT_NAME" --env-file "\$OPL_ENV_FILE" -f compose\.yaml -f deploy\/portable\/compose\.local-workspace\.yaml -f "\$OPL_COMPOSE_OVERRIDE_FILE" up -d --wait/);
  assert.match(runtimeRun, /const mePath = "\/api\/auth\/me";/);
  assert.match(runtimeRun, /const walletPath = "\/api\/gateway\/wallet";/);
  assert.match(runtimeRun, /const usageSummaryPath = "\/api\/gateway\/usage-summary\?period=month";/);
  assert.ok(runtimeRun.indexOf('const mePath = "/api/auth/me";') < runtimeRun.indexOf("const launchBody = {"));
  assert.ok(runtimeRun.indexOf('const walletPath = "/api/gateway/wallet";') < runtimeRun.indexOf("const launchBody = {"));
  assert.ok(runtimeRun.indexOf('const usageSummaryPath = "/api/gateway/usage-summary?period=month";') < runtimeRun.indexOf("const launchBody = {"));
  assert.match(runtimeRun, /\/api\/workspace-launches/);
  assert.match(runtimeRun, /\/api\/workspaces\/\$\{workspaceId\}\/runtime-status/);
  assert.match(runtimeRun, /\/w\/\$\{workspaceId\}\//);
  assert.match(
    runtimeRun,
    /BLOCKED_PERMISSION: Sub2API operator authentication rejected the clean-host credentials\.\\nUPSTREAM_HANDOFF: provide active reserved Cloud operator credentials that map to acct-admin, then rerun clean-host qualification\./
  );
  assert.match(
    runtimeRun,
    /BLOCKED_ENV: Sub2API source is unavailable for clean-host qualification\.\\nUPSTREAM_HANDOFF: restore the configured Sub2API endpoint and source health, then rerun clean-host qualification\./
  );
  assert.match(
    runtimeRun,
    /BLOCKED_PERMISSION: reserved Cloud operator balance is insufficient for the Basic 10 GB qualification launch\.\\nUPSTREAM_HANDOFF: fund the reserved acct-admin operator balance for the quoted Basic 10 GB charge, then rerun clean-host qualification\./
  );
  assert.match(runtimeRun, /const runtimeEnvironmentHandoff = "UPSTREAM_HANDOFF:/);
  assert.match(runtimeRun, /const blockedEnvironment = \(reason\) => fail\(`BLOCKED_ENV: \$\{reason\}\\n\$\{runtimeEnvironmentHandoff\}`\);/);
  assert.equal((runtimeRun.match(/await fetch\(/g) || []).length, 1, "all runtime HTTP must pass through the redacted request wrapper");
  assert.match(runtimeRun, /async function request[\s\S]*try \{[\s\S]*await fetch[\s\S]*await response\.text\(\)[\s\S]*\} catch \{[\s\S]*blockedEnvironment/);
  for (const reason of [
    "Runtime readiness readback failed.",
    "Pricing preview readback failed.",
    "Workspace launch request failed.",
    "Workspace launch identity readback failed.",
    "Workspace launch polling request failed.",
    "Workspace launch terminal readback failed.",
    "Workspace launch completion readback failed.",
    "Workspace launch list readback failed.",
    "Workspace readback failed.",
    "Workspace runtime readback failed.",
    "Workspace open readback failed.",
    "Workspace owner-authorized DELETE readback failed.",
    "Workspace delete absence readback failed.",
    "Workspace runtime delete readback failed."
  ]) {
    assert.ok(runtimeRun.includes(`"${reason}"`), `missing BLOCKED_ENV classification for ${reason}`);
  }
  assert.doesNotMatch(runtimeRun, /(?:fail|console\.error)\((?:`|"|')qualification_/);
  assert.match(runtimeRun, /launchAttempt\.response\.status === 502 && code === "upstream_unavailable"/);
  assert.match(runtimeRun, /String\(launch\.errorCode \|\| ""\) === "monthly_balance_insufficient"/);
  const workspaceKnownIndex = runtimeRun.indexOf("workspaceId = String(launch.workspaceId");
  const finallyIndex = runtimeRun.indexOf("} finally {");
  const deleteInFinallyIndex = runtimeRun.indexOf('method: "DELETE"', finallyIndex);
  assert.ok(workspaceKnownIndex >= 0, "runtime must record the exact Workspace ID once launch readback knows it");
  assert.ok(finallyIndex > workspaceKnownIndex, "runtime DELETE must be governed by a finally path after Workspace ID readback");
  assert.ok(deleteInFinallyIndex > finallyIndex, "owner-authorized DELETE must execute inside the finally path");
  assert.doesNotMatch(runtimeRun, /\/\/ DELETE \/api\/workspaces/);

  const diagnosticsRun = stepRun("Dump clean-host compose diagnostics");
  assert.match(residualCleanupRun, /opl\.fabric\.provider=local-docker/);
  assert.match(residualCleanupRun, /opl\.account\.id=acct-admin/);
  assert.match(residualCleanupRun, /opl\.workspace\.id=\$workspace_id/);
  assert.match(residualCleanupRun, /docker ps -aq --filter "label=\$provider_label" --filter "label=\$account_label" --filter "label=\$workspace_label"/);
  assert.match(residualCleanupRun, /docker volume ls -q --filter "label=\$provider_label" --filter "label=\$account_label" --filter "label=\$workspace_label"/);
  assert.match(residualCleanupRun, /docker network ls -q --filter "label=\$provider_label" --filter "label=\$account_label" --filter "label=\$workspace_label"/);
  assert.match(residualCleanupRun, /docker inspect/);
  assert.match(residualCleanupRun, /docker volume inspect/);
  assert.match(residualCleanupRun, /docker network inspect/);
  const containerRemovalIndex = residualCleanupRun.indexOf("docker rm -f");
  const volumeRemovalIndex = residualCleanupRun.indexOf("docker volume rm");
  const networkRemovalIndex = residualCleanupRun.indexOf("docker network rm");
  assert.ok(containerRemovalIndex >= 0, "cleanup must remove exact matching containers");
  assert.ok(quiesceReadbackIndex < containerRemovalIndex, "mutating Compose services must be confirmed stopped before container residual removal");
  assert.ok(volumeRemovalIndex > containerRemovalIndex, "cleanup must remove volumes after containers");
  assert.ok(networkRemovalIndex > volumeRemovalIndex, "cleanup must remove networks after volumes");
  assert.ok(quiesceReadbackIndex < volumeRemovalIndex, "quiescence readback must precede volume residual removal");
  assert.ok(quiesceReadbackIndex < networkRemovalIndex, "quiescence readback must precede network residual removal");
  assert.match(residualCleanupRun, /docker rm -f "\$resource_id" \|\|\n\s+blocked_env "exact clean-host container removal failed\."/);
  assert.match(residualCleanupRun, /docker volume rm "\$resource_id" \|\|\n\s+blocked_env "exact clean-host volume removal failed\."/);
  assert.match(residualCleanupRun, /docker network rm "\$resource_id" \|\|\n\s+blocked_env "exact clean-host network removal failed\."/);
  assert.ok((residualCleanupRun.match(/docker ps -aq/g) || []).length >= 2, "cleanup must re-query exact containers");
  assert.ok((residualCleanupRun.match(/docker volume ls -q/g) || []).length >= 2, "cleanup must re-query exact volumes");
  assert.ok((residualCleanupRun.match(/docker network ls -q/g) || []).length >= 2, "cleanup must re-query exact networks");
  assert.match(residualCleanupRun, /echo "BLOCKED_ENV: \$1"/);
  assert.match(residualCleanupRun, /blocked_env "exact clean-host Workspace residuals remain after cleanup\."/);
  assert.match(residualCleanupRun, /UPSTREAM_HANDOFF: inspect only resources carrying provider=local-docker, account=acct-admin, and this run exact Workspace ID/);
  const quiesceReadbackFailureIndex = residualCleanupRun.indexOf('blocked_env "exact clean-host mutating service status readback failed before residual cleanup."');
  const runningMutatorFailureIndex = residualCleanupRun.indexOf('blocked_env "exact clean-host mutating services remain running before residual cleanup."');
  assert.ok(quiesceReadbackFailureIndex > quiesceReadbackIndex && quiesceReadbackFailureIndex < containerRemovalIndex,
    "failed quiescence readback must block before label-based removal");
  assert.ok(runningMutatorFailureIndex > quiesceReadbackIndex && runningMutatorFailureIndex < containerRemovalIndex,
    "running mutators must block before label-based removal");

  const teardownRun = stepRun("Tear down isolated compose project");
  const residualCleanupIndex = steps.findIndex((step) => step.name === "Remove exact clean-host workspace residuals");
  const teardownIndex = steps.findIndex((step) => step.name === "Tear down isolated compose project");
  assert.ok(residualCleanupIndex >= 0 && residualCleanupIndex < teardownIndex, "residual cleanup must run before Compose teardown");
  assert.match(diagnosticsRun, /-f "\$OPL_COMPOSE_OVERRIDE_FILE" ps/);
  assert.match(diagnosticsRun, /-f "\$OPL_COMPOSE_OVERRIDE_FILE" logs --no-color --tail=200/);
  assert.match(teardownRun, /-f "\$OPL_COMPOSE_OVERRIDE_FILE" down --volumes --remove-orphans/);
  assert.match(teardownRun, /rm -f "\$OPL_ENV_FILE" "\$OPL_COMPOSE_OVERRIDE_FILE" "\$OPL_WORKSPACE_ID_FILE"/);
  assert.doesNotMatch(JSON.stringify(workflow), /release-opl-cloud-image|fixture|mock/i);
});
