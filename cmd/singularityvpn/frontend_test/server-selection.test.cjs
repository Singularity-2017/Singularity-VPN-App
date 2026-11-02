const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const root = path.resolve(__dirname, '..', 'frontend');
const html = fs.readFileSync(path.join(root, 'index.html'), 'utf8');
const about = fs.readFileSync(path.join(root, 'about.js'), 'utf8');

test('server settings gear button controls server-policy popup', () => {
  assert.match(html, /id="server-settings-button"[^>]*aria-haspopup="listbox"[^>]*aria-controls="server-policy"/);
  assert.match(html, /id="server-policy"[^>]*role="listbox"/);
  assert.match(html, /data-server-policy="preferred"[^>]*data-tip="[^"]+"[^>]*>[\s\S]*?Предпочитаемый/);
  assert.match(html, /data-server-policy="pinned"[^>]*data-tip="[^"]+"[^>]*>[\s\S]*?Закреплённый/);
});

test('about notes preferred and pinned server modes', () => {
  assert.match(about, /Выбранный сервер можно предпочесть или закрепить/);
});
