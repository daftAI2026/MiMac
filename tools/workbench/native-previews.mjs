import { readFile, mkdir, writeFile, rename } from 'node:fs/promises';
import { resolve, join } from 'node:path';
import { homedir } from 'node:os';
import { createHash } from 'node:crypto';

// Optional development captures. App builds never import this module, and
// fresh workbench checkouts do not depend on the private evidence repository.
const definitions = {
  device: ['设备', 'cmd/mimac/settings.go'],
  mapping: ['按键映射', 'cmd/mimac/settings_mapping.go'],
  voice: ['语音工具', 'cmd/mimac/settings_voice.go'],
  plugins: ['插件', 'cmd/mimac/settings_mapping.go'],
  general: ['通用', 'cmd/mimac/native_preferences.go'],
  about: ['关于', 'cmd/mimac/settings_about.go'],
};
const pageIDs = new Set(Object.keys(definitions));
const uuidPattern = /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/i;
const validTime = value => typeof value === 'string' && Number.isFinite(Date.parse(value));
const validSize = value => Number.isFinite(value) && value > 0 && value <= 16384;
const validSHA256 = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);
const matrixKeys = new Set(['light-0', 'light-1', 'light-2', 'dark-0', 'dark-1', 'dark-2']);

async function readOptionalJSON(path) {
  try { return JSON.parse(await readFile(path, 'utf8')); }
  catch (error) { if (error.code === 'ENOENT') return null; throw error; }
}

function validateMatrixIndex(index) {
  if (!index || typeof index !== 'object' || Array.isArray(index) || index.schema !== 1 ||
      !Array.isArray(index.configurations) || index.configurations.length !== 6) {
    throw new Error('Invalid native matrix index schema');
  }
  const seen = new Set();
  const entries = [];
  for (const entry of index.configurations) {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry) ||
        !['light', 'dark'].includes(entry.appearance) || ![-1, 0, 1].includes(entry.textSize) ||
        entry.key !== `${entry.appearance}-${entry.textSize + 1}` || !matrixKeys.has(entry.key) ||
        seen.has(entry.key) || !uuidPattern.test(entry.captureId)) {
      throw new Error('Invalid or duplicate native matrix configuration');
    }
    const bounds = entry.bounds;
    const preferences = entry.preferences;
    if (!bounds || typeof bounds !== 'object' || bounds.frontBundle !== 'dev.mimac.app' ||
        !Number.isInteger(bounds.frontPID) || bounds.frontPID < 1 ||
        !validSize(bounds.width) || !validSize(bounds.height) ||
        !preferences || typeof preferences !== 'object' || preferences.version !== 1 ||
        preferences.appearance !== entry.appearance ||
        (preferences.text_size ?? 0) !== entry.textSize) {
      throw new Error(`Invalid matrix capture identity or preferences: ${entry.key}`);
    }
    seen.add(entry.key);
    entries.push(entry);
  }
  if (seen.size !== matrixKeys.size || [...matrixKeys].some(key => !seen.has(key))) {
    throw new Error('Native matrix must contain each light/dark and three-size configuration exactly once');
  }
  return entries;
}

function captureSignature(capture) {
  return JSON.stringify({
    bundleIdentifier: capture.app.bundleIdentifier,
    executableSHA256: capture.app.executableSHA256,
    pid: capture.app.pid,
    processStartedAt: capture.app.processStartedAt,
    version: capture.app.version,
    width: capture.window.width,
    height: capture.window.height,
    pages: capture.pages.map(page => ({
      id: page.id,
      title: page.title,
      state: page.state,
      width: page.width,
      height: page.height,
      sourcePaths: page.sourcePaths,
    })).sort((a, b) => a.id.localeCompare(b.id)),
  });
}

