#!/usr/bin/env bash

readonly OPL_MIN_ROLLOUT_AVAILABLE_BYTES=26843545600

read_workspace_config_image() {
  kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" get configmap opl-cloud-config \
    -o jsonpath='{.data.OPL_WORKSPACE_IMAGE}'
}

verify_workspace_config_image() {
  local expected_image="$1"
  local current_image
  if ! current_image="$(read_workspace_config_image)"; then
    return 1
  fi
  [ "$current_image" = "$expected_image" ]
}

preflight_rollout_storage() {
  local evidence_file="$1"
  local nodes_file="${evidence_file}.nodes"
  local names_file="${evidence_file}.names"
  local node_name stats_file
  local -a stats_args=()

  kubectl --kubeconfig "$KUBECONFIG" get nodes -o json > "$nodes_file"
  node - "$nodes_file" > "$names_file" <<'NODE'
const fs = require("node:fs");
const payload = JSON.parse(fs.readFileSync(process.argv[2], "utf8"));
const nodes = Array.isArray(payload.items) ? payload.items : [];
if (nodes.length !== 1) throw new Error(`rollout_single_node_required:${nodes.length}`);
for (const node of nodes) {
  const name = String(node.metadata?.name || "");
  if (!/^[a-zA-Z0-9.-]+$/.test(name)) throw new Error("rollout_node_name_invalid");
  const condition = (type) => (node.status?.conditions || []).find((item) => item.type === type)?.status;
  if (condition("Ready") !== "True") throw new Error(`rollout_node_not_ready:${name}`);
  if (condition("DiskPressure") !== "False") throw new Error(`rollout_node_DiskPressure:${name}`);
  if (node.spec?.unschedulable === true) throw new Error(`rollout_node_Unschedulable:${name}`);
  process.stdout.write(`${name}\n`);
}
NODE

  while IFS= read -r node_name; do
    [ -n "$node_name" ] || continue
    stats_file="${evidence_file}.${node_name}.stats-summary.json"
    kubectl --kubeconfig "$KUBECONFIG" get --raw "/api/v1/nodes/$node_name/proxy/stats/summary" > "$stats_file"
    stats_args+=("$node_name" "$stats_file")
  done < "$names_file"

  node - "$nodes_file" "$evidence_file" "$OPL_MIN_ROLLOUT_AVAILABLE_BYTES" "${stats_args[@]}" <<'NODE'
const fs = require("node:fs");
const [nodesPath, evidencePath, minimumText, ...statsArgs] = process.argv.slice(2);
const minimumAvailableBytes = Number(minimumText);
const nodesPayload = JSON.parse(fs.readFileSync(nodesPath, "utf8"));
const statsByNode = new Map();
for (let index = 0; index < statsArgs.length; index += 2) {
  statsByNode.set(statsArgs[index], JSON.parse(fs.readFileSync(statsArgs[index + 1], "utf8")));
}
const result = {
  checkedAt: new Date().toISOString(),
  minimumAvailableBytes,
  nodes: (nodesPayload.items || []).map((node) => {
    const name = node.metadata.name;
    const stats = statsByNode.get(name)?.node;
    const nodefs = stats?.fs;
    const imagefs = stats?.runtime?.imageFs;
    const diskPressure = (node.status?.conditions || []).find((item) => item.type === "DiskPressure")?.status;
    for (const [label, value] of [["nodefs", nodefs], ["imagefs", imagefs]]) {
      if (!Number.isFinite(value?.capacityBytes) || !Number.isFinite(value?.availableBytes)) {
        throw new Error(`rollout_${label}_stats_unavailable:${name}`);
      }
    }
    return {
      name,
      diskPressure,
      nodefs: { capacityBytes: nodefs.capacityBytes, availableBytes: nodefs.availableBytes },
      imagefs: { capacityBytes: imagefs.capacityBytes, availableBytes: imagefs.availableBytes }
    };
  })
};
fs.writeFileSync(evidencePath, `${JSON.stringify(result, null, 2)}\n`);
for (const node of result.nodes) {
  if (node.diskPressure !== "False") throw new Error(`rollout_node_DiskPressure:${node.name}`);
  if (node.nodefs.availableBytes < minimumAvailableBytes) throw new Error(`rollout_nodefs_below_25GiB:${node.name}`);
  if (node.imagefs.availableBytes < minimumAvailableBytes) throw new Error(`rollout_imagefs_below_25GiB:${node.name}`);
}
process.stdout.write(`${JSON.stringify(result)}\n`);
NODE
}

