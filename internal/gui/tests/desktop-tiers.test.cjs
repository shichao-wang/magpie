// Claude Desktop has tiers but no main model field: its tiers menu and each
// tier's model picker use the first catalog model unless given their own model.
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const path = require("node:path");
const { test } = require("node:test");
const { chromium, webkit } = require("playwright");

const assets = path.resolve(__dirname, "../assets");
const models = [
  { value: "deepseek/flash", label: "DeepSeek Flash", group: "DeepSeek", icon: "generic", ref: "deepseek/flash" },
  { value: "deepseek/pro", label: "DeepSeek Pro", group: "DeepSeek", icon: "generic", ref: "deepseek/pro" },
];
const tiers = ["opus", "sonnet", "haiku", "fable"];
const row = (id) => `.row.agent[data-id="${id}"]`;

function server(lang, writes, catalog = models) {
  let wired = true;
  let haiku = catalog.length ? "" : "deepseek/private-model";
  const state = () => ({
    agents: [{ id: "claude-desktop", name: "Claude Desktop", icon: "claude-color", path: "/test/Claude", fields: [
      { key: "provider", label: "provider", value: wired ? "magpie" : "", options: [{ value: "magpie", label: "magpie" }] },
      ...tiers.map((key) => ({ key, label: key, value: wired && key === "haiku" ? haiku : "", options: wired ? catalog : [] })),
    ] }, { id: "claude", name: "Claude Code", icon: "claudecode-color", path: "/test/.claude", fields: [
      { key: "model", label: "model", value: "deepseek/pro", options: models },
      ...tiers.map((key) => ({ key, label: key, value: "", options: models })),
    ] }], profiles: [], settings: { lang, theme: "light" },
  });
  return async (route) => {
    const req = route.request(), url = new URL(req.url());
    if (url.pathname === "/boot.js") return route.fulfill({ contentType: "text/javascript", body: `window.bootPrefs = {lang:"${lang}",theme:"light",web:true};` });
    if (url.pathname === "/wails/runtime.js") return route.fulfill({ contentType: "text/javascript", body: "export const Window = {};" });
    if (url.pathname === "/api/state") return route.fulfill({ json: state() });
    if (url.pathname === "/api/set") {
      const data = req.postDataJSON();
      writes.push(data);
      if (data.agent === "claude-desktop" && data.field === "haiku") haiku = data.value;
      if (data.agent === "claude-desktop" && data.field === "provider") wired = !!data.value;
      return route.fulfill({ json: state() });
    }
    if (url.pathname === "/api/usage/quotas") return route.fulfill({ json: [] });
    if (url.pathname === "/api/groups") return route.fulfill({ json: { groups: [] } });
    if (url.pathname === "/api/providers") return route.fulfill({ json: { providers: [], gateway: { running: true } } });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ json: {} });
    const file = path.join(assets, url.pathname === "/" ? "index.html" : url.pathname);
    const contentType = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".png": "image/png" }[path.extname(file)];
    await route.fulfill({ body: await fs.readFile(file), contentType });
  };
}

