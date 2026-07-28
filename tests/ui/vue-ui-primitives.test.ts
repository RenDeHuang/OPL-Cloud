import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../../", import.meta.url);
const source = (path: string) => readFile(new URL(path, root), "utf8").catch(() => "");

function uiTags(value: string) {
  return [...value.matchAll(/<(?<name>Ui[A-Z][A-Za-z]+)\b/g)]
    .map((match) => match.groups?.name || "")
    .filter(Boolean);
}

test("Console exports exactly the UI components reachable from its pages", async () => {
  const [index, app, keysPanel] = await Promise.all([
    source("apps/console-ui/src/components/ui/index.ts"),
    source("apps/console-ui/src/App.vue"),
    source("apps/console-ui/src/components/keys/KeysPanel.vue")
  ]);
  const exports = [...index.matchAll(/export \{ default as (?<name>Ui[A-Z][A-Za-z]+) \}/g)]
    .map((match) => match.groups?.name || "")
    .filter(Boolean)
    .sort();
  const reachable = [...new Set([...uiTags(app), ...uiTags(keysPanel)])].sort();

  assert.equal(exports.length, 17);
  assert.deepEqual(exports, reachable);
  for (const component of exports) {
    assert.notEqual(await source(`apps/console-ui/src/components/ui/${component}.vue`), "", `${component}.vue must exist`);
  }
});

test("Console mounts App directly and loads only the retained UI styles", async () => {
  const [entry, styles] = await Promise.all([
    source("apps/console-ui/src/main.ts"),
    source("apps/console-ui/src/components/ui/components.css")
  ]);

  assert.match(entry, /import \{ createApp \} from "vue"/);
  assert.match(entry, /components\/ui\/tokens\.css/);
  assert.match(entry, /components\/ui\/components\.css/);
  assert.match(entry, /createApp\(App\)\.mount\("#root"\)/);
  assert.doesNotMatch(entry, /UiProvider|components-advanced/);
  for (const selector of [".ui-avatar", ".ui-code-block", ".ui-radio-group", ".ui-segmented"]) {
    assert.match(styles, new RegExp(selector.replace(".", "\\.")));
  }
  assert.doesNotMatch(styles, /\.ui-slider|\.ui-shimmer|\.ui-date-range|\.ui-tag-input|\.ui-provider|\.ui-usage-bar/);
});

test("retained controls expose the disabled and accessible states used by Console", async () => {
  const [button, input, select, checkbox, segmented, radioGroup] = await Promise.all([
    source("apps/console-ui/src/components/ui/UiButton.vue"),
    source("apps/console-ui/src/components/ui/UiInput.vue"),
    source("apps/console-ui/src/components/ui/UiSelect.vue"),
    source("apps/console-ui/src/components/ui/UiCheckbox.vue"),
    source("apps/console-ui/src/components/ui/UiSegmentedControl.vue"),
    source("apps/console-ui/src/components/ui/UiRadioGroup.vue")
  ]);

  assert.match(button, /aria-busy/);
  for (const field of [input, select]) {
    assert.match(field, /defineModel/);
    assert.match(field, /aria-invalid/);
    assert.match(field, /aria-describedby/);
  }
  assert.match(checkbox, /defineModel<boolean>/);
  assert.match(segmented, /role="radiogroup"/);
  assert.match(radioGroup, /role="radiogroup"/);
  assert.match(radioGroup, /:disabled="(?:props\.)?disabled \|\| option\.disabled"/);
  assert.match(radioGroup, /option\.value === model\.value && !option\.disabled/);
  assert.match(radioGroup, /find\(\(option\) => !option\.disabled\)/);
  assert.match(radioGroup, /tabStopValue === option\.value \? 0 : -1/);
});

test("Workspace launch disables unavailable catalog options through UiRadioGroup", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const options = app.slice(app.indexOf("const workspacePlanOptions"), app.indexOf("const selectedPlanPrice"));

  assert.match(options, /plans\.value\.map/);
  assert.match(options, /disabled: !plan\.available/);
  assert.match(options, /plans\.value\.find\([\s\S]+plan\.available/);
  assert.match(app, /<UiRadioGroup[^>]+:options="workspacePlanOptions"/);
  assert.doesNotMatch(app, /type="radio"/);
});