expected_cloud_image() {
  local mode="$1"
  local deployment="$2"
  if [ "$mode" = "previous" ]; then
    cat "$rollback_dir/$deployment"
  else
    printf '%s' "$OPL_CLOUD_IMAGE"
  fi
}

verify_single_candidate_revisions() {
  local deployment previous_file previous_revision current_revision
  for deployment in opl-cloud-control-plane opl-cloud-ledger opl-cloud-fabric; do
    previous_file="$rollback_dir/$deployment.deployment.json"
    test -s "$previous_file"
    previous_revision="$(node -e '
      const fs = require("node:fs");
      const value = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
      process.stdout.write(String(value.metadata?.annotations?.["deployment.kubernetes.io/revision"] || ""));
    ' "$previous_file")"
    current_revision="$(kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" get "deployment/$deployment" \
      -o jsonpath='{.metadata.annotations.deployment\.kubernetes\.io/revision}')"
    PREVIOUS_REVISION="$previous_revision" CURRENT_REVISION="$current_revision" node -e '
      const previous = Number(process.env.PREVIOUS_REVISION);
      const current = Number(process.env.CURRENT_REVISION);
      if (!Number.isInteger(previous) || !Number.isInteger(current) || current < previous || current > previous + 1) {
        throw new Error(`candidate_revision_count_invalid:${previous}:${current}`);
      }
    '
  done
}

