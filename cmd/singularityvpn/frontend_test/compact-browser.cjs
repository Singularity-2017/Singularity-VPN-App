const {chromium}=require('playwright');
const fs=require('node:fs/promises');
const path=require('node:path');
const assert=require('node:assert/strict');
const root=path.resolve(__dirname,'../frontend');
(async()=>{
  const browser=await chromium.launch({headless:true});
  try {
    const page=await browser.newPage({viewport:{width:440,height:560}});
    const errors=[], heights=[];
    let first_resize=true, available_height=1200;
    page.on('pageerror',e=>errors.push(e.message));
    await page.exposeFunction('fixture_resize',async height=>{
      if(first_resize) { first_resize=false; return 0; }
      const applied=Math.min(available_height,Math.ceil(height));
      heights.push({requested:height,applied});
      if(page.viewportSize().height!==applied) await page.setViewportSize({width:page.viewportSize().width,height:applied});
      return applied;
    });
    await page.route('http://vpn-compact.test/**',async route=>{
      const name=new URL(route.request().url()).pathname.slice(1)||'index.html';
      if(name.startsWith('wails/')) return route.fulfill({body:'',contentType:'text/javascript'});
      if(!/^[\w.-]+$/.test(name)) return route.abort();
      await route.fulfill({body:await fs.readFile(path.join(root,name)),contentType:name.endsWith('.css')?'text/css':name.endsWith('.js')?'text/javascript':'text/html'});
    });
    await page.addInitScript(()=>{
      window.fixture_name='Synthetic Player'; window.fixture_signed_in=true; window.fixture_events={};
      window.fixture_entries=Array.from({length:8},(_,i)=>({at:new Date(Date.UTC(2026,9,25,12,i)).toISOString(),level:'warn',message:'event',attrs:{kind:'connection_degraded',degraded_probes:'true',loss_percent:'25'}}));
      window.fixture_account={username:'Synthetic Player',plan:{id:'fast',name:'Gaming',subscription_active:false},credits:42};
      window.runtime={EventsOn:(key,fn)=>window.fixture_events[key]=fn};
      const methods={
        Status:async()=>({service_running:true,status:{signed_in:window.fixture_signed_in,username:window.fixture_name,state:'failed'}}),
        Strategy:async()=>({strategy:{}}),Tunnel_mode:async()=>({mode:'vpn'}),
        Gamer_default:async()=>true,Games:async()=>({games:[{id:'synthetic',name:'Synthetic Game',on:false}]}),
        Account:async()=>window.fixture_delay ? new Promise(resolve=>window.fixture_resolve=()=>resolve({account:{username:'Old user',plan:{id:'slow',name:'Old tariff'},credits:999}})) : {account:window.fixture_account},
        Journal:async()=>window.fixture_journal_fail ? {error:{code:'unavailable'}} : {entries:window.fixture_entries},
        Set_window_height:async h=>window.fixture_resize(h),
        Sign_out:async()=>{window.fixture_signed_in=false;return {};},
      };
      window.go={main:{App:new Proxy(methods,{get:(obj,key)=>obj[key]||(async()=>({}))})}};
    });
    await page.goto('http://vpn-compact.test/');
    await page.locator('#screen-main').waitFor({state:'visible'});
    await page.waitForFunction(()=>document.getElementById('account-name').textContent==='Synthetic Player');
    // A transient missing username must not clear cached account identity.
    await page.evaluate(()=>{window.fixture_name='';window.fixture_delay=true;window.fixture_events.changed();});
    await page.evaluate(()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve))));
    assert.equal(await page.locator('#account-name').textContent(),'Synthetic Player');
    assert.equal(await page.locator('#avatar-letter').textContent(),'S');
    await page.evaluate(()=>{ window.fixture_animations=[]; const animate=Element.prototype.animate; Element.prototype.animate=function(frames,options){ window.fixture_animations.push(options.duration); return animate.call(this,frames,options); }; });
    const initial=page.viewportSize().height;
    const header = await page.locator('.brand').boundingBox();
    const avatar = await page.locator('#menu-button').boundingBox();
    assert(Math.abs(header.y-avatar.y)<2, 'brand must stay on the first header row');
    await page.locator('#compact-journal').click();
    await page.locator('#journal').waitFor({state:'visible'});
    await page.waitForFunction(()=>document.querySelectorAll('#journal-list li').length===8);
    assert.equal(await page.locator('#game-cards').isVisible(),false);
    assert.equal(await page.locator('#gamer').getAttribute('aria-checked'),'true');
    const durations=await page.evaluate(()=>window.fixture_animations);
    assert(durations.includes(100), 'replacement must animate for 100ms');
    assert.equal(page.viewportSize().height, initial);
    await page.locator('#gamer').click();
    assert.equal(await page.locator('#journal').isVisible(),false);
    assert.equal(await page.locator('#game-cards').isVisible(),true);
    assert.equal(await page.locator('#gamer').getAttribute('aria-checked'),'true');
    await page.locator('#compact-journal').click();
    await page.locator('#journal-title').click();
    assert.equal(await page.locator('#game-cards').isVisible(),true);
    await page.locator('#compact-journal').click();
    await page.evaluate(()=>{window.fixture_entries=[];window.fixture_events.changed();});
    await page.waitForFunction(()=>document.getElementById('journal-note').textContent.includes('Записей пока нет'));
    await page.evaluate(()=>{window.fixture_journal_fail=true;window.fixture_events.changed();});
    await page.locator('#journal-retry').waitFor({state:'visible'});
    await page.evaluate(()=>{window.fixture_journal_fail=false;window.fixture_entries=[{at:'2026-10-25T12:30:00Z',level:'info',message:'service starting'}];});
    await page.locator('#journal-retry').click();
    await page.waitForFunction(()=>document.querySelectorAll('#journal-list li').length===1);
    // The monitor cap is the only reason for a scroll fallback.
    await page.evaluate(()=>{window.fixture_entries=Array.from({length:100},(_,i)=>({at:new Date(Date.UTC(2026,9,25,13,i)).toISOString(),level:'info',message:'service starting'}));window.fixture_events.changed();});
    await page.waitForFunction(()=>document.querySelectorAll('#journal-list li').length===100);
    assert(await page.locator('#journal-list').evaluate(el=>el.scrollHeight>el.clientHeight));
    await page.waitForTimeout(250);
    assert.equal(heights.length,0,'automatic window resizing must be removed');
    assert.equal(page.viewportSize().height,initial);
    await page.locator('#compact-journal').click();
    await page.locator('#menu-button').click();
    await page.locator('#sign-out').click();
    await page.locator('#confirm').waitFor({state:'visible'});
    assert.match(await page.locator('#confirm-text').textContent(), /пароль/);
    assert.equal(await page.evaluate(()=>window.fixture_signed_in),true);
    await page.locator('#confirm-no').click();
    assert.equal(await page.evaluate(()=>window.fixture_signed_in),true);
    await page.locator('#menu-button').click();
    await page.locator('#sign-out').click();
    await page.locator('#confirm-yes').click();
    await page.locator('#screen-signin').waitFor({state:'visible'});
    if(await page.evaluate(()=>!!window.fixture_resolve)) await page.evaluate(()=>window.fixture_resolve());
    assert.equal(await page.locator('#account-name').textContent(),'');
    assert.deepEqual(errors,[]);
    console.log('Compact journal animation, fixed window geometry, confirmed sign-out and stable account identity passed.');
  } finally {await browser.close();}
})().catch(e=>{console.error(e);process.exitCode=1;});