for (const engine of (process.env.BROWSER ? [process.env.BROWSER] : ["chromium", "webkit"])) {
  for (const lang of ["en", "zh"]) {
    test(`${engine} ${lang}: clear a configured tier with an empty catalog`, async (t) => {
      const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
      t.after(() => browser.close());
      const writes = [];
      const page = await browser.newPage();
      page.setDefaultTimeout(5000);
      await page.route("**/*", server(lang, writes, []));
      await page.goto("http://magpie.test/");
      const b = page.locator(`${row("claude-desktop")} .field[data-key="tiers"]`);
      await b.waitFor();
      assert.match(await b.getAttribute("title"), /haiku: deepseek\/private-model/);
      const fallback = lang === "zh" ? "目录中没有可用模型" : "no catalog model available";
      assert.ok((await b.getAttribute("title")).includes(`opus: ${fallback}`));
      await b.click();
      const menu = page.locator("#list li[data-i]");
      assert.ok((await menu.first().textContent()).includes(fallback));
      await menu.filter({ hasText: "haiku" }).click();
      assert.ok((await page.locator("#list li.reset").textContent()).includes(fallback));
      await page.locator("#list li.reset").click();
      assert.deepEqual(writes.at(-1), { agent: "claude-desktop", field: "haiku", value: "" });
      await b.waitFor({ state: "detached" });
    });
    for (const catalog of [models, [{ value: "group/primary", label: "Primary route", group: "Routing groups", ref: "group/primary" }, ...models]]) {
      test(`${engine} ${lang}: Claude Desktop tiers default to ${catalog[0].label}`, async (t) => {
        const browser = await (engine === "webkit" ? webkit.launch() : chromium.launch({ channel: "chromium" }));
        const errors = [], writes = [];
        t.after(async () => { await browser.close(); assert.deepEqual(errors, []); });
        const page = await (await browser.newContext({ viewport: { width: 980, height: 720 } })).newPage();
        page.setDefaultTimeout(5000);
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/*", server(lang, writes, catalog));
        await page.goto("http://magpie.test/");
        const defaultNote = lang === "zh" ? `未配置档位时使用目录首项（${catalog[0].label}）` : `unconfigured tier uses first catalog model (${catalog[0].label})`;
        const desktop = row("claude-desktop");
        await page.locator(desktop).waitFor();
        const b = page.locator(`${desktop} .field[data-key="tiers"]`);
        assert.equal(await b.count(), 1);
        assert.equal(await page.locator(`${desktop} .field[data-key="model"]`).count(), 0);
        assert.equal(await b.getAttribute("title"), `${lang === "zh" ? "分档：" : "tiers: "}${defaultNote}\n${tiers.map((key) => `${key}: ${defaultNote}`).join("\n")}`);
        const code = page.locator(`${row("claude")} .field[data-key="tiers"]`);
        await code.click();
        assert.ok((await page.locator("#list li[data-i]").first().textContent()).includes(lang === "zh" ? "同主模型（DeepSeek Pro）" : "same as model (DeepSeek Pro)"));
        await page.locator("#list li[data-i]").filter({ hasText: "haiku" }).click();
        assert.ok((await page.locator("#list li.reset").textContent()).includes("DeepSeek Pro"));
        await page.keyboard.press("Escape");

        await b.click();
        const menu = page.locator("#list li[data-i]");
        assert.deepEqual(await menu.locator(".v").allTextContents(), tiers);
        assert.deepEqual(await menu.locator(".n").allTextContents(), tiers.map(() => defaultNote));
        await menu.filter({ hasText: "haiku" }).click();
        const reset = page.locator("#list li.reset");
        assert.ok((await reset.textContent()).includes(defaultNote));
        await page.locator("#list li[data-i]").filter({ hasText: "DeepSeek Pro" }).click();
        assert.deepEqual(writes.at(-1), { agent: "claude-desktop", field: "haiku", value: "deepseek/pro" });
        await page.locator(`${desktop} .field[data-key="tiers"][title*="haiku: DeepSeek Pro"]`).waitFor();

        await b.click();
        await page.locator("#list li[data-i]").filter({ hasText: "haiku" }).click();
        // The configured model is moved to the top; the default still uses the catalog's first.
        assert.ok((await page.locator("#list li.reset").textContent()).includes(defaultNote));
        assert.equal(await page.locator("#list li[data-i]:not(.reset)").first().locator(".v").textContent(), "DeepSeek Pro");
        await page.locator("#list li.reset").click();
        assert.deepEqual(writes.at(-1), { agent: "claude-desktop", field: "haiku", value: "" });
        await page.locator(`${desktop} .field[data-key="tiers"][title*="haiku: ${defaultNote}"]`).waitFor();
        // Without the gateway's provider, no tier choice is offered.
        const provider = page.locator(`${desktop} .field[data-key="provider"]`);
        await provider.click();
        await page.locator("#list li.reset").click();
        assert.deepEqual(writes.at(-1), { agent: "claude-desktop", field: "provider", value: "" });
        await b.waitFor({ state: "detached" });
      });
    }
  }
}
