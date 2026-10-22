const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const os=require('node:os');
const path=require('node:path');
const reconcile=require('./reconcile.cjs');

test('reconciliation collects duplicates once and supplies no repro authority',async t=>{
 const temp=fs.mkdtempSync(path.join(os.tmpdir(),'green-lane-'));
 t.after(()=>fs.rmSync(temp,{recursive:true,force:true}));
 const old=process.env.RUNNER_TEMP;process.env.RUNNER_TEMP=temp;t.after(()=>{if(old===undefined)delete process.env.RUNNER_TEMP;else process.env.RUNNER_TEMP=old});
 const calls=[],published=[];
 const prs=[1,2].map(number=>({number,head:{sha:`head-${number}`},base:{sha:'base'}}));
 await reconcile({github:{paginate:async()=>prs,rest:{pulls:{list:()=>{}}}},context:{repo:{owner:'owner',repo:'repo'}},core:{warning:assert.fail,setFailed:assert.fail},
  run:(_file,_args,options)=>{calls.push(options.env);return options.env.LIST_OPEN_PRS==='true'?'{}':JSON.stringify({pr_number:Number(options.env.PR_NUMBER)})},
  publishReport:async args=>published.push(args.result.pr_number)
 });
 assert.equal(calls.filter(c=>c.LIST_OPEN_PRS==='true').length,1);
 assert.deepEqual(published,[1,2]);
 for(const call of calls.slice(1)){assert.equal(call.REPRO_PASS,'');assert.equal(call.REPRO_HEAD_SHA,'');assert.equal(call.REPRO_BASE_SHA,'');assert.equal(call.EXPECTED_BASE_SHA,'base');assert.ok(call.DUPLICATE_SNAPSHOT_PATH.startsWith(temp))}
});

test('oversized sweep fails instead of silently omitting PRs',async()=>{
 await assert.rejects(()=>reconcile({github:{paginate:async()=>Array(26).fill({}),rest:{pulls:{list:()=>{}}}},context:{repo:{owner:'owner',repo:'repo'}},core:{}}),/sharding/);
});
