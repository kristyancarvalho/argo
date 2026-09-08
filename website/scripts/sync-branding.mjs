import { cp, mkdir, rm } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const website = join(dirname(fileURLToPath(import.meta.url)), "..");
const source = join(website, "..", "assets", "branding");
const destination = join(website, "public", "branding");

const assets = [
  "logo/argo-logo-mark.svg",
  "logo/argo-logo-horizontal.svg",
  "banner/argo-banner.svg",
  "banner/argo-social-preview.png",
];

await rm(destination, { recursive: true, force: true });

for (const asset of assets) {
  const output = join(destination, asset);
  await mkdir(dirname(output), { recursive: true });
  await cp(join(source, asset), output);
}

await mkdir(join(website, "public"), { recursive: true });
await cp(join(source, "logo", "argo-logo-mark.svg"), join(website, "public", "favicon.svg"));
