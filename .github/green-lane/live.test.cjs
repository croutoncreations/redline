const {test} = require('node:test');
const assert = require('node:assert/strict');
const live = require('./live.cjs');
const main='a'.repeat(40), head='b'.repeat(40), base='c'.repeat(40), merged='d'.repeat(40);
const names=['actionlint','go','dashboard','linux-smoke','windows-smoke','macos','macos-intel'];
function fixture() {
  const f={state:'active',tip:main,prs:[1,2],merges:[],reads:0,enabledReads:0,dispatches:[],valid:true,behind:0,
    protection:{isAdminEnforced:true,requiresStrictStatusChecks:true,requiresStatusChecks:true,allowsForcePushes:false,
      allowsDeletions:false,requiresConversationResolution:true,requiredStatusChecks:names.map(context=>({context,app:{databaseId:15368}}))}};
  f.pr=n=>({number:n,state:'open',draft:false,mergeable:true,updated_at:'now',labels:[],head:{sha:head,repo:{full_name:'croutoncreations/redline'}},base:{sha:base,ref:'main'}});
  f.result=n=>({repository:'croutoncreations/redline',pr_number:n,head_sha:head,base_sha:base,base_branch:'main',pr_updated_at:'now',evaluated_at:new Date().toISOString(),verdict:'would_merge',rules:[{pass:true}]});
  f.github={paginate:async()=>f.prs.map(f.pr),graphql:async()=>({repository:{ref:{branchProtectionRule:f.protection}}}),rest:{
    actions:{createWorkflowDispatch:async args=>{f.dispatches.push(args);if(f.dispatchError)throw new Error('dispatch failed')},getWorkflow:async()=>{f.enabledReads++;return {data:{state:f.state}}}},git:{getRef:async()=>({data:{object:{sha:f.tip}}})},
    repos:{compareCommitsWithBasehead:async args=>{assert.ok(args.basehead.startsWith(`${main}...`));return {data:{behind_by:f.behind,status:'ahead',merge_base_commit:{sha:f.behind ? base : main}}}}},
    pulls:{get:async({pull_number:n})=>{f.reads++;return {data:f.pr(n)}},merge:async args=>{f.merges.push(args);if(f.mergeError)throw new Error('timeout');return {data:{merged:!f.rejected,sha:merged}}}}
  }};
  f.context={repo:{owner:'croutoncreations',repo:'redline'},sha:main,ref:'refs/heads/main',eventName:'schedule'};
  f.run=(_path,_args,opts)=>{assert.equal(opts.env.CONFIG_PATH,'');assert.equal(opts.env.EXPECTED_HEAD_SHA,head);return JSON.stringify(f.result(Number(opts.env.PR_NUMBER)))};
  return f;
}
async function run(f) {
  const before=process.env.LIVE_PILOT_ENABLED; process.env.LIVE_PILOT_ENABLED=f.optin ?? 'true';
  try { return await live({github:f.github,context:f.context,core:{info:()=>{}},run:f.run,validate:async args=>{if(f.onValidate)f.onValidate(args.result);return f.valid},write:r=>{f.report=structuredClone(r)}}); }
  finally {if(before===undefined)delete process.env.LIVE_PILOT_ENABLED;else process.env.LIVE_PILOT_ENABLED=before}
}
test('one exact-head squash merge even with multiple eligible PRs; old recorded base is not main tip',async()=>{
 const f=fixture();const r=await run(f);assert.equal(f.merges.length,1);assert.equal(f.merges[0].pull_number,1);assert.equal(f.merges[0].sha,head);assert.equal(f.merges[0].merge_method,'squash');assert.equal(r.state,'merged');assert.equal(r.main_tip,main);assert.equal(f.enabledReads,2);assert.deepEqual(f.dispatches,[{owner:'croutoncreations',repo:'redline',workflow_id:'ci.yml',ref:'main'}]);
});
for(const [name,edit] of Object.entries({
 expiredAfterValidation:f=>f.onValidate=r=>r.evaluated_at='2000-01-01T00:00:00Z',disabled:f=>f.optin='false',missingOptin:f=>f.optin='',killed:f=>f.state='disabled_manually',staleWorkflow:f=>f.tip=base,
 oldHead:f=>f.behind=1,blocked:f=>f.result=n=>({...fixture().result(n),verdict:'would_not_merge'}),
 inconclusive:f=>f.result=n=>({...fixture().result(n),rules:[{pass:true,inconclusive:true}]}),
 failedRule:f=>f.result=n=>({...fixture().result(n),rules:[{pass:false}]}),emptyRules:f=>f.result=n=>({...fixture().result(n),rules:[]}),
 staleReviewOrCI:f=>f.valid=false,hold:f=>{const p=f.pr;f.pr=n=>({...p(n),labels:[{name:'green-lane-hold'}]})},
 draft:f=>{const p=f.pr;f.pr=n=>({...p(n),draft:true})},foreignHead:f=>{const p=f.pr;f.pr=n=>({...p(n),head:{sha:head,repo:{full_name:'other/repo'}}})},
 killedDuringEvaluation:f=>f.onValidate=()=>f.state='disabled_manually',mainMovesDuringEvaluation:f=>f.onValidate=()=>f.tip=base,
 headMovesDuringEvaluation:f=>{f.prs=[1];f.onValidate=()=>{const p=f.pr;f.pr=n=>({...p(n),head:{sha:merged,repo:{full_name:'croutoncreations/redline'}}})}},
 threadActivityAfterSnapshot:f=>f.onValidate=()=>{const p=f.pr;f.pr=n=>({...p(n),updated_at:'later'})}
}))test(`no merge: ${name}`,async()=>{const f=fixture();edit(f);await run(f);assert.equal(f.merges.length,0)});
for(const [name,edit] of Object.entries({
 missingProtection:f=>f.protection=null,nonStrict:f=>f.protection.requiresStrictStatusChecks=false,adminBypass:f=>f.protection.isAdminEnforced=false,
 spoofedSource:f=>f.protection.requiredStatusChecks[0].app.databaseId=1,missingCI:f=>f.protection.requiredStatusChecks.pop(),
 wrongRepo:f=>f.context.repo.repo='other',untrustedRef:f=>f.context.ref='refs/pull/1/merge',reviewEvent:f=>f.context.eventName='pull_request_review',
 protectionChanges:f=>f.onValidate=()=>f.protection=null,variableReadFailure:f=>f.github.rest.actions.getWorkflow=async()=>{throw new Error('403')}
}))test(`fails closed: ${name}`,async()=>{const f=fixture();edit(f);await assert.rejects(run(f));assert.equal(f.merges.length,0)});
test('ambiguous merge failure never retries or tries next candidate',async()=>{const f=fixture();f.mergeError=true;await assert.rejects(run(f),/timeout/);assert.equal(f.merges.length,1);assert.equal(f.report.state,'merge_requested')});
test('rejected merge never advances to second candidate',async()=>{const f=fixture();f.rejected=true;await assert.rejects(run(f));assert.equal(f.merges.length,1)});

test('post-merge dispatch failure preserves confirmed merge and never retries merge',async()=>{const f=fixture();f.dispatchError=true;await assert.rejects(run(f),/dispatch failed/);assert.equal(f.merges.length,1);assert.equal(f.report.state,'merged');assert.equal(f.report.merged[0].commit,merged);assert.equal(f.dispatches.length,1)});
