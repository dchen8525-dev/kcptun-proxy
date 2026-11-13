// Copy the freshly built Go kcptun-client into the desktop resources dir so the
// Electron shell can ship it next to the app (asar:false keeps it on disk). The
// shell resolves the binary from here when packaged, or from ../../dist in dev.
import { copyFileSync, mkdirSync, existsSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, '..', '..');
const resDir = resolve(here, '..', 'resources');
mkdirSync(resDir, { recursive: true });

const platform = process.platform;
const srcName = platform === 'win32'
  ? 'kcptun-client-windows-amd64.exe'
  : platform === 'linux'
    ? 'kcptun-client-linux-amd64'
    : null;
if (!srcName) {
  console.error(`copy-binary: unsupported platform ${platform}`);
  process.exit(1);
}
const dstName = platform === 'win32' ? 'kcptun-client.exe' : 'kcptun-client';
const src = resolve(root, 'dist', srcName);
const dst = resolve(resDir, dstName);

if (!existsSync(src)) {
  console.error(`copy-binary: missing build artifact: ${src}\nrun scripts/build.sh first`);
  process.exit(1);
}
copyFileSync(src, dst);
console.log(`copied ${src} -> ${dst}`);
