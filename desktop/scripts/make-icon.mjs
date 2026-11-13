// Generates the tray icon and the Windows installer/exe icon from one shape:
// blue rounded square, white K, amber dot (same mark as renderer/index.html).
// Run: node scripts/make-icon.mjs
import { deflateSync } from 'node:zlib';
import { writeFileSync, mkdirSync } from 'node:fs';

const lerp = (a, b, t) => a + (b - a) * t;
const distSeg = (px, py, ax, ay, bx, by) => {
  const dx = bx - ax, dy = by - ay;
  const t = Math.max(0, Math.min(1, ((px - ax) * dx + (py - ay) * dy) / (dx * dx + dy * dy)));
  return Math.hypot(px - (ax + t * dx), py - (ay + t * dy));
};
const roundedBoxDist = (px, py, cx, cy, hw, hh, r) => {
  const qx = Math.abs(px - cx) - (hw - r);
  const qy = Math.abs(py - cy) - (hh - r);
  return Math.hypot(Math.max(qx, 0), Math.max(qy, 0)) + Math.min(Math.max(qx, qy), 0) - r;
};

// Design space is 32x32 regardless of output size; SS supersamples per pixel.
function render(size, ss) {
  const N = size * ss;
  const scale = 32 / size;
  const acc = new Float32Array(size * size * 4);
  for (let y = 0; y < N; y += 1) {
    for (let x = 0; x < N; x += 1) {
      const u = (x + 0.5) / ss * scale;
      const v = (y + 0.5) / ss * scale;
      if (roundedBoxDist(u, v, 16, 16, 15, 15, 8.5) > 0) continue;
      const t = Math.max(0, Math.min(1, (u + v) / 62));
      let r = lerp(59, 29, t), g = lerp(130, 78, t), b = lerp(246, 216, t);
      const k = Math.min(
        distSeg(u, v, 10.6, 9.6, 10.6, 22.4),
        distSeg(u, v, 11.2, 16, 19.4, 9.4),
        distSeg(u, v, 11.2, 16, 19.4, 22.6),
      );
      const kw = 1.75;
      if (k < kw) { const m = Math.min(1, (kw - k) / 1.1); r = lerp(r, 255, m); g = lerp(g, 255, m); b = lerp(b, 255, m); }
      const dot = Math.hypot(u - 23.4, v - 21.9) - 2.0;
      if (dot < 0) { const m = Math.min(1, -dot / 1.0); r = lerp(r, 240, m); g = lerp(g, 180, m); b = lerp(b, 106, m); }
      const o = ((y / ss | 0) * size + (x / ss | 0)) * 4;
      acc[o] += r; acc[o + 1] += g; acc[o + 2] += b; acc[o + 3] += 1;
    }
  }
  const out = Buffer.alloc(size * size * 4);
  const n = ss * ss;
  for (let i = 0; i < size * size; i += 1) {
    const a = acc[i * 4 + 3];
    const o = i * 4;
    out[o] = a ? Math.round(acc[o] / a) : 0;
    out[o + 1] = a ? Math.round(acc[o + 1] / a) : 0;
    out[o + 2] = a ? Math.round(acc[o + 2] / a) : 0;
    out[o + 3] = Math.round((a / n) * 255);
  }
  return out;
}

const crcTable = (() => {
  const t = new Int32Array(256);
  for (let n = 0; n < 256; n += 1) {
    let c = n;
    for (let k = 0; k < 8; k += 1) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    t[n] = c;
  }
  return t;
})();
const crc32 = (buf) => {
  let c = -1;
  for (const byte of buf) c = crcTable[(c ^ byte) & 0xff] ^ (c >>> 8);
  return (c ^ -1) >>> 0;
};
const chunk = (type, data) => {
  const len = Buffer.alloc(4); len.writeUInt32BE(data.length);
  const body = Buffer.concat([Buffer.from(type, 'latin1'), data]);
  const crc = Buffer.alloc(4); crc.writeUInt32BE(crc32(body));
  return Buffer.concat([len, body, crc]);
};
function png(size, rgba) {
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0); ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8; ihdr[9] = 6;
  const raw = Buffer.alloc((size * 4 + 1) * size);
  for (let y = 0; y < size; y += 1) {
    raw[y * (size * 4 + 1)] = 0;
    rgba.copy(raw, y * (size * 4 + 1) + 1, y * size * 4, (y + 1) * size * 4);
  }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', deflateSync(raw, { level: 9 })),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}

// ICO with PNG-compressed entries (supported since Vista); 256 needs a 0 byte.
function ico(entries) {
  const header = Buffer.alloc(6);
  header.writeUInt16LE(0, 0); header.writeUInt16LE(1, 2); header.writeUInt16LE(entries.length, 4);
  const dir = Buffer.alloc(16 * entries.length);
  let offset = 6 + dir.length;
  entries.forEach(({ size, data }, i) => {
    const o = i * 16;
    dir[o] = size >= 256 ? 0 : size;
    dir[o + 1] = size >= 256 ? 0 : size;
    dir[o + 2] = 0; dir[o + 3] = 0;
    dir.writeUInt16LE(1, o + 4);
    dir.writeUInt16LE(32, o + 6);
    dir.writeUInt32LE(data.length, o + 8);
    dir.writeUInt32LE(offset, o + 12);
    offset += data.length;
  });
  return Buffer.concat([header, dir, ...entries.map((e) => e.data)]);
}

const tray = png(32, render(32, 4));
const big = png(256, render(256, 3));
const sizes = [16, 24, 32, 48, 64, 128, 256];
const icon = ico(sizes.map((size) => ({ size, data: png(size, render(size, size >= 64 ? 2 : 4)) })));

mkdirSync('electron/assets', { recursive: true });
mkdirSync('build', { recursive: true });
writeFileSync('electron/assets/tray.png', tray);
writeFileSync('build/icon.png', big);
writeFileSync('build/icon.ico', icon);
console.log(`tray.png ${tray.length}B, icon.png ${big.length}B, icon.ico ${icon.length}B (${sizes.join(',')})`);
