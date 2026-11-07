'use strict';

// Source patches for the client's webpack bundles, applied in memory at load
// time. Each patch is optional: if the pattern is not found (for example
// after a GG update) we log and keep going with the unpatched code.

const PATCHES = [
  {
    file: 'main/index.js',
    name: 'data directory',
    // getDataDir(): `${PROGRAMDATA}\SteelSeries\GG` on Windows.
    find: /:process\.env\.PROGRAMDATA\?`\$\{process\.env\.PROGRAMDATA\}\\\\SteelSeries\\\\GG`:`[^`]*`/,
    replace: ':process.env.GG_LINUX_DATA_DIR',
  },
  {
    file: 'main/index.js',
    name: 'files under the data directory',
    // e.g. `${process.env.PROGRAMDATA}\SteelSeries\GG\windowProps.json`
    find: /`\$\{process\.env\.PROGRAMDATA\}\\\\SteelSeries\\\\GG\\\\([A-Za-z0-9_.-]+)`/g,
    replace: '(process.env.GG_LINUX_DATA_DIR+"/$1")',
  },
];

function apply(src, file) {
  for (const p of PATCHES) {
    if (p.file !== file) continue;
    if (!p.find.test(src)) {
      console.warn(`[gg-linux] patch "${p.name}" did not match ${file}; continuing unpatched`);
      continue;
    }
    src = src.replace(p.find, p.replace);
  }
  return src;
}

module.exports = { apply, PATCHES };
