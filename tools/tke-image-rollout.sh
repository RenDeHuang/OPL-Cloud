#!/usr/bin/env bash

list_workspace_images() {
  local payload
  if ! payload="$(kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" \
    get deployment -l 'oplcloud.cn/workspace-id' -o json)"; then
    return 1
  fi
  printf '%s' "$payload" | node -e '
    const fs = require("node:fs");
    const payload = JSON.parse(fs.readFileSync(0, "utf8"));
    const seen = new Set();
    const lines = (payload.items || []).map((item) => {
      const deployment = String(item?.metadata?.name || "");
      const workspaceId = String(item?.metadata?.labels?.["oplcloud.cn/workspace-id"] || "");
      const containers = (item?.spec?.template?.spec?.containers || []).filter((container) => container.name === "workspace");
      const image = String(containers[0]?.image || "");
      if (!/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(deployment) || seen.has(deployment) || !workspaceId || containers.length !== 1 || !image || /[\t\r\n]/.test(image)) {
        throw new Error("workspace_deployment_image_snapshot_ambiguous");
      }
      seen.add(deployment);
      return [deployment, "workspace", image].join("\t");
    }).sort();
    process.stdout.write(lines.length ? `${lines.join("\n")}\n` : "");
  '
}

patch_workspace_image() {
  local image="$1"
  local patch
  patch="$(WORKSPACE_IMAGE="$image" node -e '
    process.stdout.write(JSON.stringify({ data: { OPL_WORKSPACE_IMAGE: process.env.WORKSPACE_IMAGE } }));
  ')"
  kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" patch configmap opl-cloud-config \
    --type merge -p "$patch"
}

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

workspace_expected_image() {
  local mode="$1"
  local deployment="$2"
  local previous_default="$3"
  local snapshot_deployment snapshot_container snapshot_image extra
  if [ "$mode" = "candidate" ]; then
    printf '%s' "$OPL_WORKSPACE_IMAGE"
    return 0
  fi
  [ "$mode" = "previous" ] && [ -f "$workspace_images" ] || return 1
  # ponytail: snapshot lookup is O(n^2); index the TSV only if rollout volume makes this measurable.
  while IFS=$'\t' read -r snapshot_deployment snapshot_container snapshot_image extra; do
    [ -n "$snapshot_deployment" ] || continue
    if [ "$snapshot_deployment" = "$deployment" ]; then
      [ "$snapshot_container" = "workspace" ] && [ -n "$snapshot_image" ] && [ -z "$extra" ] || return 1
      printf '%s' "$snapshot_image"
      return 0
    fi
  done < "$workspace_images"
  [ -n "$previous_default" ] || return 1
  printf '%s' "$previous_default"
}

set_workspace_images() {
  local mode="$1"
  local rows="$2"
  local previous_default="$3"
  local failed=0
  local deployment container current_image extra expected_image
  while IFS=$'\t' read -r deployment container current_image extra; do
    [ -n "$deployment" ] || continue
    if [ "$container" != "workspace" ] || [ -z "$current_image" ] || [ -n "$extra" ]; then
      failed=1
      continue
    fi
    if ! expected_image="$(workspace_expected_image "$mode" "$deployment" "$previous_default")"; then
      failed=1
      continue
    fi
    kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" set image \
      "deployment/$deployment" "$container=$expected_image" || failed=1
  done <<< "$rows"
  return "$failed"
}

workspace_rows_contain() {
  local rows="$1"
  local expected_deployment="$2"
  local deployment rest
  while IFS=$'\t' read -r deployment rest; do
    [ "$deployment" = "$expected_deployment" ] && return 0
  done <<< "$rows"
  return 1
}

wait_workspace_rollouts() {
  local mode="$1"
  local rows="$2"
  local previous_default="$3"
  local failed=0
  local deployment container listed_image extra expected_image current_image
  local snapshot_deployment snapshot_container snapshot_image snapshot_extra
  [ -f "$workspace_images" ] || failed=1
  while IFS=$'\t' read -r deployment container listed_image extra; do
    [ -n "$deployment" ] || continue
    if [ "$container" != "workspace" ] || [ -z "$listed_image" ] || [ -n "$extra" ]; then
      failed=1
      continue
    fi
    expected_image=""
    if ! expected_image="$(workspace_expected_image "$mode" "$deployment" "$previous_default")"; then
      failed=1
    fi
    kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" rollout status \
      "deployment/$deployment" --timeout=300s || failed=1
    if ! current_image="$(kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" get "deployment/$deployment" \
      -o "jsonpath={.spec.template.spec.containers[?(@.name=='$container')].image}")"; then
      failed=1
    elif [ -z "$expected_image" ] || [ "$current_image" != "$expected_image" ]; then
      failed=1
    fi
  done <<< "$rows"
  if [ "$mode" = "previous" ] && [ -f "$workspace_images" ]; then
    while IFS=$'\t' read -r snapshot_deployment snapshot_container snapshot_image snapshot_extra; do
      [ -n "$snapshot_deployment" ] || continue
      if [ "$snapshot_container" != "workspace" ] || [ -z "$snapshot_image" ] || [ -n "$snapshot_extra" ] || ! workspace_rows_contain "$rows" "$snapshot_deployment"; then
        failed=1
      fi
    done < "$workspace_images"
  fi
  return "$failed"
}

