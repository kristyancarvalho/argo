import { readdir, readFile } from "node:fs/promises";
import { extname, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("../dist/", import.meta.url));
const files = [];

async function walk(directory) {
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) await walk(path);
    else files.push(relative(root, path).replaceAll("\\", "/"));
  }
}

await walk(root);
const available = new Set(files);
const errors = [];
const required = [
  "index.html",
  "404.html",
  "docs/index.html",
  "docs/getting-started/index.html",
  "docs/architecture/index.html",
  "changelog/index.html",
  "changelog/v0.8.0/index.html",
  "changelog/v0.8.1/index.html",
  "branding/logo/argo-logo-horizontal.svg",
  "branding/banner/argo-social-preview.png",
];

for (const path of required) {
  if (!available.has(path)) errors.push(`missing required output: ${path}`);
}

const html = new Map();
for (const path of files.filter((file) => file.endsWith(".html"))) {
  html.set(path, await readFile(resolve(root, path), "utf8"));
}

const routeFor = (path) => (path === "index.html" ? "/" : `/${path.replace(/index\.html$/, "")}`);
const candidatesFor = (pathname) => {
  const path = decodeURIComponent(pathname).replace(/^\//, "");
  if (!path) return ["index.html"];
  if (path.endsWith("/")) return [`${path}index.html`];
  if (extname(path)) return [path];
  return [`${path}/index.html`, `${path}.html`];
};

for (const [path, content] of html) {
  for (const forbidden of ["/specs/", "AUDIT-REPORT", "audit-evidence/"]) {
    if (content.includes(forbidden)) errors.push(`${path} exposes internal material: ${forbidden}`);
  }
  const base = new URL(routeFor(path), "https://argo.invalid");
  for (const match of content.matchAll(/(?:href|src)="([^"]+)"/g)) {
    const target = match[1];
    if (/^(?:https?:|mailto:|data:|javascript:)/.test(target)) continue;
    const url = new URL(target, base);
    const candidates = candidatesFor(url.pathname);
    const destination = candidates.find((candidate) => available.has(candidate));
    if (!destination) {
      errors.push(`${path} has unresolved local target: ${target}`);
      continue;
    }
    if (!url.hash || !destination.endsWith(".html")) continue;
    const fragment = decodeURIComponent(url.hash.slice(1));
    if (!fragment) continue;
    const destinationHTML = html.get(destination);
    const escaped = fragment.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    if (!new RegExp(`id=["']${escaped}["']`).test(destinationHTML)) {
      errors.push(`${path} has unresolved fragment: ${target}`);
    }
  }
}

if (files.some((path) => /(^|\/)specs(\/|$)/.test(path))) {
  errors.push("static output contains an internal specs path");
}

if (errors.length) {
  console.error(errors.join("\n"));
  process.exitCode = 1;
} else {
  console.log(`Validated ${html.size} pages and ${files.length} static files.`);
}
