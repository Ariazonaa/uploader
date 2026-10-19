// Copies the Swagger UI assets out of node_modules into public/vendor so the
// docs page can load them from our own origin (no CDN, CSP stays 'self').
//
// This used to be a checked-in 1.5 MB snapshot, which meant the bytes we
// actually served were decoupled from the swagger-ui-dist version in
// package.json: `npm audit` and a Trivy filesystem scan both looked at the
// dependency, while the browser got a frozen copy nobody was tracking. Copying
// at build time keeps the served assets and the scanned dependency identical.
//
// Runs from package.json as `predev` / `prebuild`.
import { copyFileSync, mkdirSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const src = join(here, "..", "node_modules", "swagger-ui-dist");
const dest = join(here, "..", "public", "vendor", "swagger-ui");
const files = ["swagger-ui.css", "swagger-ui-bundle.js"];

if (!existsSync(src)) {
  console.error(`vendor-swagger: ${src} not found — run "npm install" first.`);
  process.exit(1);
}

mkdirSync(dest, { recursive: true });
for (const f of files) {
  copyFileSync(join(src, f), join(dest, f));
}
console.log(`vendor-swagger: copied ${files.length} file(s) to public/vendor/swagger-ui`);
