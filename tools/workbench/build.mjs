import { readFile, writeFile, mkdir, cp } from 'node:fs/promises';
import { execFileSync } from 'node:child_process';
import { dirname, resolve, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';
import { render } from 'pele';
import { nativePreviews } from './native-previews.mjs';
import { statePreviews } from './state-previews.mjs';

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, '../..');
const out = resolve(here, 'dist');
const go = process.env.MIMAC_GO || 'go';
const data = JSON.parse(execFileSync(go, ['run', './tools/workbench/export', '-root', root], { cwd: root, encoding: 'utf8' }));
const manifest = JSON.parse(await readFile(resolve(here, 'diagrams/manifest.json'), 'utf8'));
const diagrams = [];
const states = await statePreviews(out);
const sourcePaths = new Set(['DESIGN.md', 'cmd/mimac/design.go', 'Assets/Brand/brand.go']);
for (const item of manifest) {
  const path = resolve(here, 'diagrams', `${item.id}.mmd`);
  const source = await readFile(path, 'utf8');
  const result = render(source, {
    fontFamily: '-apple-system, BlinkMacSystemFont, "PingFang SC", sans-serif',
    // Pele's edge captions are two points smaller than node labels.
    fontSize: 14, idPrefix: item.id, responsive: true, links: false, images: false,
    padding: 20,
  });
  if (!result.svg.includes('<svg')) throw new Error(`No SVG rendered for ${item.id}`);
  const svg = result.svg.replace('<svg ', `<svg stroke-width="${data.tokens.lineWidth}" `);
  diagrams.push({ ...item, source, svg, width: result.width, height: result.height });
  for (const path of item.sourcePaths) sourcePaths.add(path);
}
for (const page of states?.pages || []) for (const path of page.sourcePaths) sourcePaths.add(path);
for (const path of [
  'cmd/mimac/settings.go', 'cmd/mimac/settings_mapping.go', 'cmd/mimac/settings_voice.go',
  'cmd/mimac/native_preferences.go', 'cmd/mimac/settings_about.go', 'cmd/mimac/remote.go',
  'internal/platform/readyindicator/native/ReadyIndicator.m',
]) sourcePaths.add(path);
await mkdir(resolve(out, 'source'), { recursive: true });
const sourceIndex = {};
for (const path of sourcePaths) {
  const absolute = resolve(root, path);
  if (relative(root, absolute).startsWith('..')) throw new Error(`Source outside project: ${path}`);
  const content = await readFile(absolute, 'utf8');
  const digest = createHash('sha256').update(content).digest('hex');
  const filename = `${digest.slice(0, 12)}.txt`;
  await writeFile(resolve(out, 'source', filename), content);
  sourceIndex[path] = { url: `source/${filename}`, sha256: digest };
}
data.diagrams = diagrams;
data.sourceIndex = sourceIndex;
data.nativePreviews = await nativePreviews(out);
data.statePreviews = states;
data.revision = execFileSync('git', ['rev-parse', '--short', 'HEAD'], { cwd: root, encoding: 'utf8' }).trim();
data.builtAt = new Date().toISOString();
data.renderer = 'Pele 1.12.0';
await writeFile(resolve(out, 'data.json'), JSON.stringify(data));
const paletteCSS = Object.entries(data.palettes).map(([mode, palette]) =>
  `[data-theme="${mode}"] {\n${Object.entries(palette).map(([name, value]) => `  --${name}: ${value};`).join('\n')}\n}`
).join('\n');
const numericCSS = `:root {\n${Object.entries(data.tokens).map(([name, value]) => `  --${name}: ${value}px;`).join('\n')}\n}`;
await writeFile(resolve(out, 'tokens.css'), `${numericCSS}\n${paletteCSS}\n`);
for (const file of ['index.html', 'style.css', 'app.js', 'diagram-canvas.js']) await cp(resolve(here, file), resolve(out, file));
await mkdir(resolve(out, 'brand'), { recursive: true });
for (const file of ['mark.svg', 'app-icon.png', 'about-icon.png', 'tray.png']) {
  await cp(resolve(root, 'Assets/Brand', file), resolve(out, 'brand', file));
}
await cp(resolve(here, 'node_modules/pele/LICENSE'), resolve(out, 'PELE-LICENSE.txt'));
console.log(`Built ${diagrams.length} diagrams, ${Object.keys(data.tokens).length} tokens and ${sourcePaths.size} source snapshots.`);