wait_cloud_rollouts() {
  local mode="$1"
  local failed=0
  local deployment container expected_image current_image
  for deployment in opl-cloud-control-plane opl-cloud-ledger opl-cloud-fabric; do
    container="${deployment#opl-cloud-}"
    if [ "$mode" = "previous" ]; then
      if ! expected_image="$(cat "$rollback_dir/$deployment")"; then
        expected_image=""
        failed=1
      fi
    else
      expected_image="$OPL_CLOUD_IMAGE"
    fi
    kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" rollout restart "deployment/$deployment" || failed=1
    kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" rollout status "deployment/$deployment" --timeout=300s || failed=1
    if ! current_image="$(kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" get "deployment/$deployment" \
      -o "jsonpath={.spec.template.spec.containers[?(@.name=='$container')].image}")"; then
      failed=1
    elif [ -z "$expected_image" ] || [ "$current_image" != "$expected_image" ]; then
      failed=1
    fi
  done
  return "$failed"
}

restore_previous_images() {
  local failed=0
  local item deployment container previous_image previous_workspace_image
  local current_workspace_rows=""
  [ -f "$workspace_images" ] || failed=1
  for item in \
    "opl-cloud-control-plane:control-plane" \
    "opl-cloud-ledger:ledger" \
    "opl-cloud-fabric:fabric"; do
    deployment="${item%%:*}"
    container="${item##*:}"
    if ! previous_image="$(cat "$rollback_dir/$deployment")"; then
      failed=1
      continue
    fi
    kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" set image "deployment/$deployment" "$container=$previous_image" || failed=1
  done
  if ! previous_workspace_image="$(cat "$rollback_dir/OPL_WORKSPACE_IMAGE")"; then
    previous_workspace_image=""
    failed=1
  elif ! patch_workspace_image "$previous_workspace_image"; then
    failed=1
  fi
  if ! current_workspace_rows="$(list_workspace_images)"; then
    current_workspace_rows=""
    failed=1
  else
    set_workspace_images previous "$current_workspace_rows" "$previous_workspace_image" || failed=1
  fi
  wait_cloud_rollouts previous || failed=1
  if ! current_workspace_rows="$(list_workspace_images)"; then
    current_workspace_rows=""
    failed=1
  else
    set_workspace_images previous "$current_workspace_rows" "$previous_workspace_image" || failed=1
    wait_workspace_rollouts previous "$current_workspace_rows" "$previous_workspace_image" || failed=1
  fi
  if [ -n "$previous_workspace_image" ]; then
    verify_workspace_config_image "$previous_workspace_image" || failed=1
  fi
  return "$failed"
}

apply_candidate_images() {
  local failed=0
  local item deployment container current_workspace_rows
  [ -f "$workspace_images" ] || failed=1
  patch_workspace_image "$OPL_WORKSPACE_IMAGE" || failed=1
  for item in \
    "opl-cloud-control-plane:control-plane" \
    "opl-cloud-ledger:ledger" \
    "opl-cloud-fabric:fabric"; do
    deployment="${item%%:*}"
    container="${item##*:}"
    kubectl --kubeconfig "$KUBECONFIG" -n "$OPL_K8S_NAMESPACE" set image "deployment/$deployment" "$container=$OPL_CLOUD_IMAGE" || failed=1
  done
  if ! current_workspace_rows="$(list_workspace_images)"; then
    current_workspace_rows=""
    failed=1
  else
    set_workspace_images candidate "$current_workspace_rows" "" || failed=1
  fi
  wait_cloud_rollouts candidate || failed=1
  if ! current_workspace_rows="$(list_workspace_images)"; then
    current_workspace_rows=""
    failed=1
  else
    set_workspace_images candidate "$current_workspace_rows" "" || failed=1
    wait_workspace_rollouts candidate "$current_workspace_rows" "" || failed=1
  fi
  verify_workspace_config_image "$OPL_WORKSPACE_IMAGE" || failed=1
  return "$failed"
}
