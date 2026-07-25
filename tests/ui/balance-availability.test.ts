import assert from "node:assert/strict";
import test from "node:test";

import { formatAvailableBalance } from "../../apps/console-ui/src/console-model.ts";

test("unavailable balance is not displayed as zero", () => {
  assert.equal(formatAvailableBalance({ status: "unavailable", available: false }), "暂不可用");
  assert.equal(formatAvailableBalance({ status: "available", available: true, usdMicros: "0" }), "$0.00");
});
