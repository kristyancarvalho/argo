import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL(".", import.meta.url));
const temporary = mkdtempSync(join(tmpdir(), "argo-banner-"));
const description = "A traffic-aware download manager for Linux.";
const layouts = [
  { file: "argo-banner.svg", prefix: "banner", x: 500, y: 86, size: 136, lines: ["A traffic-aware download manager for Linux."], body: 34, top: 268 },
  { file: "argo-social-preview.svg", prefix: "social", x: 584, y: 146, size: 132, lines: ["A traffic-aware download", "manager for Linux."], body: 34, top: 328 },
];

try {
  for (const layout of layouts) {
    const path = join(root, layout.file);
    let source = readFileSync(path, "utf8");
    source = source.replace(/<g id="(?:banner|social)-[^\"]*-glyph-\d+">[\s\S]*?<\/g>/g, "");
    while (/<g>\s*<\/g>/.test(source)) source = source.replace(/<g>\s*<\/g>/g, "");
    source = source.replace(/<g transform="translate\([^\"]+\)">\s*<g fill="[^\"]+" fill-opacity="1">[\s\S]*?<\/g>\s*<\/g>/g, "");
    source = source.replace(/<desc id="description">.*?<\/desc>/, `<desc id="description">Argo. ${description}</desc>`);
    const entries = [
      ["Argo", layout.size, layout.y],
      ...layout.lines.map((line, index) => [line, layout.body, layout.top + index * 48]),
    ];
    const definitions = [];
    const groups = [];
    for (const [index, [text, size, y]] of entries.entries()) {
      const output = join(temporary, "type.svg");
      execFileSync("pango-view", ["--no-display", `--font=DejaVu Serif ${size}`, `--text=${text}`, `--output=${output}`]);
      const rendered = readFileSync(output, "utf8");
      const prefix = `${layout.prefix}-text${index}-glyph-`;
      definitions.push(rendered.match(/<defs>\s*([\s\S]*?)\s*<\/defs>/)[1].replaceAll("glyph-0-", prefix));
      const group = rendered.match(/(<g fill="rgb\(0%, 0%, 0%\)"[\s\S]*?<\/g>)\s*<\/svg>/)[1]
        .replaceAll("glyph-0-", prefix).replaceAll("xlink:href", "href")
        .replace('fill="rgb(0%, 0%, 0%)"', `fill="${index === 0 ? "#F7FBFF" : "#D8EEFF"}"`);
      groups.push(`<g transform="translate(${layout.x} ${y})">\n${group}\n</g>`);
    }
    source = source.replace("</defs>", `${definitions.join("\n")}\n</defs>`);
    source = source.replace("</svg>", `${groups.join("\n")}\n</svg>`);
    writeFileSync(path, source.replace(/\n\s*\n/g, "\n"));
  }
} finally {
  rmSync(temporary, { recursive: true, force: true });
}
