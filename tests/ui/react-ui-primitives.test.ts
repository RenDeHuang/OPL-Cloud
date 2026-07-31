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
  const [button, field, select, checkbox, segmented, modal, styles] = await Promise.all([
    source("apps/console-ui/src/components/ui/Button.tsx"),
    source("apps/console-ui/src/components/ui/Field.tsx"),
    source("apps/console-ui/src/components/ui/Select.tsx"),
    source("apps/console-ui/src/components/ui/Checkbox.tsx"),
    source("apps/console-ui/src/components/ui/SegmentedControl.tsx"),
    source("apps/console-ui/src/components/ui/Modal.tsx"),
    source("apps/console-ui/src/styles.css")
  ]);
  assert.match(button, /aria-busy/);
  assert.match(button, /className=\{\["ui-button", className\]\.filter\(Boolean\)\.join\(" "\)\}/);
  assert.match(field, /aria-invalid/);
  assert.match(select, /aria-invalid/);
  assert.match(checkbox, /<AppsCheckbox/);
  assert.doesNotMatch(checkbox, /console-checkbox__native/);
  assert.match(segmented, /role="radiogroup"/);
  assert.match(modal, /role="dialog"/);
  assert.match(modal, /Escape/);
  assert.match(modal, /focus/);
  assert.match(styles, /\.console-modal-backdrop\s*\{[\s\S]+position:\s*fixed;[\s\S]+z-index:/);
  assert.match(styles, /\.console-modal\s*\{[\s\S]+max-height:[\s\S]+overflow:/);
  assert.match(styles, /\.console-modal__body\s*\{[\s\S]+overflow-y:\s*auto/);
});

test("Apps SDK fields are not restyled by the legacy global input border", async () => {
  const styles = await source("apps/console-ui/src/styles.css");
  assert.doesNotMatch(styles, /(?:^|\n)input,\s*\nselect\s*\{[^}]*\bborder:/);
  assert.doesNotMatch(styles, /(?:^|\n)input:focus,\s*\nselect:focus\s*\{/);
  assert.match(styles, /\.native-control\s*\{/);
  assert.match(styles, /@media \(max-width:\s*520px\)[\s\S]+\.console-modal__footer > \*\s*\{[\s\S]+flex:\s*1 1 0;/);
});
