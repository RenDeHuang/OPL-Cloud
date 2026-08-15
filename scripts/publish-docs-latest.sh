#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

if [[ "$(git branch --show-current)" != "main" ]]; then
  echo "The family whitepaper publication must be requested from Cloud main." >&2
  exit 1
fi
if [[ -n "$(git status --porcelain)" ]]; then
  echo "The family whitepaper publication requires a clean Cloud worktree." >&2
  exit 1
fi

git fetch --quiet origin main
if [[ "$(git rev-parse HEAD)" != "$(git rev-parse origin/main)" ]]; then
  echo "The family whitepaper publication requires Cloud HEAD == origin/main." >&2
  exit 1
fi
if ! command -v gh >/dev/null 2>&1; then
  echo "GitHub CLI is required to request the approved publication workflow." >&2
  exit 1
fi

gh workflow run whitepaper.yml \
  --repo gaofeng21cn/one-person-lab \
  --ref main \
  -f publish=true
echo "Requested the unified OPL family whitepaper publication. The Framework workflow builds all five documents, publishes one branded bundle, and reads back the public bytes."
