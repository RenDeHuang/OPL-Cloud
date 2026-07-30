import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);
const source = (path: string) => readFile(new URL(path, root), "utf8").catch(() => "");

test("Console UI foundation uses real Apps SDK UI exports", async () => {
  const [index, button, badge, field, styles] = await Promise.all([
    source("apps/console-ui/src/components/ui/index.ts"),
    source("apps/console-ui/src/components/ui/Button.tsx"),
    source("apps/console-ui/src/components/ui/Badge.tsx"),
    source("apps/console-ui/src/components/ui/Field.tsx"),
    source("apps/console-ui/src/components/ui/apps-sdk.css")
  ]);
  const joined = [button, badge, field].join("\n");
  assert.match(joined, /@openai\/apps-sdk-ui\/components\//);
  for (const name of ["Button", "Badge", "Field", "Select", "Checkbox", "SegmentedControl", "Alert", "Tooltip", "Modal"]) {
    assert.match(index, new RegExp(`export \\{ ${name}`));
  }
  assert.match(styles, /@import\s+"@openai\/apps-sdk-ui\/css"/);
  assert.match(styles, /tailwindcss/);
});

test("Console primitives expose accessible busy, invalid and modal states", async () => {
  const [button, field, select, checkbox, segmented, modal] = await Promise.all([
    source("apps/console-ui/src/components/ui/Button.tsx"),
    source("apps/console-ui/src/components/ui/Field.tsx"),
    source("apps/console-ui/src/components/ui/Select.tsx"),
    source("apps/console-ui/src/components/ui/Checkbox.tsx"),
    source("apps/console-ui/src/components/ui/SegmentedControl.tsx"),
    source("apps/console-ui/src/components/ui/Modal.tsx")
  ]);
  assert.match(button, /aria-busy/);
  assert.match(field, /aria-invalid/);
  assert.match(select, /aria-invalid/);
  assert.match(checkbox, /type="checkbox"/);
  assert.match(segmented, /role="radiogroup"/);
  assert.match(modal, /role="dialog"/);
  assert.match(modal, /Escape/);
  assert.match(modal, /focus/);
});
