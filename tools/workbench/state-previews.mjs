import { readFile, mkdir, writeFile, rename } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { homedir } from 'node:os';
import { createHash } from 'node:crypto';

const scenes = {
  'mapping-menu': { title: '按键预设', source: 'cmd/mimac/settings_mapping.go', items: ['使用设备默认', '录入快捷键…'] },
  'tray-menu': { title: '菜单栏', source: 'cmd/mimac/tray.go', items: ['按键映射…', '设置…', '退出 MiMac'] },
};
const libraries = ['libMiMacATVV.dylib', 'libMiMacProcessTap.dylib', 'libMiMacReadyIndicator.dylib', 'libMiMacSettings.dylib', 'libMiMacSpeech.dylib', 'libSiriGlassBridge.dylib'];
const hash = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);
const time = value => typeof value === 'string' && Number.isFinite(Date.parse(value));
const size = value => Number.isFinite(value) && value > 0 && value <= 16384;

// Optional developer captures. This module never queries or starts the App.
export async function statePreviews(out, root = join(homedir(), 'Library/Caches/MiMac/workbench-states')) {
  let latest;
  try { latest = JSON.parse(await readFile(join(root, 'latest.json'), 'utf8')); }
  catch (error) { if (error.code === 'ENOENT') return null; throw error; }
  if (!latest || typeof latest.directory !== 'string' || !/^[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}$/i.test(latest.directory)) {
    throw new Error('Invalid state capture directory');
  }
  const directory = join(root, latest.directory);
  const manifest = JSON.parse(await readFile(join(directory, 'manifest.json'), 'utf8'));
  const app = manifest?.app;
  if (manifest?.schema !== 1 || !time(manifest.capturedAt) || !app || app.bundleIdentifier !== 'dev.mimac.app' ||
      !hash(app.executableSHA256) || !Number.isInteger(app.pid) || app.pid < 1 || !time(app.processStartedAt) ||
      app.binaryIdentity !== 'bundle-on-disk' || typeof app.version !== 'string' || !app.version ||
      !app.nativeLibrarySHA256 || Object.keys(app.nativeLibrarySHA256).length !== libraries.length ||
      libraries.some(name => !hash(app.nativeLibrarySHA256[name])) || !hash(manifest.captureToolSHA256) ||
      ['originalPageRestored', 'foregroundApplicationRestored', 'configurationUnchanged', 'menusDismissed'].some(key => manifest[key] !== true) ||
      !manifest.configurationSHA256 || Object.keys(manifest.configurationSHA256).length !== 3 ||
      ['tools.json', 'mappings.json', 'preferences.json'].some(name => !hash(manifest.configurationSHA256[name])) ||
      !Array.isArray(manifest.pages) || manifest.pages.length !== Object.keys(scenes).length) {
    throw new Error('Incomplete state capture identity or restoration');
  }
  const seen = new Set();
  const validated = [];
  for (const page of manifest.pages) {
    const scene = scenes[page?.id];
    if (!scene || seen.has(page.id) || page.file !== `${page.id}.png` || page.title !== scene.title ||
        page.state !== '菜单展开' || !time(page.capturedAt) || !hash(page.sha256) ||
        !Array.isArray(page.sourcePaths) || page.sourcePaths.length !== 1 || page.sourcePaths[0] !== scene.source ||
        !Array.isArray(page.menuItems) || !scene.items.every(item => page.menuItems.includes(item)) ||
        !Number.isInteger(page.windowID) || page.windowID < 1 || !Number.isInteger(page.openerActionResult) ||
        !Number.isInteger(page.width) || !Number.isInteger(page.height) || !size(page.width) || !size(page.height) ||
        !page.window || !size(page.window.Width) || !size(page.window.Height) ||
        !page.captureExtent || !size(page.captureExtent.width) || !size(page.captureExtent.height) ||
        !Number.isFinite(page.captureExtent.pixelScale) || page.captureExtent.pixelScale < 1 || page.captureExtent.pixelScale > 4 ||
        Math.abs(page.width - page.captureExtent.width * page.captureExtent.pixelScale) >= 1 ||
        Math.abs(page.height - page.captureExtent.height * page.captureExtent.pixelScale) >= 1 ||
        !Array.isArray(page.capturedWindowIDs) || page.capturedWindowIDs.length !== (page.id === 'mapping-menu' ? 2 : 1) ||
        new Set(page.capturedWindowIDs).size !== page.capturedWindowIDs.length ||
        !page.capturedWindowIDs.includes(page.windowID) || page.capturedWindowIDs.some(id => !Number.isInteger(id) || id < 1)) {
      throw new Error('Invalid state capture scene');
    }
    seen.add(page.id);
    const bytes = await readFile(join(directory, page.file));
    if (bytes.length < 24 || bytes.subarray(0, 8).toString('hex') !== '89504e470d0a1a0a' ||
        bytes.readUInt32BE(16) !== page.width || bytes.readUInt32BE(20) !== page.height ||
        createHash('sha256').update(bytes).digest('hex') !== page.sha256) {
      throw new Error(`State capture image mismatch: ${page.id}`);
    }
    validated.push({ page, bytes });
  }
  // Validate the complete group before publishing any image or data reference.
  const target = resolve(out, 'previews');
  await mkdir(target, { recursive: true });
  const pages = [];
  for (const { page, bytes } of validated) {
    const filename = `state-${page.id}-${page.sha256.slice(0, 12)}.png`;
    const destination = join(target, filename);
    await writeFile(destination + '.tmp', bytes);
    await rename(destination + '.tmp', destination);
    pages.push({ ...page, url: `previews/${filename}` });
  }
  return { capturedAt: manifest.capturedAt, app, captureToolSHA256: manifest.captureToolSHA256, pages };
}
