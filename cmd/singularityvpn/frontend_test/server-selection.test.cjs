const { test, describe, before, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createStaticServer, launchBrowser } = require('./cdp.cjs');

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

describe('Server selection and switching interactions', () => {
  let serverInfo;
  let browser;

  before(async () => {
    serverInfo = await createStaticServer(root);
    browser = await launchBrowser({ width: 440, height: 660 });
    if (!browser) return;

    await browser.send('Page.addScriptToEvaluateOnNewDocument', {
      source: `
        window.page_errors = [];
        window.addEventListener('error', e => window.page_errors.push(e.message || String(e)));
        window.addEventListener('unhandledrejection', e => window.page_errors.push(e.reason ? (e.reason.message || String(e.reason)) : String(e)));
        window.strategy_calls = [];
        window.mock_strategy = { country: '', node_id: '', pinned: false };
        window.mock_status = {
          signed_in: true,
          username: 'Tester',
          state: 'connected',
          session_id: 'sess-1',
          node_id: 'de-01',
          node_name: 'Frankfurt 1',
          country: 'DE',
          city: 'Frankfurt',
          protocol: 'amneziawg',
          rtt_ms: 35
        };
        window.fixture_events = {};
        window.runtime = {
          EventsOn: (key, fn) => { window.fixture_events[key] = fn; },
          WindowSetSize: () => {},
          WindowGetSize: async () => ({ w: 440, h: 660 })
        };
        window.go = {
          main: {
            App: new Proxy({
              Status: async () => ({
                service_running: true,
                status: window.mock_status
              }),
              Strategy: async () => ({ strategy: window.mock_strategy }),
              Tunnel_mode: async () => ({ mode: 'vpn' }),
              Games: async () => ({ games: [] }),
              Gamer_default: async () => false,
              Profiles: async () => ({ profiles: [{ id: 'p1', label: 'Основной', current: true }] }),
              Vless_options: async (id) => ({ nodes: [{ node_id: 'de-01', vless_available: true }, { node_id: 'nl-01', vless_available: false }] }),
              Download_vless: async (id, node) => { window.vless_download = [id, node]; return { message: 'saved' }; },
              Servers: async () => ({
                servers: [
                  { node_id: 'de-01', name: 'Frankfurt 1', country: 'DE', city: 'Frankfurt', available: true },
                  { node_id: 'nl-01', name: 'Amsterdam 1', country: 'NL', city: 'Amsterdam', available: true }
                ]
              }),
              Ping_servers: async () => ({
                pings: [
                  { node_id: 'de-01', rtt_ms: 35 },
                  { node_id: 'nl-01', rtt_ms: 42 }
                ]
              }),
              Version: async () => ({ version: '0.1.5' }),
              Journal: async () => ({ entries: [] }),
              Analysis: async () => ({}),
              Set_strategy: async (s) => {
                window.strategy_calls.push(Object.assign({}, s));
                window.mock_strategy = Object.assign({}, s);
                return {};
              }
            }, { get: (obj, key) => obj[key] || (async () => ({})) })
          }
        };
      `
    });

    await browser.send('Page.navigate', {
      url: `http://127.0.0.1:${serverInfo.port}/index.html`
    });
    // Wait for initial render
    await new Promise(r => setTimeout(r, 500));
  });

  after(async () => {
    if (browser) await browser.close();
    if (serverInfo) await serverInfo.close();
  });

  test('profile menu creates and saves VLESS on a capable node without changing the tunnel', async (t) => {
    if (!browser) return t.skip('No browser');
    await browser.evaluate(`document.getElementById('menu-button').click(); document.getElementById('profile-row').click()`);
    await new Promise(r => setTimeout(r, 150));
    await browser.evaluate(`document.querySelector('#profile-list .vless-disclosure').click()`);
    await new Promise(r => setTimeout(r, 150));
    const result = await browser.evaluate(`(async () => {
      const buttons = document.querySelectorAll('#profile-list .vless-options button');
      if (buttons.length !== 1) throw new Error('Unavailable node offered for export');
      buttons[0].click();
      await new Promise(resolve => setTimeout(resolve, 0));
      return { download: window.vless_download, text: buttons[0].textContent, errors: window.page_errors, calls: window.strategy_calls.length };
    })()`);
    assert.deepEqual(result.download, ['p1', 'de-01']);
    assert.match(result.text, /Файл сохранён/);
    assert.deepEqual(result.errors, []);
    assert.equal(result.calls, 0);
    await browser.evaluate(`document.getElementById('menu-button').click()`);
  });

  test('selecting a server switches server with preferred by default', async (t) => {
    if (!browser) return t.skip('No browser');

    // Open server list
    await browser.evaluate(`document.getElementById('chip').click()`);
    await new Promise(r => setTimeout(r, 200));

    // Expand Netherlands group if collapsed and click Amsterdam server
    const clickResult = await browser.evaluate(`
      (() => {
        const list = document.getElementById('server-list');
        const groups = Array.from(list.querySelectorAll('button.group'));
        const nlGroup = groups.find(g => g.textContent.includes('Нидерланды'));
        if (nlGroup && nlGroup.getAttribute('aria-expanded') !== 'true') {
          nlGroup.click();
        }
        const opts = Array.from(list.querySelectorAll('button.opt'));
        const ams = opts.find(o => o.textContent.includes('Amsterdam') || o.textContent.includes('nl-01'));
        if (ams) {
          ams.click();
          return true;
        }
        return false;
      })()
    `);
    assert.ok(clickResult, 'Amsterdam server button must be found and clicked');
    await new Promise(r => setTimeout(r, 300));

    const state = await browser.evaluate(`
      ({
        calls: window.strategy_calls,
        errors: window.page_errors
      })
    `);

    assert.equal(state.errors.length, 0, 'No JavaScript errors must occur: ' + state.errors.join('; '));
    assert.ok(state.calls.length > 0, 'Set_strategy must be called');
    const lastCall = state.calls[state.calls.length - 1];
    assert.equal(lastCall.node_id, 'nl-01');
    assert.equal(lastCall.pinned, false, 'Default selection must be preferred (pinned = false)');
  });

  test('selecting a server with Ctrl sets pinned = true', async (t) => {
    if (!browser) return t.skip('No browser');

    // Open server list
    await browser.evaluate(`document.getElementById('chip').click()`);
    await new Promise(r => setTimeout(r, 200));

    const ctrlClickResult = await browser.evaluate(`
      (() => {
        const list = document.getElementById('server-list');
        const groups = Array.from(list.querySelectorAll('button.group'));
        const deGroup = groups.find(g => g.textContent.includes('Германия'));
        if (deGroup && deGroup.getAttribute('aria-expanded') !== 'true') {
          deGroup.click();
        }
        const opts = Array.from(list.querySelectorAll('button.opt'));
        const fra = opts.find(o => o.textContent.includes('Frankfurt') || o.textContent.includes('de-01'));
        if (fra) {
          fra.dispatchEvent(new MouseEvent('click', { bubbles: true, ctrlKey: true }));
          return true;
        }
        return false;
      })()
    `);
    assert.ok(ctrlClickResult, 'Frankfurt server button must be found and ctrl-clicked');
    await new Promise(r => setTimeout(r, 300));

    const state = await browser.evaluate(`
      ({
        calls: window.strategy_calls,
        errors: window.page_errors
      })
    `);

    assert.equal(state.errors.length, 0, 'No JavaScript errors must occur: ' + state.errors.join('; '));
    const lastCall = state.calls[state.calls.length - 1];
    assert.equal(lastCall.node_id, 'de-01');
    assert.equal(lastCall.pinned, true, 'Ctrl-click selection must be pinned (pinned = true)');
  });

  test('gear button toggles preferred vs pinned mode for selected server', async (t) => {
    if (!browser) return t.skip('No browser');

    // Open server policy popup
    await browser.evaluate(`document.getElementById('server-settings-button').click()`);
    await new Promise(r => setTimeout(r, 200));

    // Click preferred
    await browser.evaluate(`
      (() => {
        const pref = document.querySelector('#server-policy [data-server-policy="preferred"]');
        if (pref) pref.click();
      })()
    `);
    await new Promise(r => setTimeout(r, 300));

    const state = await browser.evaluate(`
      ({
        calls: window.strategy_calls,
        errors: window.page_errors
      })
    `);

    assert.equal(state.errors.length, 0, 'No JavaScript errors must occur: ' + state.errors.join('; '));
    const lastCall = state.calls[state.calls.length - 1];
    assert.equal(lastCall.pinned, false, 'Policy click must set pinned = false');
  });

  test('selecting auto country resets node_id and pinned', async (t) => {
    if (!browser) return t.skip('No browser');

    // Open server list
    await browser.evaluate(`document.getElementById('chip').click()`);
    await new Promise(r => setTimeout(r, 200));

    await browser.evaluate(`
      (() => {
        const autoCountry = Array.from(document.querySelectorAll('#server-list button.opt')).find(b => b.textContent.includes('Страна'));
        if (autoCountry) autoCountry.click();
      })()
    `);
    await new Promise(r => setTimeout(r, 300));

    const state = await browser.evaluate(`
      ({
        calls: window.strategy_calls,
        errors: window.page_errors
      })
    `);

    assert.equal(state.errors.length, 0, 'No JavaScript errors must occur: ' + state.errors.join('; '));
    const lastCall = state.calls[state.calls.length - 1];
    assert.equal(lastCall.node_id, '', 'Auto country must clear node_id');
    assert.equal(lastCall.pinned, false, 'Auto country must clear pinned');
  });
});
