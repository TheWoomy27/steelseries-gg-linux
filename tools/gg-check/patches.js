// node patches.js APPDIR: whether each of gg-linux's client patches
// (client/linux/patches.js) matches an imported client. Exits 1 if any
// doesn't.
'use strict';
const fs = require('fs');
const path = require('path');

const app = process.argv[2];
const { PATCHES } = require(path.join(__dirname, '../../client/linux/patches.js'));
const files = ['main/index.js', 'main/preload.js',
  ...fs.readdirSync(path.join(app, 'render')).filter((f) => f.endsWith('.js')).map((f) => 'render/' + f)];
const src = {};
for (const f of files) {
  if (fs.existsSync(path.join(app, f))) src[f] = fs.readFileSync(path.join(app, f), 'utf8');
}
let missed = 0;
for (const p of PATCHES) {
  const where = Object.keys(src).filter((f) =>
    (p.file === 'render' ? f.startsWith('render/') && src[f].includes(p.marker) : p.file === f));
  const hit = where.filter((f) => {
    p.find.lastIndex = 0;
    return p.find.test(src[f]);
  });
  if (!hit.length) missed++;
  console.log(`${hit.length ? 'match' : 'NO MATCH'}  ${p.name} (${hit.join(', ') || where.join(', ') || 'no file with its marker'})`);
}
console.log(`${PATCHES.length - missed} of ${PATCHES.length} patches match`);
process.exit(missed ? 1 : 0);
