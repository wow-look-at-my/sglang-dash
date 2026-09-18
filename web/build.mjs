// Builds the dashboard UI into web/dist, which main.go embeds.
//
// js-snippets components are imported by URL and stay external: the browser
// fetches them from the library site at runtime, so an upstream fix reaches
// this dashboard without a re-vendor here.
import { build } from "esbuild";
import { cp, mkdir, rm } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const dist = resolve(here, "dist");

await rm(dist, { recursive: true, force: true });
await mkdir(dist, { recursive: true });

const result = await build({
	entryPoints: [resolve(here, "src/main.ts")],
	outfile: resolve(dist, "app.js"),
	bundle: true,
	format: "esm",
	target: "es2022",
	platform: "browser",
	external: ["https://*"],
	sourcemap: false,
	minify: false,
	logLevel: "info",
	legalComments: "none",
});
if (result.errors.length > 0) process.exit(1);

await cp(resolve(here, "src/index.html"), resolve(dist, "index.html"));
await cp(resolve(here, "src/app.css"), resolve(dist, "app.css"));