wait_cloud_rollouts() {
  local mode="$1"
  local timeout_seconds="${OPL_ROLLOUT_TIMEOUT_SECONDS:-300}"
  local poll_seconds="${OPL_ROLLOUT_POLL_SECONDS:-5}"
  local state_dir="${OPL_ROLLOUT_STATE_DIR:-${RUNNER_TEMP:-/tmp}/opl-cloud-rollout-state}"
  local deadline now status
  local control_plane_image ledger_image fabric_image

  if ! [[ "$timeout_seconds" =~ ^[1-9][0-9]*$ ]] || [ "$timeout_seconds" -gt 300 ]; then
    echo "OPL_ROLLOUT_TIMEOUT_SECONDS must be between 1 and 300." >&2
    return 1
  fi
  control_plane_image="$(expected_cloud_image "$mode" opl-cloud-control-plane)" || return 1
  ledger_image="$(expected_cloud_image "$mode" opl-cloud-ledger)" || return 1
  fabric_image="$(expected_cloud_image "$mode" opl-cloud-fabric)" || return 1
  mkdir -p "$state_dir"
  deadline=$(( $(date +%s) + timeout_seconds ))

  while true; do
    kubectl --kubeconfig "$KUBECONFIG" get nodes -o json > "$state_dir/nodes.json" || return 1
    if ! node - "$state_dir/nodes.json" <<'NODE'
const fs = require("node:fs");
const nodes = JSON.parse(fs.readFileSync(process.argv[2], "utf8")).items || [];
for (const node of nodes) {
  const pressure = (node.status?.conditions || []).find((item) => item.type === "DiskPressure");
  if (pressure?.status === "True") throw new Error(`rollout_DiskPressure:${node.metadata?.name || "unknown"}`);
}
NODE
    then
      return 1
    fi
    kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" get deployment \
      opl-cloud-control-plane opl-cloud-ledger opl-cloud-fabric -o json > "$state_dir/deployments.json" || return 1
    kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" get pods \
      -l app.kubernetes.io/name=opl-cloud -o json > "$state_dir/pods.json" || return 1

    if CONTROL_PLANE_IMAGE="$control_plane_image" LEDGER_IMAGE="$ledger_image" FABRIC_IMAGE="$fabric_image" \
      node - "$state_dir/nodes.json" "$state_dir/deployments.json" "$state_dir/pods.json" <<'NODE'
const fs = require("node:fs");
const nodes = JSON.parse(fs.readFileSync(process.argv[2], "utf8")).items || [];
const deployments = JSON.parse(fs.readFileSync(process.argv[3], "utf8")).items || [];
const pods = JSON.parse(fs.readFileSync(process.argv[4], "utf8")).items || [];
for (const node of nodes) {
  const pressure = (node.status?.conditions || []).find((item) => item.type === "DiskPressure");
  if (pressure?.status === "True") throw new Error(`rollout_DiskPressure:${node.metadata?.name || "unknown"}`);
}
const expected = new Map([
  ["opl-cloud-control-plane", ["control-plane", process.env.CONTROL_PLANE_IMAGE]],
  ["opl-cloud-ledger", ["ledger", process.env.LEDGER_IMAGE]],
  ["opl-cloud-fabric", ["fabric", process.env.FABRIC_IMAGE]]
]);
const cloudComponents = new Set(["control-plane", "ledger", "fabric"]);
for (const pod of pods) {
  const component = pod.metadata?.labels?.["app.kubernetes.io/component"];
  if (!cloudComponents.has(component)) continue;
  if (pod.status?.reason === "Evicted") throw new Error(`rollout_Evicted:${pod.metadata?.name || "unknown"}`);
  const unschedulable = (pod.status?.conditions || []).find((item) =>
    item.type === "PodScheduled" && item.status === "False" && item.reason === "Unschedulable"
  );
  if (unschedulable) throw new Error(`rollout_Unschedulable:${pod.metadata?.name || "unknown"}`);
  for (const status of [...(pod.status?.initContainerStatuses || []), ...(pod.status?.containerStatuses || [])]) {
    const reason = status.state?.waiting?.reason;
    if (reason === "ImagePullBackOff" || reason === "CrashLoopBackOff") {
      throw new Error(`rollout_${reason}:${pod.metadata?.name || "unknown"}:${status.name}`);
    }
  }
}
let complete = true;
for (const [name, [containerName, expectedImage]] of expected) {
  const deployment = deployments.find((item) => item.metadata?.name === name);
  if (!deployment) throw new Error(`rollout_deployment_missing:${name}`);
  const image = (deployment.spec?.template?.spec?.containers || []).find((item) => item.name === containerName)?.image;
  if (!expectedImage || image !== expectedImage) throw new Error(`rollout_image_mismatch:${name}`);
  const desired = Number(deployment.spec?.replicas ?? 1);
  const status = deployment.status || {};
  if (Number(status.observedGeneration || 0) < Number(deployment.metadata?.generation || 0) ||
      Number(status.updatedReplicas || 0) !== desired || Number(status.readyReplicas || 0) !== desired ||
      Number(status.availableReplicas || 0) !== desired || Number(status.unavailableReplicas || 0) !== 0) {
    complete = false;
  }
}
process.exit(complete ? 0 : 10);
NODE
    then
      return 0
    else
      status=$?
      if [ "$status" -ne 10 ]; then
        return "$status"
      fi
    fi

    now="$(date +%s)"
    if [ "$now" -ge "$deadline" ]; then
      echo "Cloud rollout did not converge within ${timeout_seconds}s shared deadline." >&2
      return 1
    fi
    sleep "$poll_seconds"
  done
}

set_cloud_images() {
  local mode="$1"
  local failed=0
  local item deployment container image
  for item in \
    "opl-cloud-control-plane:control-plane" \
    "opl-cloud-ledger:ledger" \
    "opl-cloud-fabric:fabric"; do
    deployment="${item%%:*}"
    container="${item##*:}"
    if ! image="$(expected_cloud_image "$mode" "$deployment")"; then
      failed=1
      continue
    fi
    kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" set image \
      "deployment/$deployment" "$container=$image" || failed=1
  done
  return "$failed"
}

