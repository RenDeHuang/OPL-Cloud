# Development Worktree Policy

Use a short-lived Git worktree when a medium or large change, concurrent writer,
or risky integration makes isolation and recovery useful. Keep one objective and
one explicit write set in each worktree.

## Integration

- Run focused checks first, then the applicable `verify:local` gate.
- Reconcile the change with current `main` before canonical integration.
- After the result is absorbed and read back, remove the task-owned worktree,
  branch, stash, and temporary artifacts.

## Repository Hygiene

- Worktrees are execution spaces, not archives.
- Commit source and owned generated artifacts only; keep `node_modules` and
  ordinary `dist` output local.
- Active docs and tests describe current behavior. A temporary migration or
  cleanup guard is removed with the path it protects.
