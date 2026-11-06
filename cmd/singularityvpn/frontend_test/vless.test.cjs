const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function fixture(api) {
  class Element {
    constructor() { this.children = []; this.attrs = {}; this.hidden = false; }
    append(...items) { this.children.push(...items); }
    setAttribute(k,v) { this.attrs[k] = v; }
    addEventListener(k,fn) { this[k] = fn; }
    replaceChildren(...items) { this.children = items; }
  }
  const context = { window: {}, document: { createElement: () => new Element() } };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname,'../frontend/vless.js'),'utf8'), context);
  const row = new Element();
  context.window.vless_ui.append(row, { id:'profile-1',label:'Test' }, api);
  return row;
}

test('VLESS discovers capabilities on demand and downloads only available nodes for that profile', async () => {
  const calls = [];
  const row = fixture({
    Vless_options: async id => { calls.push(['options',id]); return { nodes:[{node_id:'off',vless_available:false},{node_id:'on',vless_available:true}] }; },
    Download_vless: async (id,node) => { calls.push(['download',id,node]); return {}; }
  });
  assert.equal(calls.length,0);
  const disclosure = row.children[0];
  await disclosure.click();
  const panel = row.children[1];
  assert.equal(panel.children.length,1);
  await panel.children[0].click();
  assert.deepEqual(calls,[['options','profile-1'],['download','profile-1','on']]);
});

test('missing, false, malformed or failed capabilities never offer a download', async () => {
  for (const response of [{},{nodes:[{node_id:'n'}]},{nodes:[{node_id:'n',vless_available:'true'}]},{error:{code:'failed'}},null]) {
    let downloads=0;
    const row=fixture({Vless_options:async()=>response,Download_vless:async()=>downloads++});
    await row.children[0].click();
    assert.equal(row.children[1].children.filter(e=>e.click).length,0);
    assert.equal(downloads,0);
  }
});

test('capability rejection and download rejection release controls for retry', async () => {
  let fail=true;
  const row=fixture({Vless_options:async()=>{if(fail)throw Error('offline');return {nodes:[{node_id:'n',vless_available:true}]};},Download_vless:async()=>{throw Error('offline');}});
  await row.children[0].click();
  assert.equal(row.children[0].disabled,false);
  fail=false;
  await row.children[0].click();
  const download=row.children[1].children[0];
  await download.click();
  assert.equal(download.disabled,false);
});
