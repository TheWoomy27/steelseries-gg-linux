// The gg-linux AppImage's first run: imports SteelSeries GG's Windows
// installer (downloaded from steelseries.com when the user asks, or one
// they have), sets up device access, and starts ggd at login and adds GG
// to the application menu if they want. Exits 0 when GG can be opened,
// 1 when the user closed the window first.
'use strict';

const { app, BrowserWindow, dialog, ipcMain, net } = require('electron');
const { execFile, spawn } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');

const appDir = process.env.APPDIR || path.resolve(__dirname, '../../../..');
const bin = path.join(appDir, 'usr/bin');
const share = path.join(appDir, 'usr/share/steelseries-gg');
const home = os.homedir();
const cache = path.join(process.env.XDG_CACHE_HOME || path.join(home, '.cache'), 'gg-linux');
const data = process.env.GG_LINUX_DATA ||
  path.join(process.env.XDG_DATA_HOME || path.join(home, '.local/share'), 'steelseries-gg');
// It redirects to the current SteelSeriesGG<version>Setup.exe, as setup.sh
// uses it.
const downloadURL = 'https://steelseries.com/gg/downloads/gg/latest/windows';
const rulesName = '70-steelseries-gg.rules';
const systemRules = path.join('/etc/udev/rules.d', rulesName);

app.setPath('userData', path.join(cache, 'setup-window'));

let win;
let done = false;

function send(channel, value) {
  if (win && !win.isDestroyed()) win.webContents.send(channel, value);
}

function imported() {
  return fs.existsSync(path.join(data, 'client/app/package.json'));
}

function rulesInstalled() {
  try {
    return fs.readFileSync(systemRules, 'utf8') === fs.readFileSync(path.join(share, rulesName), 'utf8');
  } catch {
    return false;
  }
}

function run(file, args) {
  return new Promise((resolve) => {
    execFile(file, args, (err, stdout, stderr) => resolve({ ok: !err, stdout, stderr }));
  });
}

function which(name) {
  return (process.env.PATH || '').split(':').some((d) => {
    try {
      fs.accessSync(path.join(d, name), fs.constants.X_OK);
      return true;
    } catch {
      return false;
    }
  });
}

ipcMain.handle('state', async () => {
  const groups = await run('id', ['-nG']);
  return {
    imported: imported(),
    rules: rulesInstalled(),
    pkexec: which('pkexec'),
    inputGroup: groups.ok && groups.stdout.split(/\s+/).includes('input'),
    user: os.userInfo().username,
  };
});

// download fetches GG's installer into ~/.cache/gg-linux, keeping only the
// newest, and returns its path.
ipcMain.handle('download', async () => {
  fs.mkdirSync(cache, { recursive: true });
  const abort = new AbortController();
  const res = await net.fetch(downloadURL, { signal: abort.signal });
  if (!res.ok) throw new Error(`steelseries.com answered ${res.status}`);
  const name = path.basename(new URL(res.url).pathname);
  if (!/^[\w.-]+\.exe$/i.test(name)) {
    abort.abort();
    throw new Error(`unexpected download ${res.url}`);
  }
  const target = path.join(cache, name);
  const size = Number(res.headers.get('content-length')) || 0;
  if (size && fs.existsSync(target) && fs.statSync(target).size === size) {
    abort.abort();
    send('progress', { text: `${name} is already downloaded.`, fraction: 1 });
    return target;
  }
  const part = target + '.part';
  const out = fs.createWriteStream(part);
  const reader = res.body.getReader();
  let got = 0;
  let shown = 0;
  try {
    for (;;) {
      const { done: end, value } = await reader.read();
      if (end) break;
      got += value.length;
      if (!out.write(value)) await new Promise((r) => out.once('drain', r));
      if (got - shown > 1 << 20 || got === size) {
        shown = got;
        send('progress', {
          text: `Downloading ${name}: ${Math.floor(got / 1048576)} of ${Math.floor(size / 1048576)} MB`,
          fraction: size ? got / size : 0,
        });
      }
    }
  } finally {
    await new Promise((r) => out.end(r));
  }
  if (size && got !== size) throw new Error('the download was cut short');
  fs.renameSync(part, target);
  for (const f of fs.readdirSync(cache)) {
    if (/^SteelSeriesGG.*Setup\.exe$/.test(f) && f !== name) fs.rmSync(path.join(cache, f), { force: true });
  }
  return target;
});

