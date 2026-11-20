'use strict';

const $ = (id) => document.getElementById(id);
const api = window.setup;

function status(id, text, kind = '') {
  const el = $(id);
  el.textContent = text;
  el.className = `status ${kind}`;
}

function ready() {
  $('devices').classList.remove('off');
  $('options').classList.remove('off');
  $('open').disabled = false;
}

function busy(on) {
  for (const id of ['download', 'choose']) $(id).disabled = on;
  $('bar').hidden = !on;
}

api.onProgress(({ text, fraction }) => {
  status('installer-status', text);
  if (fraction !== undefined) $('bar').value = fraction;
  else $('bar').removeAttribute('value');
});

async function importFrom(getInstaller) {
  busy(true);
  $('bar').value = 0;
  try {
    const installer = await getInstaller();
    if (!installer) return;
    status('installer-status', 'Unpacking GG\'s client, device specifications and assets. This takes a minute.');
    $('bar').removeAttribute('value');
    await api.import(installer);
    status('installer-status', 'SteelSeries GG is imported.', 'ok');
    ready();
  } catch (e) {
    status('installer-status', String(e.message || e).replace(/^Error invoking remote method '[^']*': (Error: )?/, ''), 'bad');
  } finally {
    busy(false);
  }
}

$('download').onclick = () => importFrom(api.download);
$('choose').onclick = () => importFrom(api.choose);

$('allow').onclick = async () => {
  $('allow').disabled = true;
  status('devices-status', 'Waiting for your password…');
  const r = await api.devices();
  if (r.ok) {
    status('devices-status', 'Done. Replug your devices if GG does not see them.', 'ok');
    $('allow').hidden = $('manual').hidden = true;
  } else {
    status('devices-status', r.error ? `Not installed: ${r.error}` : 'Not installed. You can run the command below in a terminal instead.', 'bad');
    $('command').textContent = r.command;
    $('command').hidden = false;
    $('allow').disabled = false;
  }
};

$('manual').onclick = async () => {
  $('command').textContent = await api.rulesCommand();
  $('command').hidden = false;
};

$('open').onclick = async () => {
  $('open').disabled = true;
  try {
    await api.finish({ service: $('service').checked, menu: $('menu').checked });
  } catch (e) {
    $('finish-status').textContent = String(e.message || e);
    $('open').disabled = false;
  }
};

api.state().then((s) => {
  if (s.imported) {
    $('installer-text').textContent = 'SteelSeries GG is imported. To update it, download the newest installer or use one you have.';
    ready();
  }
  if (s.rules) {
    status('devices-status', 'Device access is set up.', 'ok');
    $('allow').hidden = $('manual').hidden = true;
  } else if (!s.pkexec) {
    $('allow').hidden = true;
  }
  if (!s.inputGroup) {
    $('input-command').textContent = `sudo usermod -aG input ${s.user}`;
    $('input-note').hidden = false;
  }
});
