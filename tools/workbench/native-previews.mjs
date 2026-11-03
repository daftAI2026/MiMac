import { readFile, mkdir, writeFile, rename } from 'node:fs/promises';
import { resolve, join } from 'node:path';
import { homedir } from 'node:os';
import { createHash } from 'node:crypto';

// Optional development captures. App builds never import this module, and
// fresh workbench checkouts do not depend on the private evidence repository.
export async function nativePreviews(out) {
  const root = join(homedir(), 'Library/Caches/MiMac/workbench-captures');
  let latest;
  try { latest = JSON.parse(await readFile(join(root, 'latest.json'), 'utf8')); }
  catch (error) { if (error.code === 'ENOENT') return null; throw error; }
  if (!/^[a-f0-9-]{36}$/i.test(latest.directory)) throw new Error('Invalid native capture directory');
  const directory = join(root, latest.directory);
  const manifest = JSON.parse(await readFile(join(directory, 'manifest.json'), 'utf8'));
  const definitions = {
    device: ['设备', 'cmd/mimac/settings.go'], mapping: ['按键映射', 'cmd/mimac/settings_mapping.go'],
    voice: ['语音工具', 'cmd/mimac/settings_voice.go'], plugins: ['插件', 'cmd/mimac/settings_mapping.go'],
    general: ['通用', 'cmd/mimac/native_preferences.go'], about: ['关于', 'cmd/mimac/settings_about.go'],
  };
  const ids = new Set(Object.keys(definitions));
  const validTime = value => typeof value === 'string' && Number.isFinite(Date.parse(value));
  const validSize = value => Number.isFinite(value) && value > 0 && value <= 16384;
  if (manifest.schema !== 1 || manifest.app?.bundleIdentifier !== 'dev.mimac.app' ||
      !/^[a-f0-9]{64}$/.test(manifest.app.executableSHA256) || manifest.originalPageRestored !== true || manifest.activeWindowCapture !== true ||
      !validSize(manifest.window?.width) || !validSize(manifest.window?.height) || !validTime(manifest.capturedAt) ||
      !Array.isArray(manifest.pages) || manifest.pages.length !== ids.size) throw new Error('Incomplete native capture manifest');
  const validated = [];
  for (const page of manifest.pages) {
    if (!ids.delete(page.id) || page.file !== `${page.id}.png` || !Number.isInteger(page.width) || !Number.isInteger(page.height) ||
        page.width < 1 || page.height < 1 || page.width > 8192 || page.height > 8192) throw new Error('Invalid native page capture');
    const definition = definitions[page.id];
    if (page.title !== definition[0] || page.state !== '当前配置' || !validTime(page.capturedAt) ||
        !Array.isArray(page.sourcePaths) || page.sourcePaths.length !== 1 || page.sourcePaths[0] !== definition[1]) throw new Error('Invalid native page metadata');
    const bytes = await readFile(resolve(directory, page.file));
    const digest = createHash('sha256').update(bytes).digest('hex');
    if (digest !== page.sha256 || bytes.length < 24 || bytes.subarray(0, 8).toString('hex') !== '89504e470d0a1a0a' ||
        bytes.readUInt32BE(16) !== page.width || bytes.readUInt32BE(20) !== page.height) throw new Error(`Native capture mismatch: ${page.id}`);
    validated.push({ page, bytes });
  }
  // Keep the previous manifest's hashed images usable until data.json is
  // published. A failed new build must not delete the working gallery.
  const target = resolve(out, 'previews');
  await mkdir(target, { recursive: true });
  const pages = [];
  for (const { page, bytes } of validated) {
    const filename = `${page.id}-${page.sha256.slice(0, 12)}.png`;
    const temporary = join(target, filename + '.tmp');
    await writeFile(temporary, bytes);
    await rename(temporary, join(target, filename));
    pages.push({ ...page, url: `previews/${filename}` });
  }
  return { capturedAt: manifest.capturedAt, app: manifest.app, window: manifest.window, pages };
}