ipcMain.handle('choose', async () => {
  const r = await dialog.showOpenDialog(win, {
    title: 'SteelSeries GG installer',
    filters: [{ name: 'SteelSeries GG installer', extensions: ['exe'] }],
    properties: ['openFile'],
  });
  return r.canceled ? null : r.filePaths[0];
});

// import runs gg-setup on the installer, passing on what it prints.
ipcMain.handle('import', (_e, installer) => new Promise((resolve, reject) => {
  const fd = fs.openSync(installer, 'r');
  const magic = Buffer.alloc(2);
  fs.readSync(fd, magic, 0, 2, 0);
  fs.closeSync(fd);
  if (magic.toString() !== 'MZ') {
    reject(new Error(`${path.basename(installer)} is not a Windows installer`));
    return;
  }
  const child = spawn(path.join(bin, 'gg-setup'), [installer], { stdio: ['ignore', 'pipe', 'pipe'] });
  let last = '';
  const line = (chunk) => {
    for (const l of chunk.toString().split('\n')) {
      if (l.trim()) {
        last = l.trim();
        send('progress', { text: last });
      }
    }
  };
  child.stdout.on('data', line);
  child.stderr.on('data', line);
  child.on('error', reject);
  child.on('close', (code) => (code === 0 ? resolve() : reject(new Error(last || `gg-setup failed (${code})`))));
}));

// devices installs the udev rule with pkexec. The rule is copied out of
// the AppImage first: root can't read inside its mount.
ipcMain.handle('devices', async () => {
  fs.mkdirSync(cache, { recursive: true });
  const copy = path.join(cache, rulesName);
  fs.copyFileSync(path.join(share, rulesName), copy);
  const script = 'install -Dm644 "$1" "$2" && udevadm control --reload && udevadm trigger';
  const r = await run('pkexec', ['sh', '-c', script, 'sh', copy, systemRules]);
  return { ok: r.ok && rulesInstalled(), error: r.stderr.trim(), command: rulesCommand(copy) };
});

function rulesCommand(copy) {
  const q = (s) => `'${s.replace(/'/g, `'\\''`)}'`;
  return `sudo install -Dm644 ${q(copy)} ${systemRules} && sudo udevadm control --reload && sudo udevadm trigger`;
}

ipcMain.handle('rulesCommand', () => {
  fs.mkdirSync(cache, { recursive: true });
  const copy = path.join(cache, rulesName);
  fs.copyFileSync(path.join(share, rulesName), copy);
  return rulesCommand(copy);
});

ipcMain.handle('finish', async (_e, { service, menu }) => {
  const what = [];
  if (service) what.push('service');
  if (menu) what.push('menu');
  if (what.length) {
    const r = await run(path.join(appDir, 'AppRun'), ['--integrate', ...what]);
    if (!r.ok) throw new Error(r.stderr.trim() || 'could not set up the service or menu entry');
  }
  done = true;
  app.exit(0);
});

app.whenReady().then(() => {
  win = new BrowserWindow({
    width: 660,
    height: 800,
    minWidth: 520,
    minHeight: 600,
    title: 'Set up SteelSeries GG',
    autoHideMenuBar: true,
    backgroundColor: '#17191c',
    webPreferences: { preload: path.join(__dirname, 'preload.js'), contextIsolation: true, sandbox: true },
  });
  win.webContents.setWindowOpenHandler(() => ({ action: 'deny' }));
  win.webContents.on('will-navigate', (e) => e.preventDefault());
  win.loadFile(path.join(__dirname, 'index.html'));
});

app.on('window-all-closed', () => app.exit(done ? 0 : 1));
