#!/usr/bin/env bash

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
    if [ "$mode" = "previous" ]; then
      if ! image="$(cat "$rollback_dir/$deployment")"; then
        failed=1
        continue
      fi
    else
      image="$OPL_CLOUD_IMAGE"
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
  local failed=0
  patch_workspace_image "$OPL_WORKSPACE_IMAGE" || failed=1
  set_cloud_images candidate || failed=1
  wait_cloud_rollouts candidate || failed=1
  verify_workspace_config_image "$OPL_WORKSPACE_IMAGE" || failed=1
  return "$failed"
}

apply_bootstrap_images() {
  apply_candidate_images
}
