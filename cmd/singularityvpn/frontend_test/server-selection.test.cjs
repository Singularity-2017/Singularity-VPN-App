const { test, describe, before, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createStaticServer, launchBrowser } = require('./cdp.cjs');

const root = path.resolve(__dirname, '..', 'frontend');
const html = fs.readFileSync(path.join(root, 'index.html'), 'utf8');
const release_version = fs.readFileSync(path.resolve(root, '../../../VERSION'), 'utf8').trim();
const review_dir = process.env.VPN_UI_REVIEW_DIR;
async function review_screenshot(browser, name) {
  if (!review_dir) return;
  fs.mkdirSync(review_dir, { recursive: true });
  fs.writeFileSync(path.join(review_dir, name + '.png'), await browser.screenshot());
}

test('server settings gear button controls server-policy popup', () => {
  assert.match(html, /id="server-settings-button"[^>]*aria-haspopup="listbox"[^>]*aria-controls="server-policy"/);
  assert.match(html, /id="server-policy"[^>]*role="listbox"/);
  assert.match(html, /data-server-policy="preferred"[^>]*data-tip="[^"]+"[^>]*>[\s\S]*?Предпочитаемый/);
  assert.match(html, /data-server-policy="pinned"[^>]*data-tip="[^"]+"[^>]*>[\s\S]*?Закреплённый/);
});