restore_previous_config() {
  local snapshot="$rollback_dir/opl-cloud-config.json"
  local patch
  if ! patch="$(node -e '
    const fs = require("node:fs");
    const value = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
    if (!value.data || typeof value.data !== "object" || Array.isArray(value.data)) process.exit(1);
    process.stdout.write(JSON.stringify([{ op: "replace", path: "/data", value: value.data }]));
  ' "$snapshot")"; then
    return 1
  fi
  kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" patch configmap opl-cloud-config \
    --type json -p "$patch"
}

restore_previous_images() {
  local failed=0
  local previous_workspace_image
  restore_previous_config || failed=1
  set_cloud_images previous || failed=1
  wait_cloud_rollouts previous || failed=1
  if ! previous_workspace_image="$(node -e '
    const fs = require("node:fs");
    process.stdout.write(JSON.parse(fs.readFileSync(process.argv[1], "utf8")).data.OPL_WORKSPACE_IMAGE || "");
  ' "$rollback_dir/opl-cloud-config.json")"; then
    failed=1
  elif [ -z "$previous_workspace_image" ] || ! verify_workspace_config_image "$previous_workspace_image"; then
    failed=1
  fi
  return "$failed"
}

restore_previous_bootstrap_images() {
  restore_previous_images
}

apply_candidate_images() {
  verify_single_candidate_revisions || return 1
  wait_cloud_rollouts candidate || return 1
  verify_workspace_config_image "$OPL_WORKSPACE_IMAGE"
}

apply_bootstrap_images() {
  apply_candidate_images
}

