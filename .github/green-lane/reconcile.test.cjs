const {test}=require('node:test');
const assert=require('node:assert/strict');
const reconcile=require('./reconcile.cjs');

function fixture(count=2) {
 const prs=Array.from({length:count},(_,i)=>({number:i+1,head:{sha:`head-${i+1}`},base:{sha:'base'}}));
 const calls=[],published=[],checks=[],errors=[];
 return {calls,published,checks,errors,args:{github:{paginate:async()=>prs,rest:{pulls:{list:()=>{}},checks:{create:async c=>checks.push(c)}}},context:{repo:{owner:'owner',repo:'repo'}},core:{warning:()=>{},setFailed:e=>errors.push(e)},
 run:(_file,_args,options)=>{calls.push(options.env);return JSON.stringify({pr_number:Number(options.env.PR_NUMBER)})},publishReport:async args=>published.push(args.result.pr_number)}};
}

test('reconciliation evaluates only selected PRs with no repro or duplicate scan',async()=>{
 const f=fixture();await reconcile(f.args);
 assert.deepEqual(f.published,[1,2]);assert.equal(f.calls.length,2);
 for(const call of f.calls){assert.equal(call.REPRO_PASS,'');assert.equal(call.EXPECTED_BASE_SHA,'base')}
});

test('unsharded oversized sweep fails instead of omitting PRs',async()=>{
 const f=fixture(26);await assert.rejects(()=>reconcile(f.args),/shard/);
});

test('shards cover a large repository without truncation',async t=>{
 const old=process.env.RECONCILE_PR_NUMBERS;t.after(()=>{if(old===undefined)delete process.env.RECONCILE_PR_NUMBERS;else process.env.RECONCILE_PR_NUMBERS=old});
 const f=fixture(31);
 for(let i=1;i<=31;i+=5){process.env.RECONCILE_PR_NUMBERS=JSON.stringify(Array.from({length:Math.min(5,32-i)},(_,j)=>i+j));await reconcile(f.args)}
 assert.deepEqual(f.published,Array.from({length:31},(_,i)=>i+1));
 for(const value of ['[1,1]','[0]','["1"]','[1,2,3,4,5,6]','{}']){process.env.RECONCILE_PR_NUMBERS=value;await assert.rejects(()=>reconcile(f.args),/Invalid reconciliation shard/)}
 process.env.RECONCILE_PR_NUMBERS='[32]';await reconcile(f.args);assert.equal(f.published.length,31);
});

test('failed collection clears a stale report',async()=>{
 const f=fixture(1);f.args.run=()=>{throw new Error('API unavailable')};await reconcile(f.args);
 assert.equal(f.checks[0].conclusion,'neutral');assert.equal(f.errors.length,1);
});
