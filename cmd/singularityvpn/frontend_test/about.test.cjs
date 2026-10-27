const {test}=require('node:test');
const assert=require('node:assert/strict');
const {changes,entries}=require('../frontend/about.js');
test('the changelog is newest first, with only the unreleased version undated',()=>{
  assert.ok(changes.length>0);
  assert.equal(changes[0].date,undefined);
  const released=changes.slice(1);
  for(const c of released){
    assert.match(c.version,/^\d+\.\d+\.\d+$/);
    assert.match(c.date,/^\d{4}-\d{2}-\d{2}$/);
  }
  const v=c=>c.version.split('.').map(Number);
  for(let i=1;i<released.length;i++){
    assert.ok(released[i-1].date>=released[i].date,released[i].version);
    const [a,b]=[v(released[i-1]),v(released[i])];
    assert.ok(a[0]>b[0]||a[0]===b[0]&&(a[1]>b[1]||a[1]===b[1]&&a[2]>b[2]),released[i].version);
  }
  for(const c of changes){
    assert.ok(c.groups.length>0,c.version);
    for(const [kind,items] of c.groups) assert.ok(kind&&items.length>0&&items.every(t=>typeof t==='string'&&t.trim()),c.version+' '+kind);
  }
});
test('a dev build is the unreleased entry, named by its own version',()=>{
  const e=entries('v0.1.3-dev.d8dacdf');
  assert.equal(e[0].title,'Версия 0.1.3-dev.d8dacdf');
  assert.equal(e[0].current,true);
  assert.equal(e.filter(x=>x.current).length,1);
});
test('a release build marks its release and shows nothing unreleased',()=>{
  const e=entries('v0.1.2');
  assert.equal(e.length,changes.length-1);
  assert.deepEqual(e.filter(x=>x.current).map(x=>x.title),['Версия 0.1.2']);
});
test('a build without a version names none and marks none',()=>{
  const e=entries('');
  assert.equal(e[0].title,'Не выпущена');
  assert.equal(e.filter(x=>x.current).length,0);
});
