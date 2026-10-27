const {test}=require('node:test');
const assert=require('node:assert/strict');
const {changes}=require('../frontend/about.js');
test('the changelog is newest first, with only the unreleased version undated',()=>{
  assert.ok(changes.length>0);
  assert.equal(changes[0].date,undefined);
  const released=changes.slice(1);
  for(const c of released){
    assert.match(c.version,/^\d+\.\d+\.\d+$/);
    assert.match(c.date,/^\d{4}-\d{2}-\d{2}$/);
  }
  for(let i=1;i<released.length;i++){
    assert.ok(released[i-1].date>=released[i].date,released[i].version);
    const v=s=>s.version.split('.').map(Number);
    const [a,b]=[v(released[i-1]),v(released[i])];
    assert.ok(a[0]>b[0]||a[0]===b[0]&&(a[1]>b[1]||a[1]===b[1]&&a[2]>b[2]),released[i].version);
  }
  for(const c of changes) assert.ok(c.items.length>0&&c.items.every(t=>typeof t==='string'&&t.trim()),c.version);
});
