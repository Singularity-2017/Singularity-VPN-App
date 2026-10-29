const {chromium} = require('playwright');
const fs = require('node:fs/promises');
const path = require('node:path');
const assert = require('node:assert/strict');
const root = path.resolve(__dirname, '../frontend');
(async () => {
  const browser = await chromium.launch({headless:true});
  try {
    const page = await browser.newPage({viewport:{width:440,height:760}});
    const errors = [];
    page.on('pageerror', e=>errors.push(e.message));
    await page.route('http://vpn-ui.test/**', async route => {
      const name = new URL(route.request().url()).pathname.slice(1) || 'index.html';
      if (name.startsWith('wails/')) return route.fulfill({body:'',contentType:'text/javascript'});
      if (!/^[\w.-]+$/.test(name)) return route.abort();
      const body = await fs.readFile(path.join(root,name));
      await route.fulfill({body,contentType:name.endsWith('.css')?'text/css':name.endsWith('.js')?'text/javascript':'text/html'});
    });
    await page.addInitScript(() => {
      window.test_events = {};
      window.runtime = {EventsOn(name, action){window.test_events[name]=action;}, WindowSetSize(){}, WindowGetSize:async()=>({w:440,h:760})};
      window.signed_in = true;
      window.feedback_calls = [];
      const methods = {
        Status:async()=>({service_running:true,status:{signed_in:window.signed_in,username:'Synthetic Player',state:window.test_state || 'disconnected',...(window.test_health || {})}}),
        Strategy:async()=>({strategy:{}}), Tunnel_mode:async()=>({mode:'vpn'}),
        Games:async()=>({games:[{id:'synthetic',name:'Synthetic Game',on:false,regions:[{id:'eu',name:'Европа'},{id:'asia',name:'Азия'}]}]}), Gamer_default:async()=>false,
        Profiles:async()=>({profiles:[{id:'one',label:'Основной',current:window.test_profile!=='two'},{id:'two',label:'Второй',current:window.test_profile==='two'}]}),
        Set_profile:async id=>{window.test_profile=id;window.test_journal=[{at:'2026-10-25T12:30:00Z',message:'event',attrs:{kind:'profile_changed'}},{at:'2026-10-25T12:31:00Z',message:'event',attrs:{kind:'network_changed'}}];return {};}, Servers:async()=>({servers:[]}),
        Ping_servers:async()=>({pings:[]}), Version:async()=>({}),
        Analysis:async()=>window.test_analysis || ({}), Analyze:async()=>{window.test_analysis_calls=(window.test_analysis_calls||0)+1;return window.test_analysis_fail ? {error:{code:"unavailable"}} : (window.test_analysis || {});}, Journal:async()=>({entries:window.test_journal || []}),
        Spending_page:async()=>({spending:{status:'available',reason:'',totals:[{currency:'RUB',amount_minor:150000}],coverage:'all_recorded_paid_payments',transactions:[{payment_id:'synthetic-2',amount_minor:50000,currency:'RUB',occurred_at:'2026-09-20T12:00:00Z'}],next_cursor:''}}),
        Account:async()=>({account:{username:'Synthetic Player',plan:{id:'fast',name:'Тестовый тариф',target_kbps:50000,rate_kbps:50000,client_limit:2,subscription_active:false,sponsored_place:true,expires_at:'2027-01-01T00:00:00Z'},spending:{status:'available',reason:'',totals:[{currency:'RUB',amount_minor:150000}],coverage:'all_recorded_paid_payments',transactions:[{payment_id:'synthetic-1',amount_minor:100000,currency:'RUB',occurred_at:'2026-10-20T12:00:00Z'}],next_cursor:'synthetic-cursor'},credits:42,invite_tickets:3,referrer:{username:'Synthetic Sponsor'},payments:[{payment_amount_minor:12345,payment_currency:'RUB',payment_occurred_at:'2026-10-20T12:00:00Z'}],devices:[{name:'Synthetic Device',current:true,last_seen_at:'2026-10-25T12:00:00Z'}],referrals:[]}}),
        Sign_out:async()=>{window.signed_in=false;return {};},
        Feedback:async(params)=>{window.feedback_calls.push(params);return window.feedback_calls.length===1 ? {error:{code:'temporarily_unavailable'}} : {feedback:{accepted:true,code:'SYN-123'}};},
      };
      window.go = {main:{App:new Proxy(methods,{get:(obj,key)=> obj[key] || (async()=>({}))})}};
    });
    await page.goto('http://vpn-ui.test/');
    await page.locator('#screen-main').waitFor({state:'visible'});
    await page.locator('#account-plan').waitFor({state:'visible'});
    assert.equal(await page.locator('#power').evaluate(el=>getComputedStyle(el).borderTopLeftRadius),'50%');
    assert.match(await page.locator('#account-plan').textContent(),/Тестовый тариф/);
    assert.match(await page.locator('#account-plan').textContent(),/50.*Мбит/);
    assert.doesNotMatch(await page.locator('#account-plan').textContent(),/^Тариф[ ·:]/);
    const header = await page.evaluate(() => {
      const rect = id => { const r=document.getElementById(id).getBoundingClientRect(); return {x:r.x,y:r.y,w:r.width,h:r.height}; };
      return {brand:rect('brand-title'),credits:rect('header-credits'),account:rect('menu-button'),help:rect('help-button'),settings:rect('settings-button'),plan:rect('account-plan')};
    });
    assert(header.account.x < header.help.x && header.help.x < header.settings.x);
    assert(header.credits.x < header.account.x);
    assert(header.plan.y >= header.brand.y + header.brand.h);
    assert(header.plan.x < header.account.x);
    assert.equal(await page.locator('#account-plan').evaluate(el=>getComputedStyle(el).borderTopWidth),'2px');
    assert.equal(await page.locator('#header-credits').textContent(),'42');
    assert.equal(await page.locator('#connection-duration').isVisible(),false);
    assert.equal(await page.locator('#blocks-line').isVisible(),true);
    await page.evaluate(()=>{
      window.test_state='connected';
      window.test_health={since:new Date(Date.now()-3661000).toISOString(),statistics:{total_connected_seconds:90061,connections:3,reconnections:2,losses:1,switches:1,failed_connections:2,failed_reconnections:1,probe_samples:9,average_rtt_ms:51,average_loss_percent:2.5}};
      window.test_analysis={stale:true,result:{measured_at:new Date(Date.now()-3600000).toISOString(),checks:[{label:'Synthetic blocked service',verdict:'blocked',method:'tls'},{label:'Synthetic unknown service',verdict:'unknown'},{label:'Synthetic healthy service',verdict:'open'}]}};
      window.test_events.changed();
    });
    await page.waitForFunction(()=>document.getElementById('connection-duration').textContent.startsWith('01:01:'));
    const timerGeometry=await page.evaluate(()=>{
      const label=document.getElementById('power-label').getBoundingClientRect(),timer=document.getElementById('connection-duration').getBoundingClientRect();
      return {below:timer.top>=label.bottom,small:parseFloat(getComputedStyle(document.getElementById('connection-duration')).fontSize)<parseFloat(getComputedStyle(document.getElementById('power-label')).fontSize)};
    });
    assert.deepEqual(timerGeometry,{below:true,small:true});
    if (process.env.VPN_TIMER_SCREENSHOT) await page.screenshot({path:process.env.VPN_TIMER_SCREENSHOT});
    await page.waitForFunction(()=>document.getElementById('blocks-line').textContent.includes('прошлый замер'));
    await page.locator('#blocks-line').click();
    assert.match(await page.locator('#blocks-rows').textContent(),/Synthetic blocked service/);
    assert.match(await page.locator('#blocks-rows').textContent(),/не проверено/);
    assert.equal(await page.locator('#blocks-refresh').isDisabled(),true);
    await page.locator('#settings-button').click();
    await page.locator('#statistics-row').click();
    assert.match(await page.locator('#statistics-body').textContent(),/1 д. 01:01:01/);
    assert.match(await page.locator('#statistics-body').textContent(),/51 мс/);
    await page.keyboard.press('Escape');
    await page.evaluate(()=>{window.test_state='disconnected'; window.test_health={}; window.test_analysis_fail=true; window.test_events.changed();});
    await page.waitForFunction(()=>document.getElementById('connection-duration').hidden);
    await page.locator('#blocks-refresh').click();
    await page.waitForFunction(()=>document.getElementById('blocks-rows').textContent.includes('Не удалось обновить'));
    assert.match(await page.locator('#blocks-rows').textContent(),/Synthetic blocked service/);
    await page.evaluate(()=>{window.test_analysis={result:{measured_at:new Date().toISOString(),checks:[{label:'Synthetic unknown service',verdict:'unknown'}]}};window.test_events.changed();});
    await page.waitForFunction(()=>document.getElementById('blocks-line').textContent.includes('часть не проверена'));
    assert.doesNotMatch(await page.locator('#blocks-line').textContent(),/проверенные доступны/);
    await page.locator('#blocks-line').click();
    await page.evaluate(()=>{window.test_analysis_fail=false;});
    await page.locator('#account-plan').click();
    assert.equal(await page.locator('#account-panel').isVisible(),true);
    assert.match(await page.locator('#account-panel-body').textContent(),/Тестовый тариф/);
    assert.match(await page.locator('#account-panel-body').textContent(),/Бесплатное место/);
    assert.equal(await page.locator('#account-panel-body [data-expiry]').count(),0);
    const guarantee = page.locator('.account-detail').filter({hasText:'Гарантировано тарифом'}).locator('span').last();
    assert.equal(await guarantee.textContent(),'50');
    assert.equal(await guarantee.evaluate(el=>el.getClientRects().length),1);
    await page.keyboard.press('Escape');
    await page.locator('#menu-button').click();
    assert.match(await page.locator('#account-items').textContent(),/Кредиты42/);
    assert.equal(await page.locator('#account-device').count(),0);
    const rows=await page.locator('#account-items > button').allTextContents();
    assert.match(rows[1],/Профиль/);
    await page.locator('#account-items button').filter({hasText:'Устройства'}).click();
    assert.match(await page.locator('#account-panel-body').textContent(),/Это устройство/);
    await page.locator('#account-back').click();
    await page.locator('#profile-row').click();
    await page.locator('#profile-list button').filter({hasText:'Второй'}).click();
    await page.waitForFunction(()=>document.getElementById('menu').hidden);
    assert.equal(await page.evaluate(()=>window.test_profile),'two');
    await page.locator('#compact-journal').click();
    await page.waitForFunction(()=>document.getElementById('journal-list').textContent.includes('Профиль изменён'));
    assert.match(await page.locator('#journal-list').textContent(),/Сеть изменилась/);
    await page.locator('#compact-journal').click();
    await page.locator('#menu-button').click();
    await page.locator('#account-items button').filter({hasText:'Приглашения'}).click();
    assert.match(await page.locator('#account-panel-body').textContent(),/Synthetic Sponsor/);
    assert.doesNotMatch(await page.locator('#account-panel-body').textContent(),/object Object/);
    await page.locator('#account-back').click();
    assert.equal(await page.locator('#account-items button').filter({hasText:'Платежи'}).count(),0);
    await page.locator('#account-items button').filter({hasText:'Вклад в сеть'}).click();
    await page.locator('#account-panel-body button').first().click();
    assert.match(await page.locator('#account-panel-body').textContent(),/123,45 RUB/);
    await page.keyboard.press('Escape');
    await page.locator('#settings-button').click();
    await page.locator('#theme-row').click();
    await page.locator('#theme-edit').click();
    await page.locator('#theme-accent').fill('#12abcd');
    await page.locator('#theme-shape').selectOption('square');
    assert.equal(await page.locator('#power').evaluate(el=>getComputedStyle(el).borderTopLeftRadius),'0px');
    await page.locator('#theme-shape').selectOption('rounded-square');
    await page.locator('#theme-pattern').selectOption('dots');
    assert.equal(await page.locator('#theme-name').isVisible(),false);
    await page.locator('#theme-save').click();
    await page.locator('#theme-name').fill('Не сохранять');
    await page.keyboard.press('Escape');
    assert.equal(await page.locator('#theme-name').isVisible(),false);
    await page.locator('#theme-save').click();
    assert.equal(await page.locator('#theme-name').inputValue(),'');
    await page.locator('#theme-save-confirm').click();
    assert.equal(await page.locator('#theme-name').getAttribute('aria-invalid'),'true');
    await page.locator('#theme-name').fill('Моя тестовая');
    await page.locator('#theme-save-confirm').click();
    assert.equal(await page.locator('#theme-name').isVisible(),false);
    assert.match(await page.locator('#theme-save-status').textContent(),/сохранена/);
    assert.equal(await page.locator('html').getAttribute('data-shape'),'rounded-square');
    await page.reload();
    await page.locator('#screen-main').waitFor({state:'visible'});
    assert.equal(await page.locator('html').getAttribute('data-shape'),'rounded-square');
    assert.equal(await page.locator('html').evaluate(el=>getComputedStyle(el).getPropertyValue('--accent').trim()),'#12abcd');
    assert.equal(await page.locator('html').getAttribute('data-pattern'),'dots');
    await page.locator('#gamer').click();
    assert.equal(await page.locator('html').getAttribute('data-shape'),'rounded-square');
    assert.equal(await page.locator('html').getAttribute('data-theme'),'berserk');
    assert.equal(await page.locator('#power').evaluate(el=>getComputedStyle(el).borderTopLeftRadius),'36px');
    await page.locator('#settings-button').click();
    await page.locator('#theme-row').click();
    await page.getByRole('option',{name:/Токсин/}).click();
    assert.equal(await page.locator('html').getAttribute('data-pattern'),'molecules');
    await page.locator('#theme-edit').click();
    await page.locator('#theme-shape').selectOption('round');
    await page.keyboard.press('Escape');
    assert.equal(await page.locator('.game-line').first().isVisible(),false);
    assert.doesNotMatch(await page.locator('.game-cover').first().innerText(),/Играть с бустом/);
    if (process.env.VPN_ROUND_SCREENSHOT) await page.locator('.game-card').first().screenshot({path:process.env.VPN_ROUND_SCREENSHOT});
    if (process.env.VPN_UI_PREVIEW) {
      let preview=await page.content();
      preview=preview.replace(/<script[\s\S]*?<\/script>/g,'');
      for (const file of ['app.css','account.css','help.css','themes.css']) {
        preview=preview.replace(new RegExp('<link[^>]*href="'+file+'"[^>]*>'),'<style>'+await fs.readFile(path.join(root,file),'utf8')+'</style>');
      }
      const art='data:image/svg+xml;base64,'+(await fs.readFile(path.join(root,'covers/pubg.svg'))).toString('base64');
      preview=preview.replaceAll('covers/synthetic.svg',art).replaceAll('Synthetic Game','PUBG');
      preview=preview.replace('</body>', `<script>
        const $=id=>document.getElementById(id);
        for(const [button,panel] of [['menu-button','menu'],['help-button','help'],['settings-button','settings']]) $(button).onclick=()=>{ $(panel).hidden=!$(panel).hidden; if(panel==='menu'){$('menu-main').hidden=false;$('account-panel').hidden=true;$('profile-panel').hidden=true;} };
        $('header-credits').onclick=()=>{$('menu').hidden=!$('menu').hidden;};
        $('profile-row').onclick=()=>{$('menu-main').hidden=true;$('profile-panel').hidden=false;};
        $('profile-back').onclick=()=>{$('menu-main').hidden=false;$('profile-panel').hidden=true;};
        for(const b of document.querySelectorAll('.game-cover')) b.onclick=()=>{const card=b.closest('.game-card'),on=card.classList.toggle('on'),line=b.querySelector('.game-line');line.hidden=!on;line.textContent='Буст включён';b.querySelector('.game-act').textContent=on?'Выключить буст':'Играть';};
      <\/script></body>`);
      await fs.writeFile(process.env.VPN_UI_PREVIEW,preview);
    }

    const circle = await page.locator('.game-card').first().evaluate(el => {
      const r=el.querySelector('.game-face').getBoundingClientRect(), cx=r.x+r.width/2,cy=r.y+r.height/2;
      const outside=[];
      for(const node of el.querySelectorAll('.game-name,.game-line,.game-regions button')) for(const box of node.getClientRects()) {
        for(const [x,y] of [[box.left,box.top],[box.right,box.top],[box.left,box.bottom],[box.right,box.bottom]]) {
          if(((x-cx)/(r.width/2))**2+((y-cy)/(r.height/2))**2>1.02) outside.push(node.className || node.textContent);
        }
      }
      return {width:r.width,height:r.height,outside};
    });
    assert(Math.abs(circle.width-circle.height)<1,JSON.stringify(circle));
    assert.deepEqual(circle.outside,[]);
    const face=await page.locator('.game-face').first().boundingBox(), cover=await page.locator('.game-cover').first().boundingBox();
    assert(Math.abs(face.width-cover.width)<3 && Math.abs(face.height-cover.height)<3);
    await page.locator('.game-face').first().scrollIntoViewIfNeeded();
    assert(await page.locator('.game-face').first().evaluate(el=>{const r=el.getBoundingClientRect();return document.elementFromPoint(r.x+r.width/2,r.y+12).closest('.game-cover')===el.querySelector('.game-cover');}));
    for (const width of [360,800,440]) {
      await page.setViewportSize({width,height:900});
      await page.locator('.game-card').first().evaluate(el => {
        el.querySelector('.game-name').textContent='PLAYERUNKNOWN’S BATTLEGROUNDS';
        el.querySelector('.game-line').hidden=false;
        el.querySelector('.game-line').textContent='Буст включён · 42 мс · 5 с назад';
        el.querySelectorAll('.fixture-extra').forEach(node=>node.remove());
        const ask=document.createElement('div'); ask.className='game-ask fixture-extra';
        ask.textContent='Это сервер матча? Подтвердите, когда начнётся игра.';
        const buttons=document.createElement('span'); buttons.className='game-ask-buttons';
        for(const label of ['Да','Нет']) {const b=document.createElement('button'); b.className='plain'; b.textContent=label; buttons.append(b);}
        ask.append(buttons);
        const tally=document.createElement('div'); tally.className='game-tally fixture-extra'; tally.textContent='Сервер подтверждён в 8 из 10 матчей.';
        (el.querySelector('.game-card-content') || el).append(ask,tally);
      });
      await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
      const geometry=await page.locator('.game-card').first().evaluate(el=>{
        const r=el.querySelector('.game-face').getBoundingClientRect(),cx=r.x+r.width/2,cy=r.y+r.height/2;
        const outside=[];
        const clip=el.querySelector('.game-face').getBoundingClientRect();
        for(const node of el.querySelectorAll('.game-name,.game-line,.game-regions button')) for(const b of node.getClientRects()) {
          const left=Math.max(b.left,clip.left),right=Math.min(b.right,clip.right),top=Math.max(b.top,clip.top),bottom=Math.min(b.bottom,clip.bottom);
          if(left>=right || top>=bottom) continue;
          for(const [x,y] of [[left,top],[right,top],[left,bottom],[right,bottom]]) if(((x-cx)/(r.width/2))**2+((y-cy)/(r.height/2))**2>1.02) outside.push(node.className);
        }
        const content=el;
        return {width:r.width,height:r.height,outside, horizontalOverflow:content.scrollWidth > content.clientWidth+1};
      });
      assert(geometry.width<=237, 'circle must stay compact');
      assert(Math.abs(geometry.width-geometry.height)<1,JSON.stringify({viewport:width,...geometry}));
      assert.deepEqual(geometry.outside,[],JSON.stringify({viewport:width,...geometry}));
      assert.equal(geometry.horizontalOverflow,false,JSON.stringify({viewport:width,...geometry}));
    }
    await page.locator('#help-button').click();
    assert.match(await page.locator('#help button').first().textContent(),/^FAQ/);
    await page.locator('#help button').first().click();
    assert(await page.locator('#help details').count()>=12);
    await page.locator('#help button').first().click();
    await page.locator('#help button').filter({hasText:'Сообщить о баге'}).click();
    await page.locator('#feedback-message').fill('Synthetic bug report');
    await page.locator('#help [type="submit"]').click();
    assert.match(await page.locator('#feedback-status').textContent(),/Не удалось/);
    assert.equal(await page.locator('#feedback-message').inputValue(),'Synthetic bug report');
    await page.locator('#help [type="submit"]').click();
    // Sent, only the receipt stays: no field to type into, no button.
    assert.match(await page.locator('#help .feedback-status').textContent(),/Сообщение отправлено.*SYN-123/);
    assert.equal(await page.locator('#feedback-message').count(),0);
    assert.equal(await page.locator('#help [type="submit"]').count(),0);
    assert.deepEqual(await page.evaluate(()=>window.feedback_calls),[{kind:'problem',message:'Synthetic bug report'},{kind:'problem',message:'Synthetic bug report'}]);
    await page.keyboard.press('Escape');
    if (process.env.VPN_UI_SCREENSHOT) await page.screenshot({path:process.env.VPN_UI_SCREENSHOT});
    await page.locator('#gamer').click();
    assert.equal(await page.locator('html').getAttribute('data-theme'),'singularity');
    await page.setViewportSize({width:800,height:900});
    await page.evaluate(()=>{
      window.test_state='degraded';
      window.test_health={degraded_probes:true,loss_percent:25,rtt_ms:20};
      window.test_journal=[{at:'2026-10-25T12:00:00Z',level:'info',message:'event',attrs:{kind:'connection_degraded',degraded_probes:'true',degraded_latency:'false',loss_percent:'25'}}];
      window.test_events.changed();
    });
    await page.waitForFunction(()=>document.getElementById('trouble').textContent.includes('25%'));
    await page.locator('#journal').waitFor({state:'visible'});
    assert.match(await page.locator('#journal-list').textContent(),/25% контрольных TCP/);
    assert.doesNotMatch(await page.locator('#journal-list').textContent(),/или/);
    await page.evaluate(()=>{window.test_state='disconnected';window.test_health={};window.test_events.changed();});
    await page.evaluate(() => {
      window.go.main.App.Account = () => new Promise(resolve => {
        window.finish_account = () => resolve({account:{username:'Old account', plan:{id:'fast', name:'Old tariff'}, credits:999}});
      });
    });
    await page.locator('#menu-button').click();
    await page.locator('#sign-out').click();
    await page.locator('#confirm-yes').click();
    await page.locator('#screen-signin').waitFor({state:'visible'});
    assert.equal(await page.locator('#account-plan').isVisible(),false);
    assert.equal(await page.locator('#account-items').textContent(),'');
    await page.evaluate(() => window.finish_account());
    await page.waitForTimeout(50);
    assert.equal(await page.locator('#account-plan').isVisible(),false);
    assert.equal(await page.locator('#account-items').textContent(),'');
    assert.deepEqual(errors,[]);
    console.log('Synthetic browser flow passed: tariff, account, theme editing, reload, mode separation, sign-out.');
  } finally { await browser.close(); }
})().catch(e=>{console.error(e);process.exitCode=1;});
