// Linux entry point for the SteelSeries GG Electron client.
//
// gg-setup unpacks the client from the user's installer into
// <data>/client/app and points its package.json here. This file prepares the
// environment the Windows client expects (coreProps, auth token, a running
// GG core), smooths over the handful of Windows-only calls in the main
// process bundle, and then hands control to the original main/index.js.
'use strict';

const fs = require('fs');
const path = require('path');
const Module = require('module');
const childProcess = require('child_process');
const { app } = require('electron');

const env = require('./env');
const core = require('./core');
const patches = require('./patches');

app.setName('SteelSeries GG');
// Keep Chromium state separate from any other Electron app.
app.setPath('userData', path.join(env.configHome, 'SteelSeries GG'));
app.commandLine.appendSwitch('class', 'steelseries-gg');
if (process.env.XDG_SESSION_TYPE === 'wayland' && !process.env.GG_LINUX_X11) {
  app.commandLine.appendSwitch('ozone-platform-hint', 'auto');
  app.commandLine.appendSwitch('enable-features', 'WaylandWindowDecorations');
}

// The Windows client asks `tasklist` whether SteelSeriesGG.exe and
// SteelSeriesEngine.exe are running. On Linux both live inside ggd.
const origExec = childProcess.exec;
childProcess.exec = function exec(command, options, callback) {
  if (typeof options === 'function') {
    callback = options;
    options = undefined;
  }
  const m = /^tasklist \/fi "IMAGENAME eq ([^"]+)"/i.exec(String(command));
  if (m) {
    const running = core.isRunningSync();
    const stdout = running ? `${m[1]}    ${process.pid} Console    1    1,000 K\r\n` : 'INFO: No tasks are running which match the specified criteria.\r\n';
    process.nextTick(() => callback && callback(null, stdout, ''));
    return { on() {}, kill() {} };
  }
  return origExec.call(this, command, options, callback);
};
// util.promisify(exec) resolves to { stdout, stderr } via this hook.
childProcess.exec[require('util').promisify.custom] = (command, options) =>
  new Promise((resolve, reject) => {
    childProcess.exec(command, options, (err, stdout, stderr) => {
      if (err) {
        err.stdout = stdout;
        err.stderr = stderr;
        reject(err);
      } else {
        resolve({ stdout, stderr });
      }
    });
  });

function loadClientMain() {
  const appDir = app.getAppPath();
  const mainPath = path.join(appDir, 'main', 'index.js');
  let src = fs.readFileSync(mainPath, 'utf8');
  src = patches.apply(src, 'main/index.js');
  const m = new Module(mainPath, module);
  m.filename = mainPath;
  m.paths = Module._nodeModulePaths(path.dirname(mainPath));
  require.cache[mainPath] = m;
  m._compile(src, mainPath);
}

async function main() {
  const props = await core.ensureRunning();
  process.env.CORE_PROPS = env.corePropsPath;
  process.env.GG_API_AUTH_TOKEN = props.token;
  // Used by the patched getDataDir()/getAppsDir() in the main bundle.
  process.env.GG_LINUX_DATA_DIR = env.dataDir;
  process.env.PROGRAMW6432 = env.dataDir;

  require('./ipc').install();
  loadClientMain();
}

main().catch((err) => {
  console.error('[gg-linux] failed to start:', err);
  const { dialog } = require('electron');
  app.whenReady().then(() => {
    dialog.showErrorBox('SteelSeries GG', `Could not start the GG service (ggd).\n\n${err.message}`);
    app.exit(1);
  });
});
