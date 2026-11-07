'use strict';

// Locates (and if needed starts) ggd, the Linux GG core + engine service.

const fs = require('fs');
const https = require('https');
const { spawn } = require('child_process');
const env = require('./env');

function readProps() {
  try {
    const props = JSON.parse(fs.readFileSync(env.corePropsPath, 'utf8'));
    const token = fs.readFileSync(env.tokenPath, 'utf8').trim();
    if (!props.ggEncryptedAddress || !token) return null;
    return { ...props, token };
  } catch {
    return null;
  }
}

function pidAlive(pid) {
  try {
    process.kill(pid, 0);
    return true;
  } catch (e) {
    return e.code === 'EPERM';
  }
}

function isRunningSync() {
  const props = readProps();
  return !!(props && props.pid && pidAlive(props.pid));
}

function healthCheck(props) {
  return new Promise((resolve) => {
    const [host, port] = props.ggEncryptedAddress.split(':');
    const req = https.request(
      { host, port, path: '/healthCheck', method: 'GET', rejectUnauthorized: false, timeout: 1500,
        headers: { Authorization: `Bearer ${props.token}` } },
      (res) => { res.resume(); resolve(res.statusCode === 200); },
    );
    req.on('error', () => resolve(false));
    req.on('timeout', () => { req.destroy(); resolve(false); });
    req.end();
  });
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function ensureRunning() {
  let props = readProps();
  if (props && props.pid && pidAlive(props.pid) && (await healthCheck(props))) return props;

  const child = spawn(env.ggdBinary, [], { detached: true, stdio: 'ignore' });
  child.on('error', () => {});
  child.unref();

  for (let i = 0; i < 100; i++) {
    await sleep(100);
    props = readProps();
    if (props && props.pid && pidAlive(props.pid) && (await healthCheck(props))) return props;
  }
  throw new Error(`ggd did not come up (looked for ${env.corePropsPath}). Is it installed and on PATH?`);
}

module.exports = { ensureRunning, isRunningSync, readProps };