test('main protocol menu offers native VLESS alongside existing protocols', () => {
  assert.match(html, /data-protocol="vless"/);
  assert.match(html, /data-protocol="wireguard"/);
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
        window.strategy_calls = []; window.connect_calls = 0; window.vless_capability = true;
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
              Connect: async () => { window.connect_calls++; return window.connect_error || {}; },
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
                  { node_id: 'de-01', name: 'Frankfurt 1', country: 'DE', city: 'Frankfurt', available: true, vless_available: window.vless_capability },
                  { node_id: 'nl-01', name: 'Amsterdam 1', country: 'NL', city: 'Amsterdam', available: true }
                ]
              }),
              Ping_servers: async () => ({
                pings: [
                  { node_id: 'de-01', rtt_ms: 35 },
                  { node_id: 'nl-01', rtt_ms: 42 }
                ]
              }),
              Version: async () => ({ version: ${JSON.stringify(release_version)} }),
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
  test('connected server uses catalogue name when session metadata is absent', async (t) => {
    if (!browser) return t.skip('No browser');
    await browser.evaluate(`window.mock_status.node_name=''; window.mock_status.country=''; window.mock_status.city=''; window.fixture_events.changed()`);
    await new Promise(r => setTimeout(r, 150));
    const chip = await browser.evaluate(`document.getElementById('chip').textContent`);
    assert.match(chip, /Германия/);
    assert.match(chip, /Frankfurt/);
    assert.doesNotMatch(chip, /de-01/);
    await browser.evaluate(`window.mock_status.node_name='Frankfurt 1'; window.mock_status.country='DE'; window.mock_status.city='Frankfurt'; window.fixture_events.changed()`);
  });
  test('protocol capability does not create a duplicate server badge', async (t) => {
    if (!browser) return t.skip('No browser');
    await browser.evaluate(`document.getElementById('chip').click()`);
    await new Promise(r => setTimeout(r, 150));
    assert.equal(await browser.evaluate(`document.querySelectorAll('#server-list .vless-capability').length`), 0);
    assert.doesNotMatch(await browser.evaluate(`document.getElementById('chip-sub').textContent`), /AmneziaWG|WireGuard|OpenVPN|VLESS/);
    assert.deepEqual(await browser.evaluate(`({label:document.querySelector('#protocol-chip .protocol-label').textContent,value:document.getElementById('protocol-value').textContent})`), {label:'Протокол · Авто',value:'AmneziaWG'});
    await review_screenshot(browser, 'vless-server-row');
    await browser.evaluate(`document.getElementById('chip').click()`);
  });

  test('a selected protocol remains visible if the old session is still active', async (t) => {
    if (!browser) return t.skip('No browser');
    await browser.evaluate(`window.mock_strategy.protocol='vless'; window.mock_status.protocol='openvpn'; window.fixture_events.changed()`);
    await new Promise(r => setTimeout(r, 150));
    const state = await browser.evaluate(`({label:document.querySelector('#protocol-chip .protocol-label').textContent,value:document.getElementById('protocol-value').textContent})`);
    assert.deepEqual(state, {label:'Сейчас · OpenVPN',value:'VLESS + REALITY'});
    await browser.evaluate(`window.mock_strategy.protocol=''; window.mock_status.protocol='amneziawg'; window.fixture_events.changed()`);
    await new Promise(r => setTimeout(r, 150));
  });
  test('VLESS availability follows selected server and country', async (t) => {
    if (!browser) return t.skip('No browser');
    const result = await browser.evaluate(`[
      servers_ui.vless_available({node_id:'de-01'}),
      servers_ui.vless_available({node_id:'nl-01'}),
      servers_ui.vless_available({country:'DE'}),
      servers_ui.vless_available({country:'NL'}),
      servers_ui.vless_available({node_id:'missing'})
    ]`);
    assert.deepEqual(result, [true, false, true, false, false]);
  });

  test('VLESS is capability scoped, filters servers and connects through native backend', async (t) => {
    if (!browser) return t.skip('No browser');
    await browser.evaluate(`document.getElementById('protocol-chip').click()`);
    await new Promise(r => setTimeout(r, 150));
    assert.equal(await browser.evaluate(`!!document.querySelector('[data-protocol="vless"]:not([hidden]):not(:disabled)')`), true);
    await review_screenshot(browser, 'vless-protocol-menu');
    await browser.evaluate(`document.querySelector('[data-protocol="vless"]').click()`);
    await new Promise(r => setTimeout(r, 200));
    assert.equal(await browser.evaluate(`window.mock_strategy.protocol`), 'vless');
    await browser.evaluate(`document.getElementById('chip').click()`);
    await new Promise(r => setTimeout(r, 200));
    assert.equal(await browser.evaluate(`document.getElementById('server-list').textContent.includes('Нидерланды')`), false);
    await browser.evaluate(`document.getElementById('chip').click(); window.mock_status.state='disconnected'; window.mock_status.session_id=''; window.fixture_events.changed()`);
    await new Promise(r => setTimeout(r, 150));
    await browser.evaluate(`document.getElementById('power').click()`);
    await new Promise(r => setTimeout(r, 150));
    assert.equal(await browser.evaluate(`window.connect_calls`), 1);
    assert.deepEqual(await browser.evaluate(`window.page_errors`), []);
  });

  test('missing or malformed capability disables saved VLESS without silently changing the preference', async (t) => {
    if (!browser) return t.skip('No browser');
    for (const capability of ['undefined', 'false', '"true"']) {
      await browser.evaluate(`window.vless_capability=${capability}; document.getElementById('protocol-chip').click()`);
      await new Promise(r => setTimeout(r, 100));
      assert.deepEqual(await browser.evaluate(`(() => {const b=document.querySelector('[data-protocol="vless"]'); return {disabled:b.disabled, selected:b.getAttribute('aria-selected'), label:document.getElementById('protocol-value').textContent};})()`), {disabled:true,selected:'true',label:'VLESS + REALITY'});
      await browser.evaluate(`document.getElementById('protocol-chip').click()`);
    }
    assert.equal(await browser.evaluate(`window.mock_strategy.protocol`), 'vless');
  });

  test('native VLESS failure is visible and leaves the connect button usable', async (t) => {
    if (!browser) return t.skip('No browser');
    await browser.evaluate(`window.connect_error={code:'protocol_unavailable'}; document.getElementById('power').click()`);
    await new Promise(r => setTimeout(r, 150));
    assert.equal(await browser.evaluate(`document.getElementById('action-error').hidden`), false);
    assert.equal(await browser.evaluate(`document.getElementById('power').disabled`), false);
    assert.equal(await browser.evaluate(`window.mock_strategy.protocol`), 'vless');
  });

  test('choosing VLESS while disconnected invokes native Connect immediately', async (t) => {
    if (!browser) return t.skip('No browser');
    await browser.evaluate(`window.connect_error=null; window.vless_capability=true; document.getElementById('protocol-chip').click()`);
    await new Promise(r => setTimeout(r, 100));
    await browser.evaluate(`document.querySelector('[data-protocol="wireguard"]').click()`);
    await new Promise(r => setTimeout(r, 150));
    const before = await browser.evaluate(`window.connect_calls`);
    await browser.evaluate(`document.getElementById('protocol-chip').click()`);
    await new Promise(r => setTimeout(r, 100));
    await browser.evaluate(`document.querySelector('[data-protocol="vless"]').click()`);
    await new Promise(r => setTimeout(r, 200));
    assert.equal(await browser.evaluate(`window.connect_calls`), before + 1);
    assert.equal(await browser.evaluate(`window.mock_strategy.protocol`), 'vless');
  });

  test('AntiZapret and Games require explicit full VPN transition; cancel sends no native action', async (t) => {
    if (!browser) return t.skip('No browser');
    for (const mode of ['antizapret', 'games']) {
      await browser.evaluate(`document.getElementById('protocol-chip').click()`);
      await new Promise(r => setTimeout(r, 100));
      await browser.evaluate(`document.querySelector('[data-protocol="wireguard"]').click()`);
      await new Promise(r => setTimeout(r, 150));
      await browser.evaluate(`window.mock_status.state='connected'; window.mock_status.session_id='live'; window.mock_status.tunnel='${mode}'; window.fixture_events.changed()`);
      await new Promise(r => setTimeout(r, 100));
      const before = await browser.evaluate(`({strategy:window.strategy_calls.length, connect:window.connect_calls})`);
      await browser.evaluate(`document.getElementById('protocol-chip').click()`);
      await new Promise(r => setTimeout(r, 100));
      await browser.evaluate(`document.querySelector('[data-protocol="vless"]').click()`);
      await new Promise(r => setTimeout(r, 50));
      assert.equal(await browser.evaluate(`document.getElementById('confirm').hidden`), false);
      assert.match(await browser.evaluate(`document.getElementById('confirm-text').textContent`), /весь компьютер/);
      await browser.evaluate(`document.getElementById('confirm-no').click()`);
      await new Promise(r => setTimeout(r, 50));
      assert.deepEqual(await browser.evaluate(`({strategy:window.strategy_calls.length, connect:window.connect_calls})`), before);
      await browser.evaluate(`document.querySelector('[data-protocol="vless"]').click()`);
      await new Promise(r => setTimeout(r, 50));
      await browser.evaluate(`document.getElementById('confirm-yes').click()`);
      await new Promise(r => setTimeout(r, 200));
      assert.equal(await browser.evaluate(`window.strategy_calls.length`), before.strategy + 1);
      assert.equal(await browser.evaluate(`window.strategy_calls.at(-1).protocol`), 'vless');
      assert.equal(await browser.evaluate(`window.connect_calls`), before.connect, 'Live handover uses Set_strategy once, without a second Connect');
      assert.deepEqual(await browser.evaluate(`({full:document.querySelector('[data-mode="vpn"]').getAttribute('aria-checked'),split_disabled:document.querySelector('[data-mode="antizapret"]').disabled})`), {full:'true',split_disabled:true});
    }
    assert.deepEqual(await browser.evaluate(`window.page_errors`), []);
  });

  test('current About release and its VLESS group are expanded and visible, plans last', async (t) => {
    if (!browser) return t.skip('No browser');
    await browser.evaluate(`document.getElementById('settings-button').click()`);
    await new Promise(r => setTimeout(r, 100));
    await browser.evaluate(`document.getElementById('about-row').click()`);
    await new Promise(r => setTimeout(r, 150));
    const result = await browser.evaluate(`(() => {
      const body=document.getElementById('about-body');
      const entry=Array.from(body.querySelectorAll('.about-version')).find(e=>e.querySelector('.about-current'));
      const group=entry && entry.querySelector('.about-group');
      const item=group && Array.from(group.querySelectorAll('li')).find(e=>e.textContent.includes('VLESS'));
      const rect=item && item.getBoundingClientRect();
      return {panel:!document.getElementById('about-panel').hidden, title:entry?.querySelector('.about-title').textContent, open:entry?.open,group_open:group?.open,text:item?.textContent,visible:!!rect && rect.height>0 && rect.top>=0 && rect.bottom<=window.innerHeight,plans_last:body.lastElementChild.classList.contains('about-plans'), errors:window.page_errors};
    })()`);
    assert.equal(result.panel, true);
    assert.equal(result.title, 'Версия ' + release_version);
    assert.equal(result.open, true);
    assert.equal(result.group_open, true);
    assert.match(result.text, /VLESS/);
    assert.equal(result.visible, true);
    assert.equal(result.plans_last, true);
    assert.deepEqual(result.errors, []);
    await review_screenshot(browser, 'about-current-vless');
  });

});
