#!/usr/bin/env node
// Builds the frontend into ../internal/web/dist so it can be embedded in the
// Go binary with go:embed.
//
// Steps:
//   1. compile Tailwind CSS from web/src/app.css
//   2. copy index.html, app.js and static assets
//   3. write a build manifest so the Go binary can report how it was built
//
// The script is deliberately dependency-light: only the Tailwind CLI is
// required and it is pinned in package.json.

import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { cpSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const srcDir = join(here, "src");
const outDir = join(here, "..", "internal", "web", "dist");
const assetsDir = join(outDir, "assets");

function run(command, args) {
  process.stdout.write(`> ${command} ${args.join(" ")}\n`);
  execFileSync(command, args, { stdio: "inherit", cwd: here });
}

// 1. Start from a clean output directory so stale files never end up embedded.
//    (.gitkeep is preserved: it keeps the directory tracked in git so that
//    `go build` works before the frontend is ever built.)
rmSync(assetsDir, { recursive: true, force: true });
rmSync(join(outDir, "index.html"), { force: true });
rmSync(join(outDir, "build-manifest.json"), { force: true });
mkdirSync(assetsDir, { recursive: true });

// 2. Tailwind CSS (v4 CLI, no config file needed: the theme lives in app.css).
const tailwind = join(here, "node_modules", ".bin", process.platform === "win32" ? "tailwindcss.cmd" : "tailwindcss");
run(tailwind, ["--input", join(srcDir, "app.css"), "--output", join(assetsDir, "app.css"), "--minify"]);

// 3. Copy the static frontend files.
cpSync(join(srcDir, "index.html"), join(outDir, "index.html"));
cpSync(join(srcDir, "app.js"), join(assetsDir, "app.js"));
cpSync(join(srcDir, "favicon.svg"), join(assetsDir, "favicon.svg"));

// 4. Record a manifest (sizes + hashes) used by the Go server for ETags and by
//    maintainers to verify that the embedded assets match the sources.
const files = ["index.html", "assets/app.css", "assets/app.js", "assets/favicon.svg"];
const manifest = {
  generatedAt: new Date().toISOString(),
  tailwind: execFileSync(tailwind, ["--help"], { encoding: "utf8" }).match(/tailwindcss v([\w.-]+)/)?.[1] ?? "unknown",
  files: Object.fromEntries(
    files.map((file) => {
      const content = readFileSync(join(outDir, file));
      return [
        file,
        {
          bytes: content.byteLength,
          sha256: createHash("sha256").update(content).digest("hex"),
        },
      ];
    }),
  ),
};
writeFileSync(join(outDir, "build-manifest.json"), `${JSON.stringify(manifest, null, 2)}\n`);

const total = Object.values(manifest.files).reduce((sum, file) => sum + file.bytes, 0);
process.stdout.write(`frontend built into ${outDir} (${files.length} files, ${total} bytes)\n`);
