'use strict';

// "Launch Application" bindings on Linux. GG's application picker (the
// device window's key bindings) offers .exe and .bin files from Program
// Files, and drops a pick with any other extension. On Linux it offers
// the desktop's applications (.desktop files) and AppImages; a pick the
// page would drop is handed to it as a small launcher script without an
// extension (in <data>/launchers, named after the application), which ggd
// runs when the key is pressed.

const fs = require('fs');
const path = require('path');
const { dialog, BrowserWindow } = require('electron');
const env = require('./env');

const CHANNEL = 'DIALOG:SHOW_OPEN_DIALOG';
const ACCEPTED = new Set(['', '.exe', '.bin']);

// isAppPicker reports whether a dialog is GG's application picker in a
// device window (the QuickSet and Moments game pickers stay as they are).
function isAppPicker(event, opts) {
  const url = event?.sender?.getURL?.() || '';
  const apps = (opts?.filters || []).some((f) => f.name === 'Applications' && (f.extensions || []).includes('exe'));
  return apps && url.includes('deviceConfig.html');
}

function desktopName(file) {
  try {
    const text = fs.readFileSync(file, 'utf8');
    let inEntry = false;
    for (const line of text.split('\n')) {
      const l = line.trim();
      if (l.startsWith('[')) inEntry = l === '[Desktop Entry]';
      else if (inEntry && l.startsWith('Name=')) return l.slice(5).trim();
    }
  } catch {}
  return '';
}

const quote = (s) => `'${s.replace(/'/g, `'\\''`)}'`;

// launcherFor turns a pick the page would drop into a launcher script.
function launcherFor(file) {
  const ext = path.extname(file);
  if (ACCEPTED.has(ext.toLowerCase())) return file;
  let name = ext === '.desktop' ? desktopName(file) : '';
  if (!name) name = path.basename(file, ext);
  name = name.replace(/[^A-Za-z0-9 _+-]/g, '').trim() || 'Application';
  let cmd;
  if (ext === '.desktop') {
    cmd = `exec gio launch ${quote(file)}`;
  } else {
    let executable = false;
    try {
      fs.accessSync(file, fs.constants.X_OK);
      executable = true;
    } catch {}
    cmd = executable ? `cd ${quote(path.dirname(file))} && exec ${quote(file)}` : `exec xdg-open ${quote(file)}`;
  }
  const dir = path.join(env.dataDir, 'launchers');
  fs.mkdirSync(dir, { recursive: true });
  const out = path.join(dir, name);
  fs.writeFileSync(out, `#!/bin/sh\n# Launches ${file} for a SteelSeries GG binding.\n${cmd}\n`, { mode: 0o755 });
  fs.chmodSync(out, 0o755);
  return out;
}

function install(ipc) {
  ipc.wrap('handle', CHANNEL, (orig) => async (event, opts) => {
    if (!isAppPicker(event, opts)) return orig(event, opts);
    try {
      const options = {
        ...opts,
        defaultPath: fs.existsSync('/usr/share/applications') ? '/usr/share/applications' : undefined,
        filters: [
          { name: 'Applications', extensions: ['desktop', 'AppImage', 'appimage'] },
          { name: 'All Files', extensions: ['*'] },
        ],
      };
      const win = BrowserWindow.fromWebContents(event.sender);
      const { canceled, filePaths } = win ? await dialog.showOpenDialog(win, options) : await dialog.showOpenDialog(options);
      if (canceled || !filePaths.length) return { canceled: true, filePaths: [] };
      return { canceled: false, filePaths: [launcherFor(filePaths[0])] };
    } catch (err) {
      console.error('[gg-linux] application picker:', err);
      return { canceled: true, filePaths: [] };
    }
  });
}

module.exports = { install, launcherFor };
