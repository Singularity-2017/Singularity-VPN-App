// Visual layout, geometry, and UI interaction tests using headless Chrome/Edge.
// Runs under node:test with zero external dependencies.

const { test, describe, before, after } = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const fs = require('node:fs');
const { createStaticServer, launchBrowser } = require('./cdp.cjs');

const frontendDir = path.resolve(__dirname, '../frontend');
const goldenDir = path.join(__dirname, 'golden');

describe('Visual & UI Layout Tests (Headless CDP)', () => {
  let serverInfo;
  let browser;

  before(async () => {
    if (!fs.existsSync(goldenDir)) {
      fs.mkdirSync(goldenDir, { recursive: true });
    }

    serverInfo = await createStaticServer(frontendDir);
    browser = await launchBrowser({ width: 440, height: 660 });
    if (!browser) {
      console.warn('Skipping visual tests: no local Chrome/Edge binary detected.');
      return;
    }

    // Inject mock Wails bindings before navigation
    await browser.send('Page.addScriptToEvaluateOnNewDocument', {
      source: `
        window.page_errors = [];
        window.addEventListener('error', e => window.page_errors.push(e.message || String(e)));
        window.addEventListener('unhandledrejection', e => window.page_errors.push(e.reason ? (e.reason.message || String(e.reason)) : String(e)));
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
                status: {
                  signed_in: true,
                  username: 'Visual Tester',
                  state: 'disconnected',
                  node_name: 'Frankfurt 1',
                  country: 'DE',
                  city: 'Frankfurt',
                  rtt_ms: 45
                }
              }),
              Strategy: async () => ({ strategy: { pinned: false } }),
              Tunnel_mode: async () => ({ mode: 'vpn' }),
              Games: async () => ({ games: [{ id: 'test_game', name: 'Test Game', on: false }] }),
              Gamer_default: async () => false,
              Profiles: async () => ({ profiles: [{ id: 'p1', label: 'Основной', current: true }] }),
              Servers: async () => ({ servers: [{ id: 's1', name: 'Frankfurt 1', country: 'DE', city: 'Frankfurt', ping_ms: 45 }] }),
              Ping_servers: async () => ({ pings: [{ id: 's1', ping_ms: 45 }] }),
              Version: async () => ({ version: '0.1.5' }),
              Journal: async () => ({ entries: [] }),
              Analysis: async () => ({}),
              Account: async () => ({
                account: {
                  username: 'Visual Tester',
                  plan: { name: 'Тестовый тариф', target_kbps: 50000 },
                  credits: 42
                }
              })
            }, { get: (obj, key) => obj[key] || (async () => ({})) })
          }
        };
      `
    });

    await browser.send('Page.navigate', {
      url: `http://127.0.0.1:${serverInfo.port}/index.html`
    });

    // Wait for the app to settle
    await new Promise(r => setTimeout(r, 700));
  });

  after(async () => {
    if (browser) await browser.close();
    if (serverInfo) await serverInfo.close();
  });

  test('header element geometry and order', async (t) => {
    if (!browser) return t.skip('Browser not found');

    const header = await browser.evaluate(`
      (() => {
        const get = id => {
          const el = document.getElementById(id);
          if (!el) return null;
          const r = el.getBoundingClientRect();
          return { x: r.x, y: r.y, w: r.width, h: r.height };
        };
        return {
          brand: get('brand-title'),
          credits: get('header-credits'),
          account: get('menu-button'),
          help: get('help-button'),
          settings: get('settings-button')
        };
      })()
    `);

    assert.ok(header.brand && header.account && header.settings);
    // Credits -> Account -> Help -> Settings
    assert.ok(header.credits.x < header.account.x, 'Credits must be left of account');
    assert.ok(header.account.x < header.help.x, 'Account must be left of help');
    assert.ok(header.help.x < header.settings.x, 'Help must be left of settings');
  });

  test('power button is circular and properly centered', async (t) => {
    if (!browser) return t.skip('Browser not found');

    const power = await browser.evaluate(`
      (() => {
        const el = document.getElementById('power');
        const style = getComputedStyle(el);
        const rect = el.getBoundingClientRect();
        return {
          radius: style.borderTopLeftRadius,
          width: Math.round(rect.width),
          height: Math.round(rect.height)
        };
      })()
    `);

    assert.equal(power.radius, '50%', 'Power button must have 50% border radius');
    assert.ok(Math.abs(power.width - power.height) <= 2, 'Power button width and height must match');
  });

  test('server settings gear button opens server policy popup with options', async (t) => {
    if (!browser) return t.skip('Browser not found');

    const beforeClick = await browser.evaluate(`
      (() => {
        const popup = document.getElementById('server-policy');
        return popup ? popup.getAttribute('aria-hidden') : null;
      })()
    `);

    // Click the gear button
    await browser.evaluate(`document.getElementById('server-settings-button').click()`);
    await new Promise(r => setTimeout(r, 100));

    const afterClick = await browser.evaluate(`
      (() => {
        const popup = document.getElementById('server-policy');
        const preferred = popup.querySelector('[data-server-policy="preferred"]');
        const pinned = popup.querySelector('[data-server-policy="pinned"]');
        return {
          visible: !popup.hidden,
          hasPreferred: !!preferred,
          hasPinned: !!pinned
        };
      })()
    `);

    assert.ok(afterClick.visible, 'Server policy popup must be visible after click');
    assert.ok(afterClick.hasPreferred, 'Popup must have preferred option');
    assert.ok(afterClick.hasPinned, 'Popup must have pinned option');

    // Close with Escape
    await browser.evaluate(`
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    `);
  });

  test('no horizontal scrollbar on compact 440px viewport', async (t) => {
    if (!browser) return t.skip('Browser not found');

    const overflow = await browser.evaluate(`
      (() => ({
        scrollWidth: document.documentElement.scrollWidth,
        clientWidth: document.documentElement.clientWidth
      }))()
    `);

    assert.ok(overflow.scrollWidth <= overflow.clientWidth, 'Document must not have horizontal scrollbar');
  });

  test('visual screenshot comparison against golden baseline', async (t) => {
    if (!browser) return t.skip('Browser not found');

    const screenshot = await browser.screenshot();
    const goldenFile = path.join(goldenDir, 'main-screen.png');

    if (!fs.existsSync(goldenFile)) {
      // First run: save golden baseline
      fs.writeFileSync(goldenFile, screenshot);
      assert.ok(true, 'Saved initial golden snapshot baseline');
      return;
    }

    const goldenBase64 = fs.readFileSync(goldenFile).toString('base64');
    const currentBase64 = screenshot.toString('base64');

    // Perform in-browser Canvas pixel diffing
    const diff = await browser.evaluate(`
      (async () => {
        const load = b64 => new Promise(res => {
          const img = new Image();
          img.onload = () => res(img);
          img.src = 'data:image/png;base64,' + b64;
        });
        const [img1, img2] = await Promise.all([load('${goldenBase64}'), load('${currentBase64}')]);
        const canvas = document.createElement('canvas');
        canvas.width = img1.width;
        canvas.height = img1.height;
        const ctx = canvas.getContext('2d');

        ctx.drawImage(img1, 0, 0);
        const d1 = ctx.getImageData(0, 0, canvas.width, canvas.height).data;
        ctx.clearRect(0, 0, canvas.width, canvas.height);
        ctx.drawImage(img2, 0, 0);
        const d2 = ctx.getImageData(0, 0, canvas.width, canvas.height).data;

        let diffCount = 0;
        for (let i = 0; i < d1.length; i += 4) {
          const delta = Math.abs(d1[i] - d2[i]) + Math.abs(d1[i+1] - d2[i+1]) + Math.abs(d1[i+2] - d2[i+2]);
          if (delta > 25) diffCount++;
        }
        return {
          totalPixels: img1.width * img1.height,
          diffPixels: diffCount,
          percent: (diffCount / (img1.width * img1.height)) * 100
        };
      })()
    `);

    assert.ok(diff.percent < 1.0, `Visual mismatch too high: ${diff.percent.toFixed(2)}% pixels changed`);
  });

  test('power button and menu interactions execute without runtime exceptions', async (t) => {
    if (!browser) return t.skip('Browser not found');

    const result = await browser.evaluate(`
      (async () => {
        const calls = [];
        const originalConnect = window.go.main.App.Connect;
        window.go.main.App.Connect = async () => {
          calls.push('Connect');
          return {};
        };

        // Click power button
        document.getElementById('power').click();
        await new Promise(r => setTimeout(r, 100));

        // Click server chip
        document.getElementById('chip').click();
        await new Promise(r => setTimeout(r, 100));
        const serversOpen = !document.getElementById('servers').hidden;

        // Press Escape to close servers
        document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
        await new Promise(r => setTimeout(r, 50));

        // Click settings button
        document.getElementById('settings-button').click();
        await new Promise(r => setTimeout(r, 100));
        const settingsOpen = !document.getElementById('settings').hidden;

        // Press Escape to close settings
        document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
        await new Promise(r => setTimeout(r, 50));

        return {
          calls,
          serversOpen,
          settingsOpen,
          errors: window.page_errors || []
        };
      })()
    `);

    assert.deepEqual(result.errors, [], `Runtime errors detected during interaction: ${JSON.stringify(result.errors)}`);
    assert.ok(result.calls.includes('Connect'), 'Power click must invoke backend Connect');
    assert.ok(result.serversOpen, 'Clicking chip must open server popup');
    assert.ok(result.settingsOpen, 'Clicking settings must open settings popup');
  });
});
