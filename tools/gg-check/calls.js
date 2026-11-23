// node calls.js APPDIR: the API calls an imported client makes, one per
// line: "SERVER /path" for URLs it builds from a server address
// (httpServer is the Engine, ggHttpServer the GG core, ...; "url" when
// it is not named), and
// "call path" for the paths it passes to its fetch helpers. Path
// parameters read {}.
'use strict';
const fs = require('fs');
const path = require('path');

const out = new Set();
const param = (s) => s.replace(/\$\{[^}]*\}/g, '{}');
function scan(s) {
  // Server address helpers are named (httpServer...); others are minified
  // names that change between builds.
  for (const m of s.matchAll(/\$\{\(0,[\w$]+\.([\w$]+)\)\(\)\}(\/[^`"\s]{2,160})`/g)) {
    out.add(`${/Server$/.test(m[1]) ? m[1] : 'url'} ${param(m[2])}`);
  }
  for (const m of s.matchAll(/\.(?:fetch|submit|put|delete|post|get|patch|request)\(\s*[`"']([A-Za-z_/][^`"'\s]{2,120})[`"']/g)) {
    if (!m[1].includes('.')) out.add(`call ${param(m[1])}`);
  }
}
(function walk(dir) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, e.name);
    if (e.isDirectory()) {
      if (e.name !== 'node_modules') walk(p);
    } else if (e.name.endsWith('.js')) {
      scan(fs.readFileSync(p, 'utf8'));
    }
  }
})(process.argv[2]);
console.log([...out].sort().join('\n'));
