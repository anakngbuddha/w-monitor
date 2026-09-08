// Isolated Node VM regressions, not a substitute for real browser tests.
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const elements = new Map();
function element(id) {
  if (!elements.has(id)) elements.set(id, {style:{}, value:'', textContent:'', focus(){}, replaceChildren(){}, addEventListener(){}});
  return elements.get(id);
}
const calls = [];
let handler = async () => ({ok:true, status:200, json:async()=>({client_name:'Fixture', tenant_id:'t_fixture'})});
const context = {
  URL, AbortController, Promise, console,
  window:{location:{href:'https://hub.example/?api_key=CANARY&read_token=CANARY2&range=24h'}},
  location:new URL('https://hub.example/'),
  history:{replaceState(a,b,url){this.url=url;}},
  localStorage:{removeItem(key){assert.equal(key,'wmonitor_api_key');}},
  document:{getElementById:element, querySelector:element, querySelectorAll(){return [];}},
  Option:function(){}, currentServer:'old', fetchData(){}, onServerChange(){}, exportCSV(){}, setRange(){},
  fetch:async(url,options={})=>{calls.push({url:String(url),...options}); return handler(url,options);}
};
for (const name of ['cpuChart','memChart','diskChart','netChart','iopsChart','usersChart']) context[name]={data:{labels:['old'],datasets:['old']},update(){}};
vm.createContext(context);
vm.runInContext(fs.readFileSync(path.join(__dirname,'../dashboard/static/session.js'),'utf8'),context);
(async()=>{
  assert.equal(context.history.url,'/?range=24h');
  element('apiKeyInput').value='EXACT_fixture_token';
  await context.submitAuthKey();
  assert.equal(JSON.parse(calls[0].body).read_token,'EXACT_fixture_token');
  assert.equal(calls[0].credentials,'same-origin');
  assert.equal(element('apiKeyInput').value,'');
  assert.equal(vm.runInContext('sessionReady',context),true);
  console.log('PASS: URL cleanup, exact token dispatch and input clearing');
  let release;
  handler=async(url,options)=>options.method==='POST'?new Promise(resolve=>{release=resolve;}):({ok:true,status:200});
  element('apiKeyInput').value='SECOND_fixture_token';
  const login=context.submitAuthKey();
  const logout=context.logoutApiKey();
  assert.equal(calls.at(-1).method,'POST');
  release({ok:true,status:200,json:async()=>({tenant_id:'t_new'})});
  await Promise.all([login,logout]);
  assert.equal(calls.at(-1).method,'DELETE');
  assert.equal(vm.runInContext('sessionReady',context),false);
  assert.equal(context.cpuChart.data.datasets.length,0);
  console.log('PASS: racing login/logout serializes cookie creation and revocation');
  let resolveOld;
  handler=()=>new Promise(resolve=>{resolveOld=resolve;});
  const old=context.authFetch('/api/metrics').catch(()=>null);
  vm.runInContext('requestGeneration++; sessionReady=true;',context);
  resolveOld({ok:false,status:401});
  await old;
  assert.equal(vm.runInContext('sessionReady',context),true);
  console.log('PASS: stale 401 cannot clear a newer session');
  const count=calls.length;
  context.location=new URL('http://untrusted.example/');
  element('apiKeyInput').value='NEVER_SENT';
  await context.submitAuthKey();
  assert.equal(calls.length,count);
  await assert.rejects(()=>context.authFetch('https://other.example/'));
  assert.equal(calls.length,count);
  console.log('PASS: insecure login and cross-origin requests send nothing');
})().catch(error=>{console.error(error);process.exitCode=1;});