async function loadCapture(root, captureId, out, { matrix = false } = {}) {
  if (!uuidPattern.test(captureId)) throw new Error('Invalid native capture directory');
  const directory = join(root, captureId);
  const manifest = await readOptionalJSON(join(directory, 'manifest.json'));
  if (!manifest || !manifest.app || manifest.schema !== 1 ||
      manifest.app.bundleIdentifier !== 'dev.mimac.app' ||
      !validSHA256(manifest.app.executableSHA256) ||
      (matrix && (!Number.isInteger(manifest.app.pid) || manifest.app.pid < 1 ||
        !validTime(manifest.app.processStartedAt) || typeof manifest.app.version !== 'string' || !manifest.app.version ||
        manifest.app.binaryIdentity !== 'bundle-on-disk')) ||
      manifest.originalPageRestored !== true || manifest.activeWindowCapture !== true ||
      (matrix && manifest.foregroundApplicationRestored !== true) ||
      !validSize(manifest.window?.width) || !validSize(manifest.window?.height) ||
      !validTime(manifest.capturedAt) || !Array.isArray(manifest.pages) || manifest.pages.length !== pageIDs.size) {
    throw new Error(`Incomplete native capture manifest: ${captureId}`);
  }
  const remaining = new Set(pageIDs);
  const validated = [];
  for (const page of manifest.pages) {
    if (!page || !remaining.delete(page.id) || page.file !== `${page.id}.png` ||
        !Number.isInteger(page.width) || !Number.isInteger(page.height) ||
        page.width < 1 || page.height < 1 || page.width > 8192 || page.height > 8192) {
      throw new Error(`Invalid native page capture: ${captureId}`);
    }
    const [title, sourcePath] = definitions[page.id];
    if (page.title !== title || page.state !== '当前配置' || !validTime(page.capturedAt) ||
        !Array.isArray(page.sourcePaths) || page.sourcePaths.length !== 1 || page.sourcePaths[0] !== sourcePath ||
        !validSHA256(page.sha256)) {
      throw new Error(`Invalid native page metadata: ${captureId}/${page.id}`);
    }
    const bytes = await readFile(join(directory, page.file));
    const digest = createHash('sha256').update(bytes).digest('hex');
    if (digest !== page.sha256 || bytes.length < 24 || bytes.subarray(0, 8).toString('hex') !== '89504e470d0a1a0a' ||
        bytes.readUInt32BE(16) !== page.width || bytes.readUInt32BE(20) !== page.height) {
      throw new Error(`Native capture mismatch: ${captureId}/${page.id}`);
    }
    validated.push({ page, bytes });
  }
  if (remaining.size !== 0) throw new Error(`Missing native page capture: ${captureId}`);

  // Keep hashed files from the previous gallery usable until data.json is
  // published. No capture or previously published image is removed.
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

function validateMatrixCapture(entry, capture, reference) {
  const bounds = entry.bounds;
  if (capture.app.pid !== bounds.frontPID || capture.app.bundleIdentifier !== bounds.frontBundle ||
      capture.window.width !== bounds.width || capture.window.height !== bounds.height) {
    throw new Error(`Matrix index does not match capture manifest: ${entry.key}`);
  }
  const signature = captureSignature(capture);
  if (reference && signature !== reference) {
    throw new Error(`Native matrix identity, window, or page set changed: ${entry.key}`);
  }
  return signature;
}

function matrixLabel(entry) {
  const appearance = entry.appearance === 'light' ? '浅色' : '深色';
  const size = entry.textSize < 0 ? '小号' : entry.textSize > 0 ? '大号' : '标准字号';
  return `${appearance} ${size}`;
}

export async function nativePreviews(out, root = join(homedir(), 'Library/Caches/MiMac/workbench-captures')) {
  const latest = await readOptionalJSON(join(root, 'latest.json'));
  let latestCapture = null;
  if (latest !== null) {
    if (!latest || typeof latest !== 'object' || Array.isArray(latest) || !uuidPattern.test(latest.directory)) {
      throw new Error('Invalid latest native capture index');
    }
    latestCapture = await loadCapture(root, latest.directory, out);
  }

  const matrixIndex = await readOptionalJSON(join(root, 'matrix-index.json'));
  if (matrixIndex === null) return latestCapture;
  const entries = validateMatrixIndex(matrixIndex);
  const configurations = [];
  let referenceSignature;
  for (const entry of entries) {
    const capture = await loadCapture(root, entry.captureId, out, { matrix: true });
    referenceSignature = validateMatrixCapture(entry, capture, referenceSignature);
    configurations.push({
      key: entry.key,
      appearance: entry.appearance,
      textSize: entry.textSize,
      label: matrixLabel(entry),
      capture,
    });
  }
  const fallback = latestCapture || configurations[0]?.capture || null;
  return fallback ? { ...fallback, ...(latestCapture ? {} : { isMatrixFallback: true }), matrix: { configurations } } : null;
}
