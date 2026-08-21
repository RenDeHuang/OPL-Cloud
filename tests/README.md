# Test Lifecycle

Long-term tests protect current public behavior and hard boundaries: ownership,
authorization, money, Secrets, persistence, provider resources, runtime
readback, accessibility, and consumed cross-module interfaces.

Migration and cleanup tests are temporary. Remove them with the retired route,
fixture, compatibility path, or data transition they protect.

Prefer exercising a real consumer or public behavior. Prose wording, source
layout, file absence, UI copy, and a repetition of contract JSON are not useful
regression surfaces by themselves.

Run focused checks first, then `npm run verify:local` or
`npm run verify:local:full` according to the changed boundary.
