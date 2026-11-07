'use strict';

const os = require('os');
const path = require('path');

const home = os.homedir();
const dataHome = process.env.XDG_DATA_HOME || path.join(home, '.local', 'share');
const dataDir = process.env.GG_LINUX_DATA || path.join(dataHome, 'steelseries-gg');

module.exports = {
  dataDir,
  configHome: process.env.XDG_CONFIG_HOME || path.join(home, '.config'),
  corePropsPath: path.join(dataDir, 'coreProps.json'),
  tokenPath: path.join(dataDir, 'state', 'client-token'),
  ggdBinary: process.env.GG_LINUX_GGD || 'ggd',
};
