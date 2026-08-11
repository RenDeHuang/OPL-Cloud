#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

if [[ "$(git branch --show-current)" != "main" ]]; then
  echo "Whitepaper publication must be requested from main." >&2
  exit 1
fi
if [[ -n "$(git status --porcelain)" ]]; then
  echo "Whitepaper publication requires a clean worktree." >&2
  exit 1
fi

git fetch --quiet origin main
if [[ "$(git rev-parse HEAD)" != "$(git rev-parse origin/main)" ]]; then
  echo "Whitepaper publication requires HEAD == origin/main." >&2
  exit 1
fi
if ! command -v gh >/dev/null 2>&1; then
  echo "GitHub CLI is required to request the approved publication workflow." >&2
  exit 1
fi

gh workflow run whitepaper.yml --ref main -f publish=true
echo "Requested whitepaper publication. Environment approval, same-bundle deployment, and exact-byte readback run remotely."
