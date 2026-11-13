// Electron main process: window + kcptun-client child process + a real SOCKS
// download speed test. The tunnel is a prebuilt Go binary spawned exactly the
// way the VPS systemd unit runs kcptun-client, so a tunnel crash cannot take the
// UI down.
'use strict';

const { app, BrowserWindow, ipcMain, dialog, shell, Tray, Menu, nativeImage } = require('electron');
const { spawn } = require('node:child_process');
const path = require('node:path');
const fs = require('node:fs');
const net = require('node:net');
const tls = require('node:tls');

// Surface any main-process failure instead of dying silently behind the window.
process.on('uncaughtException', (e) => { console.error('[kcptun-desktop] uncaught:', e); });
process.on('unhandledRejection', (e) => { console.error('[kcptun-desktop] unhandled:', e); });

const IS_PACKAGED = app.isPackaged;

// In dev the Go binary lives next to the repo's dist/ output under its
// release name; once packaged copy-binary.mjs stages it under resources/ and
// electron-builder's extraResources puts it directly in process.resourcesPath.
// The two names differ, and the packaged one is not versioned.
const PLATFORM_BIN = process.platform === 'win32'
  ? 'kcptun-client-windows-amd64.exe'
  : process.platform === 'linux'
    ? 'kcptun-client-linux-amd64'
    : null;
const PACKAGED_BIN = process.platform === 'win32' ? 'kcptun-client.exe' : 'kcptun-client';
const CLI_PATH = !PLATFORM_BIN
  ? null
  : IS_PACKAGED
    ? path.join(process.resourcesPath, PACKAGED_BIN)
    : path.join(__dirname, '..', '..', 'dist', PLATFORM_BIN);

const SETTINGS_PATH = () => path.join(app.getPath('userData'), 'settings.json');

let win = null;
let tray = null;
let isQuitting = false;
let trayHinted = false;
let child = null;
let childState = { running: false, pid: 0, startedAt: 0, peer: '', listen: '' };

// ---------------------------------------------------------------- settings

function readSettings() {
  try {
    return JSON.parse(fs.readFileSync(SETTINGS_PATH(), 'utf8'));
  } catch {
    return {};
  }
}

function writeSettings(patch) {
  const next = { ...readSettings(), ...patch };
  try {
    fs.mkdirSync(path.dirname(SETTINGS_PATH()), { recursive: true });
    // The file holds the PSK in clear text; keep it owner-only where the
    // platform honours modes (Linux/macOS).
    fs.writeFileSync(SETTINGS_PATH(), JSON.stringify(next, null, 2), {
      encoding: 'utf8',
      mode: 0o600,
    });
  } catch (e) {
    send('kcp:log', { level: 'warn', line: `settings: ${e.message}` });
  }
  return next;
}

function send(channel, payload) {
  if (win && !win.isDestroyed()) win.webContents.send(channel, payload);
}

// ---------------------------------------------------------------- tunnel

// IPC is a trust boundary: whatever the renderer sends becomes argv elements
// and a log-file path, so it is validated here instead of relying on the form.
const HEX32 = /^[0-9a-fA-F]{32}$/;
const LOG_LEVELS = new Set(['debug', 'info', 'warn', 'error', 'off']);

function intIn(v, lo, hi) {
  const n = Number(v);
  return Number.isInteger(n) && n >= lo && n <= hi ? n : null;
}

function joinHostPort(host, port) {
  const h = String(host).trim();
  if (h.startsWith('[')) return `${h}:${port}`;
  return h.includes(':') ? `[${h}]:${port}` : `${h}:${port}`;
}

