import assert from "node:assert/strict";
import test from "node:test";

import { usdBalance } from "../../apps/console-ui/src/pages/shared/formatters.ts";

test("unavailable Sub2API balance is not displayed as zero", () => {
  assert.equal(usdBalance({ source: "sub2api", currency: "USD", status: "unavailable", available: false }), "-");
  assert.equal(usdBalance({ source: "sub2api", currency: "USD", status: "available", available: true, usdMicros: 0 }), "$0.000000");
});
