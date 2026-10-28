const {test}=require('node:test');
const assert=require('node:assert/strict');
const {changes,entries}=require('../frontend/about.js');
// Right after a release nothing is unreleased yet: the first entry is then
// that release, and the window invents no entry for a build past it.
const unreleased=changes[0].version===undefined;
const newest_release=changes[unreleased?1:0];
test('the changelog is newest first, with only the unreleased version undated',()=>{
  assert.ok(changes.length>0);
  if(unreleased) assert.equal(changes[0].date,undefined);
  const released=changes.slice(unreleased?1:0);
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
  const e=entries('v9.9.9-dev.d8dacdf');
  if(!unreleased){
    assert.equal(e.length,changes.length);
    assert.equal(e.filter(x=>x.current).length,0);
    return;
  }
  assert.equal(e[0].title,'Версия 9.9.9-dev.d8dacdf');
  assert.equal(e[0].current,true);
  assert.equal(e.filter(x=>x.current).length,1);
});
test('a release build marks its release and shows nothing unreleased',()=>{
  const e=entries('v0.1.2');
  assert.equal(e.length,changes.length-(unreleased?1:0));
  assert.deepEqual(e.filter(x=>x.current).map(x=>x.title),['Версия 0.1.2']);
});
test('a build without a version names none and marks none',()=>{
  const e=entries('');
  if(unreleased) assert.equal(e[0].title,'Не выпущена');
  assert.equal(e.filter(x=>x.current).length,0);
});
test('only the nearest plan has a number, and it is ahead of every release',()=>{
  const {plans}=require('../frontend/about.js');
  assert.ok(plans.length>0);
  assert.match(plans[0].version,/^\d+\.\d+\.\d+$/);
  for(const p of plans.slice(1)) assert.equal(p.version,undefined);
  const n=s=>s.split('.').map(Number);
  const [a,b]=[n(plans[0].version),n(newest_release.version)];
  assert.ok(a[0]>b[0]||a[0]===b[0]&&(a[1]>b[1]||a[1]===b[1]&&a[2]>b[2]));
  for(const p of plans) for(const [kind,items] of p.groups) assert.ok(kind&&items.length>0,kind);
});