// Returns a normalized config with the argv-ready peer/bind strings, or
// { error } when the renderer sent something the CLI would reject anyway.
function tunnelConfig(raw) {
  const cfg = raw && typeof raw === 'object' ? raw : {};
  const key = String(cfg.key || '').replace(/^0x/i, '').trim().replace(/\s+/g, '');
  if (!HEX32.test(key)) return { error: 'key must be 32 hex characters' };

  const host = typeof cfg.host === 'string' ? cfg.host.trim() : '';
  if (!host || /[\s\0]/.test(host)) return { error: 'invalid server host' };
  const listenHost = typeof cfg.listenHost === 'string' && cfg.listenHost.trim()
    ? cfg.listenHost.trim() : '127.0.0.1';
  if (/[\s\0]/.test(listenHost)) return { error: 'invalid listen host' };

  const port = intIn(cfg.port, 1, 65535);
  const listenPort = intIn(cfg.listenPort, 1, 65535);
  if (port === null) return { error: 'server port must be 1..65535' };
  if (listenPort === null) return { error: 'listen port must be 1..65535' };

  let mtu = null;
  if (cfg.mtu !== undefined && cfg.mtu !== null && cfg.mtu !== '') {
    mtu = intIn(cfg.mtu, 576, 1500);
    if (mtu === null) return { error: 'MTU must be 576..1500' };
  }
  const fec = cfg.fec === undefined || cfg.fec === null || cfg.fec === ''
    ? '10:3'
    : String(cfg.fec).trim();
  if (!/^\d{1,3}:\d{1,3}$/.test(fec)) return { error: 'FEC must look like 10:3' };

  const logLevel = LOG_LEVELS.has(cfg.logLevel) ? cfg.logLevel : 'info';
  const logFile = typeof cfg.logFile === 'string' ? cfg.logFile : '';
  if (logFile.includes('\0')) return { error: 'invalid log file path' };

  return {
    key,
    peer: joinHostPort(host, port),
    bind: joinHostPort(listenHost, listenPort),
    displayPeer: `${host}:${port}`,
    displayListen: `${listenHost}:${listenPort}`,
    mtu,
    fec,
    logLevel,
    logFile,
  };
}

function statsArgs(cfg) {
  // kcptun has no PMTUD: one fixed --mtu budget, not --mtu-min/--mtu-max. FEC is
  // a single D:P geometry, not a separate parity controller.
  const args = [`--key=${cfg.key}`, `--peer=${cfg.peer}`, `-b=${cfg.bind}`];
  if (cfg.mtu) args.push(`--mtu=${cfg.mtu}`);
  args.push(`--fec=${cfg.fec}`, `--log-level=${cfg.logLevel}`);
  if (cfg.logFile) args.push(`--log-file=${cfg.logFile}`);
  return args;
}

function classify(line) {
  if (line.startsWith('DBG ')) return 'debug';
  if (line.startsWith('WRN ')) return 'warn';
  if (line.startsWith('ERR ')) return 'error';
  if (/\b(error|fail|refused|exhaust)\b/i.test(line)) return 'error';
  if (/\b(warn|retrans|loss)\b/i.test(line)) return 'warn';
  return 'info';
}

// Same machine-readable line FCP emits: "stats 12.4s live=1 up=1 tunnels=1
// tx=..B rx=..B tx_rate=..B/s rx_rate=..B/s" -- kcptun's Reporter uses the
// identical format, so this regex is reused verbatim.
const STATS_RE = /^stats\s+([\d.]+)s\s+live=(\d+)\s+up=(\d+)\s+tunnels=(\d+)\s+tx=(\d+)B\s+rx=(\d+)B\s+tx_rate=(-?\d+)B\/s\s+rx_rate=(-?\d+)B\/s/;

function idleState() {
  return { running: false, pid: 0, startedAt: 0, peer: '', listen: '' };
}

