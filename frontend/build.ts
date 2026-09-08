#!/usr/bin/env bun
import tailwind from "bun-plugin-tailwind";
import { rm } from "node:fs/promises";
import path from "node:path";

const root = import.meta.dir;
const sourceDirectory = path.join(root, "src");
const outputDirectory = path.join(root, "dist");
const apiUrl = process.env.BUN_PUBLIC_API_URL?.trim();

if (!apiUrl) {
  throw new Error("BUN_PUBLIC_API_URL is required for a production build");
}

const parsedApiUrl = new URL(apiUrl);
if (parsedApiUrl.protocol !== "http:" && parsedApiUrl.protocol !== "https:") {
  throw new Error("BUN_PUBLIC_API_URL must use http or https");
}

const entrypoints = [...new Bun.Glob("**/*.html").scanSync(sourceDirectory)]
  .map((entrypoint) => path.join(sourceDirectory, entrypoint))
  .toSorted();

if (entrypoints.length !== 2) {
  throw new Error(`Expected two HTML entrypoints, found ${entrypoints.length}`);
}

await rm(outputDirectory, { recursive: true, force: true });

const result = await Bun.build({
  entrypoints,
  outdir: outputDirectory,
  plugins: [tailwind],
  minify: true,
  splitting: true,
  target: "browser",
  packages: "bundle",
  publicPath: "/",
  env: "disable",
  define: {
    "process.env.NODE_ENV": JSON.stringify("production"),
    __TV_POLL_API_URL__: JSON.stringify(apiUrl.replace(/\/$/, "")),
  },
  naming: {
    chunk: "assets/[name]-[hash].[ext]",
    asset: "assets/[name]-[hash].[ext]",
  },
});

if (!result.success) {
  for (const log of result.logs) {
    console.error(log);
  }
  process.exit(1);
}

for (const output of result.outputs) {
  const file = path.relative(root, output.path);
  console.log(`${file} ${output.size} bytes`);
}
