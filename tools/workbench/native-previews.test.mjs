import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtemp, mkdir, readFile, readdir, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { nativePreviews } from './native-previews.mjs';

const pageDefinitions = [
  ['device', '设备', 'cmd/mimac/settings.go'],
  ['mapping', '按键映射', 'cmd/mimac/settings_mapping.go'],
  ['voice', '语音工具', 'cmd/mimac/settings_voice.go'],
  ['plugins', '插件', 'cmd/mimac/settings_mapping.go'],
  ['general', '通用', 'cmd/mimac/native_preferences.go'],
  ['about', '关于', 'cmd/mimac/settings_about.go'],
];
const matrixConfigs = [
  ['light-0', 'light', -1], ['light-1', 'light', 0], ['light-2', 'light', 1],
  ['dark-0', 'dark', -1], ['dark-1', 'dark', 0], ['dark-2', 'dark', 1],
];
const app = {
  binaryIdentity: 'bundle-on-disk',
  bundleIdentifier: 'dev.mimac.app',
  executableSHA256: 'a'.repeat(64),
  pid: 1234,
  processStartedAt: '2026-11-05T15:18:51Z',
  version: '0.1.0',
};

function pngFor(pageID, captureKey, width = 1800, height = 1360) {
  const bytes = Buffer.alloc(32);
  Buffer.from('89504e470d0a1a0a', 'hex').copy(bytes, 0);
  bytes.writeUInt32BE(width, 16);
  bytes.writeUInt32BE(height, 20);
  Buffer.from(`${captureKey}:${pageID}`).copy(bytes, 24);
  return bytes;
}

async function writeCapture(root, captureID, captureKey, overrides = {}) {
  const directory = join(root, captureID);
  await mkdir(directory, { recursive: true });
  const pages = [];
  for (const [id, title, sourcePath] of pageDefinitions) {
    const bytes = pngFor(id, captureKey, overrides.width ?? 1800, overrides.height ?? 1360);
    await writeFile(join(directory, `${id}.png`), bytes);
    pages.push({
      appearance: 'current',
      capturedAt: '2026-11-05T16:25:51Z',
      file: `${id}.png`,
      height: overrides.height ?? 1360,
      id,
      sha256: createHash('sha256').update(bytes).digest('hex'),
      sourcePaths: [sourcePath],
      state: '当前配置',
      title,
      width: overrides.width ?? 1800,
    });
  }
  const manifest = {
    activeWindowCapture: true,
    app: { ...app, ...(overrides.app || {}) },
    capturedAt: '2026-11-05T16:25:57Z',
    foregroundApplicationRestored: true,
    originalPageRestored: true,
    pages,
    schema: 1,
    window: { height: 680, width: 900, ...(overrides.window || {}) },
  };
  await writeFile(join(directory, 'manifest.json'), JSON.stringify(manifest));
  return manifest;
}

async function setup(t) {
  const temporary = await mkdtemp(join(tmpdir(), 'mimac-native-previews-'));
  t.after(() => rm(temporary, { recursive: true, force: true }));
  const root = join(temporary, 'captures');
  const out = join(temporary, 'dist');
  await mkdir(root, { recursive: true });
  return { root, out };
}

async function writeMatrixIndex(root, mutate = entry => entry) {
  const configurations = [];
  for (const [key, appearance, textSize] of matrixConfigs) {
    const captureId = `${String(configurations.length + 1).padStart(8, '0')}-1111-4111-8111-${String(configurations.length + 1).padStart(12, '0')}`;
    await writeCapture(root, captureId, key);
    configurations.push(mutate({
      key,
      appearance,
      textSize,
      captureId,
      bounds: { frontBundle: app.bundleIdentifier, frontPID: app.pid, height: 680, width: 900 },
      preferences: { version: 1, appearance, text_size: textSize },
    }, key));
  }
  await writeFile(join(root, 'matrix-index.json'), JSON.stringify({ schema: 1, configurations }));
}

test('matrix absence preserves the latest gallery shape and retains previous hashed files', async t => {
  const { root, out } = await setup(t);
  const latestID = 'aaaaaaaa-1111-4111-8111-111111111111';
  await writeCapture(root, latestID, 'latest');
  await writeFile(join(root, 'latest.json'), JSON.stringify({ directory: latestID }));
  await mkdir(join(out, 'previews'), { recursive: true });
  await writeFile(join(out, 'previews', 'previous.png'), 'keep');

  const result = await nativePreviews(out, root);
  assert.deepEqual(Object.keys(result).sort(), ['app', 'capturedAt', 'pages', 'window']);
  assert.equal(result.pages.length, 6);
  assert.equal(await readFile(join(out, 'previews', 'previous.png'), 'utf8'), 'keep');
  for (const page of result.pages) assert.match(page.url, /^previews\/[a-z]+-[a-f0-9]{12}\.png$/);
});

test('six matrix configs share validated identity and reuse hashed page output', async t => {
  const { root, out } = await setup(t);
  const latestID = 'bbbbbbbb-1111-4111-8111-111111111111';
  await writeCapture(root, latestID, 'latest');
  await writeFile(join(root, 'latest.json'), JSON.stringify({ directory: latestID }));
  await writeMatrixIndex(root);
  await mkdir(join(out, 'previews'), { recursive: true });
  await writeFile(join(out, 'previews', 'previous.png'), 'keep');

  const result = await nativePreviews(out, root);
  assert.equal(result.pages.length, 6);
  assert.equal(result.matrix.configurations.length, 6);
  assert.deepEqual(result.matrix.configurations.map(item => item.key), matrixConfigs.map(([key]) => key));
  assert.deepEqual(result.matrix.configurations.map(item => item.label), [
    '浅色 小号', '浅色 标准字号', '浅色 大号', '深色 小号', '深色 标准字号', '深色 大号',
  ]);
  assert.ok(result.matrix.configurations.every(item => item.capture.pages.length === 6));
  const files = await readdir(join(out, 'previews'));
  assert.equal(files.length, 43); // 42 capture images plus the retained prior file
  assert.ok(files.includes('previous.png'));
});

test('matrix capture with a different executable SHA is rejected', async t => {
  const { root, out } = await setup(t);
  await writeMatrixIndex(root, (entry, key) => entry);
  const first = matrixConfigs[0][0];
  const firstID = (await readFile(join(root, 'matrix-index.json'), 'utf8'));
  const index = JSON.parse(firstID);
  const capture = join(root, index.configurations.find(item => item.key === first).captureId, 'manifest.json');
  const manifest = JSON.parse(await readFile(capture, 'utf8'));
  manifest.app.executableSHA256 = 'b'.repeat(64);
  await writeFile(capture, JSON.stringify(manifest));
  await assert.rejects(nativePreviews(out, root), /identity, window, or page set changed/);
});

test('matrix requires all six unique theme and size combinations', async t => {
  const { root, out } = await setup(t);
  await writeMatrixIndex(root, (entry, key) => key === 'dark-2' ? { ...entry, key: 'dark-1' } : entry);
  await assert.rejects(nativePreviews(out, root), /Invalid or duplicate native matrix configuration/);
});

test('matrix PNG digest mismatch is rejected', async t => {
  const { root, out } = await setup(t);
  await writeMatrixIndex(root);
  const index = JSON.parse(await readFile(join(root, 'matrix-index.json'), 'utf8'));
  const page = join(root, index.configurations[0].captureId, 'device.png');
  const bytes = await readFile(page);
  bytes[24] ^= 0xff;
  await writeFile(page, bytes);
  await assert.rejects(nativePreviews(out, root), /Native capture mismatch/);
});