async function startTunnel(raw) {
  // Wait for the previous process to release the listen port before spawning
  // its replacement; a fire-and-forget kill leaves both bound to the same port.
  if (child) await stopTunnel();

  const cfg = tunnelConfig(raw);
  if (cfg.error) {
    send('kcp:log', { level: 'error', line: `config: ${cfg.error}` });
    return { ok: false, error: cfg.error };
  }
  if (!CLI_PATH) {
    return { ok: false, error: `unsupported platform: ${process.platform}` };
  }
  if (!fs.existsSync(CLI_PATH)) {
    send('kcp:log', { level: 'error', line: `missing kcptun-client binary: ${CLI_PATH}` });
    return { ok: false, error: 'missing kcptun-client binary' };
  }

  let proc;
  try {
    proc = spawn(CLI_PATH, statsArgs(cfg), {
      windowsHide: true,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
  } catch (e) {
    return { ok: false, error: `spawn failed: ${e.message}` };
  }
  // Attach before any early return: an asynchronous ENOENT/EACCES arrives as an
  // 'error' event, and an unhandled one would crash the main process.
  proc.on('error', (e) => {
    send('kcp:log', { level: 'error', line: `tunnel spawn failed: ${e.message}` });
    if (child !== proc) return;
    child = null;
    childState = idleState();
    send('kcp:state', childState);
    refreshTray();
  });
  // spawn reports ENOENT/EACCES asynchronously, but pid is already unset then;
  // returning ok for a process that never started would leave the UI lying.
  if (!proc.pid) return { ok: false, error: 'spawn failed' };

  child = proc;
  childState = {
    running: true, pid: proc.pid, startedAt: Date.now(),
    peer: cfg.displayPeer, listen: cfg.displayListen,
  };
  send('kcp:state', childState);
  refreshTray();

  let buf = '';
  proc.stdout.setEncoding('utf8');
  proc.stdout.on('data', (chunk) => {
    buf += chunk;
    let idx;
    while ((idx = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, idx).trim();
      buf = buf.slice(idx + 1);
      if (!line) continue;
      const m = STATS_RE.exec(line);
      if (m) {
        send('kcp:stats', {
          uptimeSec: Number(m[1]), live: Number(m[2]), up: Number(m[3]),
          tunnels: Number(m[4]), tx: Number(m[5]), rx: Number(m[6]),
          txRate: Number(m[7]), rxRate: Number(m[8]),
        });
      } else {
        send('kcp:log', { level: 'info', line });
      }
    }
  });
  proc.stderr.setEncoding('utf8');
  proc.stderr.on('data', (chunk) => {
    for (const line of chunk.split('\n')) {
      const text = line.trimEnd();
      if (!text) continue;
      send('kcp:log', { level: classify(text), line: text });
    }
  });
  proc.on('exit', (code, signal) => {
    // A stopped or superseded process must not clear the state of its
    // successor: stopTunnel() already nulled `child` before killing it.
    if (child !== proc) return;
    child = null;
    childState = idleState();
    send('kcp:state', childState);
    refreshTray();
    send('kcp:log', { level: code === 0 ? 'info' : 'error',
      line: `tunnel exited: code=${code} signal=${signal || 'none'}` });
  });
  return { ok: true, pid: proc.pid };
}

function stopTunnel() {
  const proc = child;
  if (!proc) return Promise.resolve({ ok: true });
  child = null;
  childState = idleState();
  send('kcp:state', childState);
  refreshTray();
  return new Promise((resolve) => {
    let settled = false;
    const finish = () => {
      if (settled) return;
      settled = true;
      resolve({ ok: true });
    };
    proc.once('exit', finish);
    try {
      // SIGTERM lets the tunnel close its sockets; the forced kill is the escape
      // hatch for a wedged process (Windows has no SIGTERM delivery for GUI
      // children, so TerminateProcess is what actually lands there).
      proc.kill('SIGTERM');
    } catch {
      finish();
      return;
    }
    setTimeout(() => { try { proc.kill('SIGKILL'); } catch { finish(); } }, 1500);
    // Never hang a restart on a process the OS refuses to reap.
    setTimeout(finish, 3000);
  });
}

// ---------------------------------------------------------------- speed test
//
// The Go client has a built-in --speedtest, but that is an in-process loopback
// that says nothing about the path the user cares about. This one dials the
// local SOCKS listener the tunnel exposes and pulls a real URL through it.

function ipv6Bytes(ip) {
  const expandV4 = (parts) => {
    if (!parts.length) return parts;
    const last = parts[parts.length - 1];
    if (!last.includes('.')) return parts;
    const octets = last.split('.').map(Number);
    if (octets.length !== 4 || octets.some((n) => !Number.isInteger(n) || n < 0 || n > 255)) return null;
    return parts.slice(0, -1).concat(
      (((octets[0] << 8) | octets[1]) >>> 0).toString(16),
      (((octets[2] << 8) | octets[3]) >>> 0).toString(16));
  };
  let head = ip;
  let tail = '';
  const dbl = ip.indexOf('::');
  if (dbl >= 0) {
    head = ip.slice(0, dbl);
    tail = ip.slice(dbl + 2);
  }
  const h = expandV4(head ? head.split(':') : []);
  const t = expandV4(tail ? tail.split(':') : []);
  if (!h || !t) return null;
  const missing = 8 - h.length - t.length;
  // "::" stands for at least one zero group, so a full 8-group address must not
  // carry it; the caller has already validated the literal with net.isIP.
  if (missing < 0 || (dbl < 0 ? missing !== 0 : missing < 1)) return null;
  const groups = [...h, ...Array(missing).fill('0'), ...t];
  const out = Buffer.alloc(16);
  for (let i = 0; i < 8; i += 1) {
    const g = parseInt(groups[i], 16);
    if (!Number.isInteger(g) || g < 0 || g > 0xffff) return null;
    out.writeUInt16BE(g, i * 2);
  }
  return out;
}

function buildConnectRequest(host, port) {
  const bare = host.replace(/^\[/, '').replace(/\]$/, '');
  if (net.isIP(bare) === 4) {
    const out = Buffer.alloc(10);
    out[0] = 0x05; out[1] = 0x01; out[2] = 0x00; out[3] = 0x01;
    bare.split('.').forEach((octet, i) => { out[4 + i] = Number(octet); });
    out.writeUInt16BE(port, 8);
    return out;
  }
  if (net.isIP(bare) === 6) {
    const raw = ipv6Bytes(bare);
    if (raw) {
      const out = Buffer.alloc(22);
      out[0] = 0x05; out[1] = 0x01; out[2] = 0x00; out[3] = 0x04;
      raw.copy(out, 4);
      out.writeUInt16BE(port, 20);
      return out;
    }
  }
  const name = Buffer.from(bare, 'utf8');
  const out = Buffer.alloc(7 + name.length);
  out[0] = 0x05; out[1] = 0x01; out[2] = 0x00; out[3] = 0x03;
  out[4] = name.length;
  name.copy(out, 5);
  out.writeUInt16BE(port, 5 + name.length);
  return out;
}

// TCP may split the SOCKS greeting/reply arbitrarily, so both stages buffer
// until a complete message is present instead of indexing into one chunk.
function socksConnect(socksHost, socksPort, host, port, timeoutMs = 8000) {
  return new Promise((resolve, reject) => {
    const sock = net.connect(socksPort, socksHost);
    let stage = 0;
    let settled = false;
    let pending = Buffer.alloc(0);
    let timer = null;
    const fail = (e) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      sock.destroy();
      reject(e);
    };
    timer = setTimeout(() => fail(new Error('SOCKS handshake timeout')), timeoutMs);
    sock.setNoDelay(true);
    sock.on('error', fail);
    sock.on('close', () => { if (stage < 2) fail(new Error('SOCKS closed early')); });
    sock.on('connect', () => sock.write(Buffer.from([0x05, 0x01, 0x00])));
    sock.on('data', function onData(chunk) {
      if (settled) return;
      pending = Buffer.concat([pending, chunk]);
      if (stage === 0) {
        if (pending.length < 2) return;
        if (pending[0] !== 0x05 || pending[1] !== 0x00) return fail(new Error('SOCKS greeting rejected'));
        pending = pending.subarray(2);
        stage = 1;
        sock.write(buildConnectRequest(host, port));
      }
      if (stage === 1) {
        if (pending.length < 4) return;
        if (pending[0] !== 0x05) return fail(new Error('SOCKS: bad reply version'));
        const atyp = pending[3];
        let need;
        if (atyp === 0x01) need = 10;
        else if (atyp === 0x04) need = 22;
        else if (atyp === 0x03) {
          if (pending.length < 5) return;
          need = 5 + pending[4] + 2;
        } else {
          return fail(new Error(`SOCKS: bad ATYP 0x${atyp.toString(16)}`));
        }
        if (pending.length < need) return;
        if (pending[1] !== 0x00) return fail(new Error(`SOCKS connect failed (rep=${pending[1]})`));
        settled = true;
        stage = 2;
        clearTimeout(timer);
        sock.removeListener('data', onData);
        sock.removeAllListeners('close');
        const left = pending.subarray(need);
        if (left.length) sock.unshift(left);
        resolve(sock);
      }
    });
  });
}

// Each run owns its socket through `job`, so a finishing or cancelled run can
// only tear down itself -- never a replacement started in the meantime.
let speedJob = null; // { sock, finished, cancelled, finish }

function speedConfig(raw) {
  const cfg = raw && typeof raw === 'object' ? raw : {};
  const url = String(cfg.url || '');
  let target;
  try {
    target = new URL(url);
  } catch {
    return { error: 'invalid speed test URL' };
  }
  if (target.protocol !== 'http:' && target.protocol !== 'https:') {
    return { error: 'speed test URL must be http:// or https://' };
  }
  const socksPort = intIn(cfg.socksPort, 1, 65535);
  return {
    url,
    target,
    socksHost: typeof cfg.socksHost === 'string' && cfg.socksHost ? cfg.socksHost : '127.0.0.1',
    socksPort: socksPort === null ? 1080 : socksPort,
    seconds: Math.min(60, Math.max(3, intIn(cfg.seconds, 1, 600) ?? 10)),
  };
}

function startSpeedTest(raw) {
  stopSpeedTest();
  const cfg = speedConfig(raw);
  if (cfg.error) {
    send('kcp:speed', {
      done: true, ok: false, bytes: 0, seconds: 0, mbps: 0, mbPerSec: 0,
      error: cfg.error, url: String((raw && raw.url) || ''),
    });
    return;
  }
  const job = { sock: null, finished: false, cancelled: false, finish: null };
  speedJob = job;
  void runSpeedTest(cfg, job).catch((e) => {
    if (job.finish) job.finish(`internal: ${e.message}`);
  });
}

function stopSpeedTest() {
  const job = speedJob;
  speedJob = null;
  if (!job) return;
  job.cancelled = true;
  if (job.finish) job.finish('cancelled');
}

async function runSpeedTest(cfg, job) {
  const started = Date.now();
  const target = cfg.target;
  const host = target.hostname;
  const port = Number(target.port || (target.protocol === 'https:' ? 443 : 80));
  const tlsTarget = target.protocol === 'https:';
  let total = 0;
  let headerBytes = 0;
  let headBuf = null;
  let headDone = false;

  const finish = (message) => {
    if (job.finished) return;
    job.finished = true;
    const elapsed = Math.max(0.2, (Date.now() - started) / 1000);
    const body = Math.max(0, total - headerBytes);
    if (job.sock) {
      try { job.sock.destroy(); } catch { /* already closed */ }
      job.sock = null;
    }
    if (speedJob === job) speedJob = null;
    if (job.cancelled) return;
    send('kcp:speed', {
      done: true, ok: !message, bytes: body, seconds: elapsed,
      mbps: (body * 8) / elapsed / 1e6, mbPerSec: body / elapsed / 1048576,
      error: message || '', url: cfg.url,
    });
  };
  job.finish = finish;

  let sock;
  try {
    sock = await socksConnect(cfg.socksHost, cfg.socksPort, host, port);
  } catch (e) {
    finish(`SOCKS: ${e.message}`);
    return;
  }
  if (job.cancelled) {
    try { sock.destroy(); } catch { /* closed */ }
    return;
  }
  job.sock = sock;
  if (tlsTarget) {
    const bare = host.replace(/^\[/, '').replace(/\]$/, '');
    const tlsOpts = { socket: sock };
    if (net.isIP(bare)) tlsOpts.host = bare;
    else tlsOpts.servername = bare;
    sock = tls.connect(tlsOpts, () => sock.write(buildRequest(target, host)));
    job.sock = sock;
    sock.on('error', (e) => finish(`TLS: ${e.message}`));
  } else {
    sock.write(buildRequest(target, host));
    sock.on('error', (e) => finish(`HTTP: ${e.message}`));
  }
  sock.on('data', (b) => {
    total += b.length;
    if (!headDone) {
      headBuf = headBuf ? Buffer.concat([headBuf, b]) : b;
      const idx = headBuf.indexOf('\r\n\r\n');
      if (idx >= 0) {
        headerBytes = idx + 4;
        headDone = true;
        headBuf = null;
      } else if (headBuf.length > 32 << 10) {
        headDone = true;
        headBuf = null;
      }
    }
    if (Date.now() - started > cfg.seconds * 1000) finish('');
  });
  sock.on('close', () => finish(''));
  sock.on('end', () => finish(''));
  setTimeout(() => finish(''), cfg.seconds * 1000 + 250);
  send('kcp:speed', { done: false, url: cfg.url, seconds: cfg.seconds });
}

function buildRequest(target, host) {
  const pathAndQuery = `${target.pathname || '/'}${target.search || ''}`;
  return `GET ${pathAndQuery} HTTP/1.1\r\n` +
    `Host: ${host}\r\n` +
    'User-Agent: kcptun-desktop/0.0.1\r\n' +
    'Accept: */*\r\n' +
    'Accept-Encoding: identity\r\n' +
    'Connection: close\r\n\r\n';
}

// ---------------------------------------------------------------- tray

// The window's X button hides to the tray instead of quitting, so the tray has
// to be the one place that explains the app is still alive and offers a way out.
function trayMenu() {
  return Menu.buildFromTemplate([
    {
      label: childState.running ? `运行中 · ${childState.listen}` : '隧道未启动',
      enabled: false,
    },
    { type: 'separator' },
    { label: win && win.isVisible() ? '隐藏主界面' : '显示主界面', click: toggleWindow },
    { type: 'separator' },
    { label: '退出 KCPtun', click: () => { isQuitting = true; app.quit(); } },
  ]);
}

function refreshTray() {
  if (!tray) return;
  tray.setToolTip(childState.running ? `KCPtun · 运行中 ${childState.listen}` : 'KCPtun · 未启动');
  tray.setContextMenu(trayMenu());
}

function createTray() {
  if (tray) return;
  const icon = nativeImage.createFromPath(path.join(__dirname, 'assets', 'tray.png'));
  if (icon.isEmpty()) {
    console.error('[kcptun-desktop] tray icon missing: assets/tray.png');
  }
  try {
    tray = new Tray(icon);
  } catch (e) {
    // Some Linux sessions have no status-icon host; the window must still work,
    // and window-all-closed falls back to quitting when there is no tray.
    console.error('[kcptun-desktop] tray unavailable:', e.message);
    return;
  }
  tray.on('click', showWindow);
  tray.on('balloon-click', showWindow);
  refreshTray();
  console.log('[kcptun-desktop] tray ready');
}

function showWindow() {
  if (!win) {
    createWindow();
    return;
  }
  if (win.isMinimized()) win.restore();
  win.show();
  win.focus();
}

function hideWindow() {
  if (win && win.isVisible()) win.hide();
}

function toggleWindow() {
  if (win && win.isVisible()) hideWindow();
  else showWindow();
}

function hintTray() {
  if (trayHinted || process.platform !== 'win32' || !tray) return;
  trayHinted = true;
  tray.displayBalloon({
    title: 'KCPtun 仍在运行',
    content: '窗口已收起到托盘，点击托盘图标可重新打开。',
  });
}

// ---------------------------------------------------------------- window

function createWindow() {
  win = new BrowserWindow({
    width: 980, height: 700, minWidth: 820, minHeight: 600,
    backgroundColor: '#f4f7f5',
    title: 'KCPtun',
    icon: path.join(__dirname, 'assets', 'tray.png'),
    show: false,
    autoHideMenuBar: true,
    webPreferences: {
      preload: path.join(__dirname, 'preload.cjs'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: false,
    },
  });
  // The renderer is a local file; nothing in it should open windows or
  // navigate away, so both are refused outright.
  win.webContents.setWindowOpenHandler(() => ({ action: 'deny' }));
  win.webContents.on('will-navigate', (event) => event.preventDefault());
  win.loadFile(path.join(__dirname, '..', 'renderer', 'index.html'));
  win.once('ready-to-show', () => { win.show(); console.log('[kcptun-desktop] window ready-to-show'); });
  console.log('[kcptun-desktop] createWindow done, loading renderer');
  win.on('close', (e) => {
    // X hides to the tray; quitting goes through the tray menu so a running
    // tunnel is never killed by a stray click.
    if (isQuitting) return;
    e.preventDefault();
    win.hide();
    hintTray();
  });
  win.on('show', refreshTray);
  win.on('hide', refreshTray);
  win.on('closed', () => { win = null; refreshTray(); });
}

// ---------------------------------------------------------------- ipc

ipcMain.handle('kcp:settings:load', () => readSettings());
ipcMain.handle('kcp:settings:save', (_e, patch) => writeSettings(patch || {}));
ipcMain.handle('kcp:start', (_e, cfg) => startTunnel(cfg || {}));
ipcMain.handle('kcp:stop', () => stopTunnel());
ipcMain.handle('kcp:state', () => childState);
ipcMain.handle('kcp:speed:start', (_e, cfg) => {
  startSpeedTest(cfg || {});
  return { ok: true };
});
ipcMain.handle('kcp:speed:stop', () => {
  stopSpeedTest();
  return { ok: true };
});
ipcMain.handle('kcp:open-log-dir', async () => {
  const dir = path.join(app.getPath('userData'), 'log');
  fs.mkdirSync(dir, { recursive: true });
  await shell.openPath(dir);
  return dir;
});
ipcMain.handle('kcp:pick-url', async () => {
  const r = await dialog.showMessageBox(win, {
    type: 'question',
    buttons: ['取消', '使用默认测速地址'],
    message: '测速地址',
    detail: '测速会经本地 SOCKS 代理真实下载该 URL。',
  });
  return r.response === 1 ? 'https://downloads.claude.ai/claude-code-releases/2.1.319/win32-x64/claude.exe' : '';
});

app.on('second-instance', () => { showWindow(); });

const gotLock = app.requestSingleInstanceLock();
if (!gotLock) {
  app.quit();
} else {
  app.whenReady().then(() => {
    console.log('[kcptun-desktop] app ready');
    createWindow();
    createTray();
  });
  // With a tray icon the app outlives its window; on a desktop without a usable
  // tray (some Linux sessions) fall back to quitting so it cannot become
  // unreachable.
  app.on('window-all-closed', () => {
    if (tray) return;
    if (child) return;
    app.quit();
  });
  app.on('before-quit', () => { isQuitting = true; stopTunnel(); });
  app.on('will-quit', () => { if (tray) { tray.destroy(); tray = null; } });
}
