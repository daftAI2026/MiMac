import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, mkdir, writeFile, readFile, access, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createHash } from 'node:crypto';
import { statePreviews } from './state-previews.mjs';
const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jM1sAAAAASUVORK5CYII=', 'base64');
const hash = createHash('sha256').update(png).digest('hex');
const id = '12345678-abcd-abcd-abcd-123456789abc';
const date = '2026-11-07T00:00:00Z';
const libraries = ['ATVV', 'ProcessTap', 'ReadyIndicator', 'Settings', 'Speech'].map(name => `libMiMac${name}.dylib`).concat('libSiriGlassBridge.dylib');
async function fixture(fn) {
  const root = await mkdtemp(join(tmpdir(), 'mimac-states-'));
  const captures = join(root, 'captures'), directory = join(captures, id), out = join(root, 'dist');
  await mkdir(directory, { recursive: true });
  const pages = ['mapping-menu', 'tray-menu'].map((id, i) => ({ id, title: i ? '菜单栏' : '按键预设', state: '菜单展开', file: `${id}.png`, width: 1, height: 1, sha256: hash, capturedAt: date, windowID: 1, window: { Width: 1, Height: 1 }, capturedWindowIDs: i ? [1] : [1, 2], captureExtent: { width: 1, height: 1, pixelScale: 1 }, openerActionResult: 0, sourcePaths: [i ? 'cmd/mimac/tray.go' : 'cmd/mimac/settings_mapping.go'], menuItems: i ? ['按键映射…', '设置…', '退出 MiMac'] : ['使用设备默认', '录入快捷键…'] }));
  const manifest = { schema: 1, capturedAt: date, app: { bundleIdentifier: 'dev.mimac.app', executableSHA256: hash, binaryIdentity: 'bundle-on-disk', pid: 123, processStartedAt: date, version: 'test', nativeLibrarySHA256: Object.fromEntries(libraries.map(name => [name, hash])) }, captureToolSHA256: hash, originalPageRestored: true, foregroundApplicationRestored: true, configurationUnchanged: true, menusDismissed: true, configurationSHA256: Object.fromEntries(['tools.json', 'mappings.json', 'preferences.json'].map(name => [name, hash])), pages };
  const save = async () => writeFile(join(directory, 'manifest.json'), JSON.stringify(manifest));
  await writeFile(join(captures, 'latest.json'), JSON.stringify({ directory: id }));
  await save();
  for (const page of pages) await writeFile(join(directory, page.file), png);
  try { await fn({ captures, directory, out, manifest, save }); } finally { await rm(root, { recursive: true, force: true }); }
}
test('missing optional state capture keeps static-only builds available', async () => {
  await fixture(async ({ captures, out }) => { await rm(captures, { recursive: true }); assert.equal(await statePreviews(out, captures), null); await assert.rejects(access(out)); });
});
test('complete menu group publishes verified content-hashed originals', async () => {
  await fixture(async ({ captures, out }) => { const result = await statePreviews(out, captures); assert.equal(result.pages.length, 2); for (const p of result.pages) assert.deepEqual(await readFile(join(out, p.url)), png); });
});
for (const [name, mutate] of [
  ['unrestored page', m => { m.originalPageRestored = false; }],
  ['changed configuration', m => { m.configurationUnchanged = false; }],
  ['open menu left behind', m => { m.menusDismissed = false; }],
  ['missing native library identity', m => { delete m.app.nativeLibrarySHA256['libMiMacSpeech.dylib']; }],
  ['missing tool identity', m => { delete m.captureToolSHA256; }],
  ['missing configuration identity', m => { delete m.configurationSHA256['tools.json']; }],
  ['duplicate scene', m => { m.pages[1] = m.pages[0]; }],
  ['wrong actual menu', m => { m.pages[1].menuItems = ['退出其他应用']; }],
  ['path traversal in scene', m => { m.pages[1].file = '../other.png'; }],
  ['unowned capture extent', m => { m.pages[1].capturedWindowIDs = [2]; }],
  ['mismatched image extent', m => { m.pages[1].captureExtent.width = 10; }],
  ['image hash mismatch', m => { m.pages[1].sha256 = '0'.repeat(64); }],
  ['image dimensions mismatch', m => { m.pages[1].width = 2; m.pages[1].captureExtent.width = 2; }],
]) test(`${name} refuses the entire group before publishing`, async () => {
  await fixture(async ({ captures, out, manifest, save }) => { mutate(manifest); await save(); await assert.rejects(statePreviews(out, captures)); await assert.rejects(access(out)); });
});
test('latest directory traversal is rejected', async () => {
  await fixture(async ({ captures, out }) => { await writeFile(join(captures, 'latest.json'), JSON.stringify({ directory: '../outside' })); await assert.rejects(statePreviews(out, captures)); });
});
