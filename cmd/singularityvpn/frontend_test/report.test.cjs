const {test}=require('node:test');
const assert=require('node:assert/strict');
const {message,attached,plural,windows_name,failing_entry}=require('../frontend/report.js');
test('counts read in Russian',()=>{
  const f=n=>n+' '+plural(n,'сбой','сбоя','сбоев');
  assert.deepEqual([1,2,5,11,12,21,22,25,111].map(f),['1 сбой','2 сбоя','5 сбоев','11 сбоев','12 сбоев','21 сбой','22 сбоя','25 сбоев','111 сбоев']);
});
test('the message says how many failures of which kind, over how long, and where',()=>{
  const failures={count:5,first:'2026-10-27T10:00:00Z',last:'2026-10-27T10:23:00Z',blips:3,lost:1,failed:1};
  assert.equal(message(failures,'Сервер: Германия · S, AmneziaWG, Антизапрет'),
    'Связь сбоит: 5 сбоев за 23 минуты (3 коротких провала, 1 потеря связи, 1 неудачная проверка), сеть при этом не менялась. Сервер: Германия · S, AmneziaWG, Антизапрет.');
  assert.equal(message({count:3,first:'2026-10-27T10:00:00Z',last:'2026-10-27T10:00:10Z',blips:3},''),
    'Связь сбоит: 3 сбоя за 10 секунд (3 коротких провала), сеть при этом не менялась.');
});
test('no failures, no message',()=>{
  assert.equal(message(null,'x'),'');
  assert.equal(message({count:0},'x'),'');
});
test('the list of what goes is read from what is sent',()=>{
  const rows=attached({app_version:'0.1.4-dev.1',os:'10.0.26300',connection:{node_id:'n1',protocol:'amneziawg'},failures:{count:5},
    created_at:'2026-10-28T02:00:00+03:00',journal:[{at:'2026-10-28T00:01:00+03:00'},{at:'2026-10-28T01:00:00+03:00'}],
    network:[{name:'Wi-Fi'},{name:'ZeroTier One'}],apps:[{exe:'cs2.exe'},{exe:'Discord.exe'},{exe:'cs2.exe'}]},48213,'2026-10-28T01:20:00+03:00');
  assert.deepEqual(rows.map(r=>r[0]),['Соединение','Сбои','Журнал','Сеть','Программы на экране','Лог службы','Версия']);
  assert.equal(rows[2][1],'2 записи за 2 часа');
  assert.equal(rows[4][1],'cs2.exe, Discord.exe');
  assert.equal(rows[5][1],'47 КБ за 40 минут');
  assert.deepEqual(attached({},0),[]);
});
test('what goes is said over the time it really covers, not a fixed window',()=>{
  // After an earlier report only what came since goes, so the span is
  // measured from the first line, and is left out when it cannot be.
  const d={created_at:'2026-10-28T02:00:00+03:00',journal:[{at:'2026-10-28T01:59:30+03:00'}]};
  assert.equal(attached(d,0)[0][1],'1 запись за 1 минуту');
  assert.equal(attached({journal:[{}]},2048)[0][1],'1 запись');
  assert.equal(attached({},2048)[0][1],'2 КБ');
  assert.equal(attached({created_at:'2026-10-28T02:00:00+03:00'},2048,'2026-10-27T23:00:00+03:00')[0][1],'2 КБ за 3 часа');
});
test('Windows is named as it names itself',()=>{
  assert.equal(windows_name('10.0.26300'),'Windows 11, сборка 26300');
  assert.equal(windows_name('10.0.19045'),'Windows 10, сборка 19045');
  assert.equal(windows_name(''),'');
});
test('the failure blamed is the loss, not a blip on the connection made after it',()=>{
  const lost=at=>({at,attrs:{kind:'connection_lost',session_id:'s-old',node_id:'n-old',protocol:'wireguard'}});
  const blip={at:'2026-10-28T20:00:00Z',attrs:{kind:'connection_blip',session_id:'s-new',node_id:'n-new',protocol:'amneziawg'}};
  const all=()=>true;
  const entries=[lost('2026-10-28T19:08:00Z'),lost('2026-10-28T19:09:00Z'),blip];
  assert.equal(failing_entry(entries,0,all),entries[1]);
  assert.equal(failing_entry([blip],0,all),blip);
  assert.equal(failing_entry(entries,Date.parse('2026-10-28T19:30:00Z'),all),blip,'what was reported is not blamed again');
  assert.equal(failing_entry(entries,0,e=>e.attrs.kind!=='connection_lost'),blip,'only failures are blamed');
  const unnamed={at:'2026-10-28T20:01:00Z',attrs:{node_id:'n-x',protocol:'openvpn'}};
  assert.equal(failing_entry([blip,unnamed],0,all),blip,'one naming its session wins');
  assert.equal(failing_entry([{at:'2026-10-28T20:02:00Z',attrs:{kind:'connection_lost'}}],0,all),null,'one naming no server names no place');
});
