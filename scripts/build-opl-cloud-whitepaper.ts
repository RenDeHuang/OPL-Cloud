#!/usr/bin/env node

import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const explicitFrameworkRepo = process.env.OPL_FRAMEWORK_REPO?.trim();
const candidates = explicitFrameworkRepo
  ? [path.resolve(explicitFrameworkRepo)]
  : [
      path.resolve(repoRoot, '..', 'one-person-lab'),
      path.resolve(repoRoot, '..', '..', 'one-person-lab'),
      path.resolve(repoRoot, '..', '..', '..', 'one-person-lab'),
    ];

const frameworkRepo = candidates.find((candidate) =>
  fs.existsSync(path.join(candidate, 'scripts', 'run-domain-whitepaper.ts')),
);

if (!frameworkRepo) {
  const searched = candidates.map((candidate) => `  - ${candidate}`).join('\n');
  throw new Error(
    `Cannot locate the OPL Framework whitepaper toolchain. Set OPL_FRAMEWORK_REPO.\nSearched:\n${searched}`,
  );
}

const result = spawnSync(
  process.execPath,
  [
    '--experimental-strip-types',
    path.join(frameworkRepo, 'scripts', 'run-domain-whitepaper.ts'),
    '--repo-root',
    repoRoot,
    '--profile',
    'contracts/whitepaper_profile.json',
  ],
  { cwd: repoRoot, env: process.env, stdio: 'inherit' },
);

if (result.error) throw result.error;
process.exitCode = result.status ?? 1;
