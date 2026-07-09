#!/usr/bin/env bash
set -euo pipefail

root="${1:-.}"
cd "$root"

fail=0
for pattern in \
  "type controlPlaneApp|type controlPlaneState|factsLocked|applyFacts|persistLocked" \
  "rememberCompute|rememberStorage|rememberAttachment|rememberWorkspaceProjection|rememberManualTopUp|rememberResourceSettlement" \
  "app\\.(users|sessions|loginFailures|computes|storages|attachments|workspaces|wallets|ledger|walletTx|topups|auditEvents)"
do
  if rg -n "$pattern" services/control-plane/internal; then
    fail=1
  fi
done

if [[ "$fail" -ne 0 ]]; then
  echo "runtime fact-source cleanup is incomplete" >&2
  exit 1
fi