capture_rollout_diagnostics() {
  local diagnostics_dir="$1"
  local nodes_file="$diagnostics_dir/nodes.json"
  local node_name pod_name container_name resource_uid

  mkdir -p "$diagnostics_dir/uid-events" "$diagnostics_dir/logs" "$diagnostics_dir/node-stats"
  : > "$diagnostics_dir/capture-errors.log"
  capture_json() {
    local output="$1"
    shift
    if ! "$@" > "$output" 2>> "$diagnostics_dir/capture-errors.log"; then
      printf 'capture_failed:%s\n' "$output" >> "$diagnostics_dir/capture-errors.log"
    fi
  }

  capture_json "$diagnostics_dir/deployments.json" kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" get deployment \
    opl-cloud-control-plane opl-cloud-ledger opl-cloud-fabric -o json
  capture_json "$diagnostics_dir/replicasets.json" kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" get replicasets \
    -l app.kubernetes.io/name=opl-cloud -o json
  capture_json "$diagnostics_dir/pods.json" kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" get pods \
    -l app.kubernetes.io/name=opl-cloud -o json
  capture_json "$nodes_file" kubectl --kubeconfig "$KUBECONFIG" get nodes -o json
  capture_json "$diagnostics_dir/events.json" kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" get events \
    --sort-by=.metadata.creationTimestamp -o json

  if [ -s "$nodes_file" ]; then
    while IFS= read -r node_name; do
      [ -n "$node_name" ] || continue
      capture_json "$diagnostics_dir/node-stats/$node_name.stats-summary.json" kubectl --kubeconfig "$KUBECONFIG" \
        get --raw "/api/v1/nodes/$node_name/proxy/stats/summary"
    done < <(node -e '
      const fs = require("node:fs");
      const value = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
      for (const item of value.items || []) {
        const name = String(item.metadata?.name || "");
        if (/^[a-zA-Z0-9.-]+$/.test(name)) console.log(name);
      }
    ' "$nodes_file" 2>> "$diagnostics_dir/capture-errors.log")
  fi

  node - "$diagnostics_dir" <<'NODE' || true
const fs = require("node:fs");
const path = require("node:path");
const root = process.argv[2];
const readItems = (name) => {
  try { return JSON.parse(fs.readFileSync(path.join(root, name), "utf8")).items || []; } catch { return []; }
};
const deployments = readItems("deployments.json");
const replicasets = readItems("replicasets.json");
const pods = readItems("pods.json");
const summary = [...deployments, ...replicasets, ...pods].map((item) => ({
  kind: item.kind,
  name: item.metadata?.name,
  uid: item.metadata?.uid,
  revision: item.metadata?.annotations?.["deployment.kubernetes.io/revision"],
  ownerReferences: item.metadata?.ownerReferences || [],
  deletionTimestamp: item.metadata?.deletionTimestamp || null,
  images: (item.spec?.template?.spec?.containers || item.spec?.containers || []).map((container) => ({ name: container.name, image: container.image })),
  imageIDs: (item.status?.containerStatuses || []).map((container) => ({ name: container.name, imageID: container.imageID || container.imageId || "" })),
  conditions: item.status?.conditions || []
}));
fs.writeFileSync(path.join(root, "resource-summary.json"), `${JSON.stringify(summary, null, 2)}\n`);
const events = readItems("events.json").sort((left, right) => {
  const stamp = (item) => item.eventTime || item.lastTimestamp || item.firstTimestamp || item.metadata?.creationTimestamp || "";
  return stamp(left).localeCompare(stamp(right));
});
fs.writeFileSync(path.join(root, "events-timeline.json"), `${JSON.stringify({ items: events }, null, 2)}\n`);
for (const item of [...deployments, ...replicasets, ...pods]) {
  const uid = String(item.metadata?.uid || "");
  if (/^[a-zA-Z0-9-]+$/.test(uid)) process.stdout.write(`${uid}\n`);
}
NODE
  while IFS= read -r resource_uid; do
    [ -n "$resource_uid" ] || continue
    capture_json "$diagnostics_dir/uid-events/$resource_uid.json" kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" \
      get events --field-selector "involvedObject.uid=$resource_uid" --sort-by=.metadata.creationTimestamp -o json
  done < <(node -e '
    const fs = require("node:fs");
    const path = require("node:path");
    for (const name of ["deployments.json", "replicasets.json", "pods.json"]) {
      try {
        for (const item of JSON.parse(fs.readFileSync(path.join(process.argv[1], name), "utf8")).items || []) {
          const uid = String(item.metadata?.uid || "");
          if (/^[a-zA-Z0-9-]+$/.test(uid)) console.log(uid);
        }
      } catch {}
    }
  ' "$diagnostics_dir" 2>> "$diagnostics_dir/capture-errors.log")

  if [ -s "$diagnostics_dir/pods.json" ]; then
    while IFS=$'\t' read -r pod_name container_name; do
      [ -n "$pod_name" ] && [ -n "$container_name" ] || continue
      capture_json "$diagnostics_dir/logs/$pod_name.$container_name.current.log" kubectl --kubeconfig "$KUBECONFIG" \
        -n "$OPL_K8S_NAMESPACE" logs "pod/$pod_name" -c "$container_name" --timestamps
      capture_json "$diagnostics_dir/logs/$pod_name.$container_name.previous.log" kubectl --kubeconfig "$KUBECONFIG" \
        -n "$OPL_K8S_NAMESPACE" logs "pod/$pod_name" -c "$container_name" --previous --timestamps
    done < <(node -e '
      const fs = require("node:fs");
      const value = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
      for (const pod of value.items || []) {
        const podName = String(pod.metadata?.name || "");
        if (!/^[a-zA-Z0-9.-]+$/.test(podName)) continue;
        for (const container of [...(pod.spec?.initContainers || []), ...(pod.spec?.containers || [])]) {
          const name = String(container.name || "");
          if (/^[a-zA-Z0-9.-]+$/.test(name)) console.log(`${podName}\t${name}`);
        }
      }
    ' "$diagnostics_dir/pods.json" 2>> "$diagnostics_dir/capture-errors.log")
  fi
  return 0
}
