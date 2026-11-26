#!/usr/bin/env node
// The web client's dev server: builds web/dist once (build.mjs, with its
// type check), then serves it on http://127.0.0.1:5173, bundling src/
// again on each change and copying index.html again when it changes. A
// page served from loopback over plain http may open ws:// and wss://
// connections, so it reaches local network and Direct TLS / Relay gateways
// (docs/REF-007-web-client.md). The host must trust its origin:
// gorex rexapi web allow http://127.0.0.1:5173
//
// A change of a catalog in web/locales writes src/locales.gen.ts again
// (i18n.mjs), which the watch then bundles; a catalog with errors leaves
// the last good one and prints them.
//
// Usage: npm run dev [-- --port N] [-- --host ADDR]
import { execFileSync } from "node:child_process";
import { copyFileSync, watch } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { appOptions, assetDefines, dist, esbuild } from "./build.mjs";
import { generateOrFail } from "./i18n.mjs";

const web = dirname(fileURLToPath(import.meta.url));
const args = process.argv.slice(2);
const flag = (name, def) => {
  const i = args.indexOf(name);
  return i >= 0 && args[i + 1] ? args[i + 1] : def;
};
const host = flag("--host", "127.0.0.1");
const port = Number(flag("--port", process.env.PORT || "5173"));

// build.mjs takes the rest (--ghostty-source, --rebuild-wasm).
const rest = args.filter((a, i) => !["--host", "--port"].includes(a) && !["--host", "--port"].includes(args[i - 1]));
execFileSync(process.execPath, [join(web, "build.mjs"), ...rest], { stdio: "inherit" });

const { context } = await esbuild();
const defines = assetDefines(dist);
const ctx = await context({
  ...appOptions, entryNames: "[name]", define: { ...appOptions.define, ...defines }, minify: false, sourcemap: "linked",
});
await ctx.watch();
copyFileSync(join(web, "index.html"), join(dist, "index.html"));
watch(join(web, "index.html"), () => {
  try {
    copyFileSync(join(web, "index.html"), join(dist, "index.html"));
  } catch {}
});
// The catalogs: checked and generated again as they change.
let i18nTimer;
watch(join(web, "locales"), (_, file) => {
  if (!file?.endsWith(".json")) return;
  clearTimeout(i18nTimer);
  i18nTimer = setTimeout(() => generateOrFail({ ci: false }).catch((e) => console.error(e.message)), 100);
});
const served = await ctx.serve({ servedir: dist, host, port });
const origin = `http://${served.hosts?.[0] ?? host}:${served.port}`;
console.log(`\nWindRex web client: ${origin}/`);
console.log(`Trust it on a host with: gorex rexapi web allow ${origin}\n`);