test("customer pages render authoritative states without invented values", async () => {
  const app = await source("apps/console-ui/src/App.vue");

  assert.match(app, /workspaceSource\.value\?\.available/);
  assert.match(app, /walletSource\.value\?\.available/);
  assert.match(app, /announcementsSource\.value\?\.status === "unavailable"/);
  assert.match(app, /announcementsSource\.value\?\.status === "empty"/);
  assert.match(app, /runtime\.value\?\.checks/);
  assert.match(app, /Workspace 不存在/);
  assert.match(app, /暂无请求记录/);
  assert.doesNotMatch(app, /输入 Token|输出 Token|缓存写入 Token|缓存读取 Token|请求详情|查看详情|Token 构成/);
  assert.doesNotMatch(app, /mockTrend|fakeTrend|sparkline|Math\.random/);
});

test("API Key management keeps its real interactions and secret handling", async () => {
  const keysPanel = await source("apps/console-ui/src/components/keys/KeysPanel.vue");

  assert.match(keysPanel, /<UiPopover[^>]*label="列设置"/);
  assert.match(keysPanel, /<UiMenu[^>]*:items="keyMenuItems\(key\)"/);
  assert.match(keysPanel, /<UiCodeBlock[^>]*:code="useConfiguration"/);
  assert.match(keysPanel, /<UiCopyButton :value="revealed\.value" @copied="notice = 'Key 已复制'"/);
  assert.match(keysPanel, /secretTimer = window\.setTimeout\(clearSecret, 60_000\)/);
  assert.match(keysPanel, /:inert="Boolean\(dialog\)"/);
  assert.match(keysPanel, /ref="dialogRoot"[^>]*@keydown="onDialogKeydown"/);
});

test("mobile Console keeps navigation isolated and data actions reachable", async () => {
  const [app, styles, keysPanel] = await Promise.all([
    source("apps/console-ui/src/App.vue"),
    source("apps/console-ui/src/styles.css"),
    source("apps/console-ui/src/components/keys/KeysPanel.vue")
  ]);

  assert.match(app, /aria-controls="console-navigation"/);
  assert.match(app, /id="console-navigation"[^>]*:class="\{ open: sidebarOpen \}"/);
  assert.match(app, /:inert="[^"]*sidebarOpen[^\"]*isMobileNavigation[^"]*"/);
  assert.match(styles, /@media \(max-width: 820px\)[\s\S]*\.sidebar\s*\{[^}]*visibility:\s*hidden[^}]*pointer-events:\s*none/s);
  assert.match(styles, /@media \(max-width: 820px\)[\s\S]*\.sidebar\.open\s*\{[^}]*visibility:\s*visible[^}]*pointer-events:\s*auto/s);
  assert.match(keysPanel, /class="mobile-key-list"/);
  assert.match(keysPanel, /@media \(max-width: 820px\)[\s\S]*\.mobile-key-list\s*\{[^}]*display:\s*grid/s);
});

test("customer dialogs close, trap focus and restore the trigger", async () => {
  const app = await source("apps/console-ui/src/App.vue");
  const handler = app.slice(app.indexOf("function onModalKeydown"), app.indexOf("function sleep"));

  assert.match(app, /const modalRoot = ref<HTMLElement \| null>\(null\)/);
  assert.match(app, /let modalReturnFocus: HTMLElement \| null = null/);
  assert.match(app, /modalReturnFocus\?\.focus\(\)/);
  assert.match(handler, /Escape/);
  assert.match(handler, /Tab/);
  assert.match(handler, /closeModal\(\)/);
  assert.match(handler, /preventDefault\(\)/);
  assert.match(handler, /first\.focus\(\)/);
  assert.match(handler, /last\.focus\(\)/);
  assert.match(app, /ref="modalRoot"[^>]*@keydown="onModalKeydown"/);
});

test("responsive Workspace and usage content remains readable", async () => {
  const [app, styles] = await Promise.all([
    source("apps/console-ui/src/App.vue"),
    source("apps/console-ui/src/styles.css")
  ]);

  assert.match(app, /class="table-wrap request-table-desktop"/);
  assert.match(app, /class="request-list-mobile"/);
  assert.match(styles, /@media \(max-width: 820px\)[\s\S]*\.request-table-desktop,[\s\S]*display:\s*none/s);
  assert.match(styles, /@media \(max-width: 820px\)[\s\S]*\.request-list-mobile,[\s\S]*display:\s*grid/s);
  assert.match(styles, /\.request-list-mobile > article,[\s\S]*min-height:\s*78px/s);
  assert.match(app, /class="panel workspace-access-panel"/);
  assert.match(styles, /\.workspace-access-panel \.data-list a\s*\{[^}]*overflow-wrap:\s*anywhere/s);
});
