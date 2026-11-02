// Zero-dependency Chrome DevTools Protocol (CDP) client using native Node.js.
// Automates headless Chrome or Edge already installed on Windows.

const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const os = require('node:os');
const { spawn } = require('node:child_process');

function findBrowser() {
  const candidates = [
    'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
    'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
    'C:\\Program Files\\Microsoft\\Edge\\Application\\msedge.exe'
  ];
  for (const p of candidates) {
    if (fs.existsSync(p)) return p;
  }
  return null;
}

function createStaticServer(rootDir) {
  const mimeTypes = {
    '.html': 'text/html; charset=utf-8',
    '.css': 'text/css; charset=utf-8',
    '.js': 'application/javascript; charset=utf-8',
    '.json': 'application/json',
    '.svg': 'image/svg+xml',
    '.png': 'image/png'
  };

  const server = http.createServer((req, res) => {
    let cleanUrl = req.url.split('?')[0].replace(/^\/+/, '');
    if (!cleanUrl) cleanUrl = 'index.html';

    // Mock empty Wails bindings
    if (cleanUrl.startsWith('wails/')) {
      res.writeHead(200, { 'Content-Type': 'application/javascript' });
      res.end('');
      return;
    }

    const filePath = path.join(rootDir, cleanUrl);
    if (!fs.existsSync(filePath) || fs.statSync(filePath).isDirectory()) {
      res.writeHead(404, { 'Content-Type': 'text/plain' });
      res.end('Not found: ' + cleanUrl);
      return;
    }

    const ext = path.extname(filePath);
    res.writeHead(200, { 'Content-Type': mimeTypes[ext] || 'application/octet-stream' });
    res.end(fs.readFileSync(filePath));
  });

  return new Promise((resolve, reject) => {
    server.listen(0, '127.0.0.1', () => {
      resolve({
        server,
        port: server.address().port,
        close: () => new Promise(r => server.close(r))
      });
    });
    server.on('error', reject);
  });
}

async function launchBrowser({ width = 440, height = 660 } = {}) {
  const browserPath = findBrowser();
  if (!browserPath) return null;

  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'svpn-ui-test-'));
  const proc = spawn(browserPath, [
    '--headless',
    '--remote-debugging-port=0',
    '--disable-gpu',
    '--no-first-run',
    '--no-default-browser-check',
    '--user-data-dir=' + tmpDir
  ]);

  const cdpPort = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      proc.kill();
      reject(new Error('Timeout waiting for DevTools listening port'));
    }, 10000);

    proc.stderr.on('data', data => {
      const match = data.toString().match(/DevTools listening on ws:\/\/127\.0\.0\.1:(\d+)/);
      if (match) {
        clearTimeout(timer);
        resolve(parseInt(match[1], 10));
      }
    });

    proc.on('error', err => {
      clearTimeout(timer);
      reject(err);
    });
  });

  const list = await (await fetch('http://127.0.0.1:' + cdpPort + '/json')).json();
  const page = list.find(p => p.type === 'page');
  if (!page || !page.webSocketDebuggerUrl) {
    proc.kill();
    throw new Error('No page target found in browser');
  }

  const ws = new WebSocket(page.webSocketDebuggerUrl);
  let msgId = 1;

  const send = (method, params = {}) => new Promise((resolve, reject) => {
    const curId = msgId++;
    const handler = e => {
      try {
        const msg = JSON.parse(e.data);
        if (msg.id === curId) {
          ws.removeEventListener('message', handler);
          if (msg.error) reject(new Error(msg.error.message || JSON.stringify(msg.error)));
          else resolve(msg.result);
        }
      } catch (err) {
        reject(err);
      }
    };
    ws.addEventListener('message', handler);
    ws.send(JSON.stringify({ id: curId, method, params }));
  });

  await new Promise((resolve, reject) => {
    ws.addEventListener('open', resolve, { once: true });
    ws.addEventListener('error', reject, { once: true });
  });

  // Enable standard domains
  await send('Page.enable');
  await send('Runtime.enable');
  await send('DOM.enable');
  await send('Emulation.setDeviceMetricsOverride', {
    width,
    height,
    deviceScaleFactor: 1,
    mobile: false
  });

  return {
    proc,
    ws,
    cdpPort,
    send,
    evaluate: async expr => {
      const res = await send('Runtime.evaluate', {
        expression: expr,
        returnByValue: true,
        awaitPromise: true
      });
      if (res.exceptionDetails) {
        throw new Error(res.exceptionDetails.text || 'Runtime exception');
      }
      return res.result ? res.result.value : undefined;
    },
    screenshot: async () => {
      const res = await send('Page.captureScreenshot', { format: 'png' });
      return Buffer.from(res.data, 'base64');
    },
    close: async () => {
      try { ws.close(); } catch {}
      try { proc.kill(); } catch {}
      await new Promise(r => proc.on('exit', r));
      try { fs.rmSync(tmpDir, { recursive: true, force: true }); } catch {}
    }
  };
}

module.exports = {
  findBrowser,
  createStaticServer,
  launchBrowser
};
