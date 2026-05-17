// SPDX-License-Identifier: AGPL-3.0-or-later

import fs from "node:fs";
import path from "node:path";
import process from "node:process";

const rootDir = process.cwd();
const nodeModulesDir = path.join(rootDir, "web", "management", "node_modules");
const outputArgIndex = process.argv.indexOf("--output");
const outputDir = outputArgIndex >= 0 && process.argv[outputArgIndex + 1]
  ? path.resolve(process.argv[outputArgIndex + 1])
  : path.join(rootDir, "dist", "legal", "third-party", "web-management");

if (!fs.existsSync(nodeModulesDir)) {
  throw new Error(`Missing ${nodeModulesDir}; run bun install in web/management first.`);
}

fs.rmSync(outputDir, { recursive: true, force: true });
fs.mkdirSync(outputDir, { recursive: true });

const packages = new Map();

function collectPackages(currentNodeModulesDir) {
  for (const entry of fs.readdirSync(currentNodeModulesDir, { withFileTypes: true })) {
    if (!entry.isDirectory() || entry.name.startsWith(".")) continue;

    const entryPath = path.join(currentNodeModulesDir, entry.name);
    if (entry.name.startsWith("@")) {
      for (const scopedEntry of fs.readdirSync(entryPath, { withFileTypes: true })) {
        if (scopedEntry.isDirectory()) {
          collectPackage(path.join(entryPath, scopedEntry.name));
        }
      }
      continue;
    }

    collectPackage(entryPath);
  }
}

function collectPackage(packageDir) {
  const manifestPath = path.join(packageDir, "package.json");
  if (!fs.existsSync(manifestPath)) return;

  const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
  const name = manifest.name || path.basename(packageDir);
  const version = manifest.version || "0.0.0";
  const key = `${name}@${version}`;
  if (!packages.has(key)) {
    packages.set(key, { dir: packageDir, name, version, license: packageLicense(manifest) });
  }

  const nestedNodeModules = path.join(packageDir, "node_modules");
  if (fs.existsSync(nestedNodeModules)) {
    collectPackages(nestedNodeModules);
  }
}

function packageLicense(manifest) {
  if (typeof manifest.license === "string" && manifest.license.trim()) {
    return manifest.license.trim();
  }
  if (Array.isArray(manifest.licenses) && manifest.licenses.length > 0) {
    return manifest.licenses.map((license) => {
      if (typeof license === "string") return license;
      return license.type || license.name || "UNKNOWN";
    }).join(" OR ");
  }
  return "UNKNOWN";
}

function licenseFileNames(packageDir) {
  return fs.readdirSync(packageDir)
    .filter((name) => /^(licen[cs]e|copying|notice)([._ -].*)?$/i.test(name))
    .sort((a, b) => a.localeCompare(b));
}

function safePackageDirName(name, version) {
  return `${name}@${version}`.replaceAll("/", "__").replaceAll("@", "_");
}

collectPackages(nodeModulesDir);

const summary = [
  "p2pstream web management third-party packages",
  "",
  "Package\tLicense",
];

for (const pkg of [...packages.values()].sort((a, b) => `${a.name}@${a.version}`.localeCompare(`${b.name}@${b.version}`))) {
  summary.push(`${pkg.name}@${pkg.version}\t${pkg.license}`);

  const names = licenseFileNames(pkg.dir);
  if (names.length === 0) continue;

  const packageOutputDir = path.join(outputDir, safePackageDirName(pkg.name, pkg.version));
  fs.mkdirSync(packageOutputDir, { recursive: true });
  for (const name of names) {
    fs.copyFileSync(path.join(pkg.dir, name), path.join(packageOutputDir, name));
  }
}

fs.writeFileSync(path.join(outputDir, "SUMMARY.txt"), `${summary.join("\n")}\n`);
