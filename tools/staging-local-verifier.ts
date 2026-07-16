process.stderr.write(`${JSON.stringify({
  ok: false,
  error: "staging_local_verifier_retired",
  replacement: "Use npm run verify:production for read-only fixed-slot verification; run Provider Acceptance separately."
}, null, 2)}\n`);
process.exitCode = 1;
