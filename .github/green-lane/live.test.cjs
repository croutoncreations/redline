const {test} = require('node:test');
const assert = require('node:assert/strict');
const live = require('./live.cjs');
const main='a'.repeat(40), head='b'.repeat(40), base='c'.repeat(40), merged='d'.repeat(40);
const names=['actionlint','go','homebrew','dashboard','linux-smoke','windows-smoke','macos','macos-intel'];
function fixture() {
  const f={state:'active',tip:main,prs:[1,2],merges:[],reads:0,enabledReads:0,dispatches:[],valid:true,behind:0,
    writers:[{login:'jfox85',permissions:{push:true,maintain:true,admin:true}}],protection:{protected:true,protection:{enabled:true,required_status_checks:{enforcement_level:"everyone",checks:names.map(context=>({context,app_id:15368}))}}}};
  f.pr=n=>({number:n,state:'open',draft:false,mergeable:true,updated_at:'now',labels:[],head:{sha:head,repo:{full_name:'croutoncreations/redline'}},base:{sha:base,ref:'main'}});
  f.result=n=>({repository:'croutoncreations/redline',pr_number:n,head_sha:head,base_sha:base,base_branch:'main',pr_updated_at:'now',evaluated_at:new Date().toISOString(),verdict:'would_merge',rules:[{pass:true}]});
  f.github={paginate:async()=>f.prs.map(f.pr),graphql:async()=>{throw new Error('branch administration is inaccessible to GITHUB_TOKEN')},rest:{
    actions:{createWorkflowDispatch:async args=>{f.dispatches.push(args);if(f.dispatchError)throw new Error('dispatch failed')},getWorkflow:async()=>{f.enabledReads++;return {data:{state:f.state}}}},git:{getRef:async()=>({data:{object:{sha:f.tip}}})},
    repos:{getCollaboratorPermissionLevel:async({username})=>{if(f.permissionError)throw new Error('permission inaccessible');return {data:f.permissionResult ? f.permissionResult(username) : {permission:username==='jfox85' ? 'admin' : username==='reader' ? 'read' : 'write',user:{login:username}}}},getBranch:async()=>({data:f.protection ?? {}}),listCollaborators:()=>{},compareCommitsWithBasehead:async args=>{assert.ok(args.basehead.startsWith(`${main}...`));return {data:{behind_by:f.behind,status:'ahead',merge_base_commit:{sha:f.behind ? base : main}}}}},
    pulls:{get:async({pull_number:n})=>{f.reads++;return {data:f.pr(n)}},merge:async args=>{f.merges.push(args);if(f.mergeError)throw new Error('timeout');return {data:{merged:!f.rejected,sha:merged}}}}
  }};
  f.accessAudit={paginate:async method=>{assert.equal(method,f.accessAudit.rest.repos.listCollaborators);return f.writers},rest:{repos:{listCollaborators:f.github.rest.repos.listCollaborators,getCollaboratorPermissionLevel:f.github.rest.repos.getCollaboratorPermissionLevel}}};
  delete f.github.rest.repos.listCollaborators;delete f.github.rest.repos.getCollaboratorPermissionLevel;
  f.context={repo:{owner:'croutoncreations',repo:'redline'},sha:main,ref:'refs/heads/main',eventName:'schedule'};
  f.run=(_path,_args,opts)=>{assert.equal(opts.env.CONFIG_PATH,'');assert.equal(opts.env.REQUIRE_AUTOMATED_REVIEW,'true');assert.equal(opts.env.EXPECTED_HEAD_SHA,head);return JSON.stringify(f.result(Number(opts.env.PR_NUMBER)))};
  return f;
}
async function run(f) {
  const before=process.env.LIVE_PILOT_ENABLED; process.env.LIVE_PILOT_ENABLED=f.optin ?? 'true';
  try { return await live({github:f.github,accessAudit:f.accessAudit,context:f.context,core:{info:()=>{},warning:()=>{}},run:f.run,validate:async args=>{if(f.onValidate)f.onValidate(args.result);return f.valid},write:r=>{f.report=structuredClone(r)}}); }
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
 untrustedWriter:f=>f.writers.push({login:'other',permissions:{push:true,maintain:false,admin:false}}),missingWriterPermissions:f=>f.writers.push({login:'other'}),writerAddedDuringEvaluation:f=>f.onValidate=()=>f.writers.push({login:'other',permissions:{push:true}}),missingProtection:f=>f.protection=null,notProtected:f=>f.protection.protected=false,adminBypass:f=>f.protection.protection.required_status_checks.enforcement_level='non_admins',
 spoofedSource:f=>f.protection.protection.required_status_checks.checks[0].app_id=1,missingCI:f=>f.protection.protection.required_status_checks.checks.pop(),
 wrongRepo:f=>f.context.repo.repo='other',untrustedRef:f=>f.context.ref='refs/pull/1/merge',reviewEvent:f=>f.context.eventName='pull_request_review',
 protectionChanges:f=>f.onValidate=()=>f.protection=null,variableReadFailure:f=>f.github.rest.actions.getWorkflow=async()=>{throw new Error('403')}
}))test(`fails closed: ${name}`,async()=>{const f=fixture();edit(f);await assert.rejects(run(f));assert.equal(f.merges.length,0)});
test('ambiguous merge failure never retries or tries next candidate',async()=>{const f=fixture();f.mergeError=true;await assert.rejects(run(f),/timeout/);assert.equal(f.merges.length,1);assert.equal(f.report.state,'merge_requested')});
test('rejected merge never advances to second candidate',async()=>{const f=fixture();f.rejected=true;await assert.rejects(run(f));assert.equal(f.merges.length,1)});
test('CI-completion trigger runs the same worker from trusted main',async()=>{
 const f=fixture();f.context.eventName='workflow_run';await run(f);assert.equal(f.merges.length,1);
});
const httpError=(status,message)=>Object.assign(new Error(message),{status});
for(const [name,edit] of Object.entries({
 evaluator504:f=>f.run=()=>{throw Object.assign(new Error('Command failed: /tmp/green-lane-check\nError: failed to fetch PR: GitHub API error 504\n'),{status:1,stderr:'Error: failed to fetch PR: GitHub API error 504\n'})},
 evaluatorTransport:f=>f.run=()=>{throw Object.assign(new Error('Command failed'),{status:1,stderr:'Error: Get "https://api.github.com/x": read tcp: connection reset by peer\n'})},
 evaluatorTimeout:f=>f.run=()=>{throw Object.assign(new Error('spawnSync ETIMEDOUT'),{code:'ETIMEDOUT',signal:'SIGTERM'})},
 octokit502:f=>f.github.rest.git.getRef=async()=>{throw httpError(502,'Bad Gateway')},
 octokitFetchFailed:f=>f.github.rest.pulls.get=async()=>{throw new Error('fetch failed')},
 audit503:f=>f.accessAudit.paginate=async()=>{throw httpError(503,'Service Unavailable')},
 transientBeforeMerge:f=>f.onValidate=()=>{f.github.rest.repos.getBranch=async()=>{throw httpError(500,'Server Error')}},
}))test(`transient GitHub error defers cleanly without merging: ${name}`,async()=>{
 const f=fixture();edit(f);const warnings=[];
 const before=process.env.LIVE_PILOT_ENABLED;process.env.LIVE_PILOT_ENABLED='true';
 try{await live({github:f.github,accessAudit:f.accessAudit,context:f.context,core:{info:()=>{},warning:w=>warnings.push(w)},run:f.run,validate:async a=>{if(f.onValidate)f.onValidate(a.result);return f.valid},write:r=>{f.report=structuredClone(r)}})}
 finally{if(before===undefined)delete process.env.LIVE_PILOT_ENABLED;else process.env.LIVE_PILOT_ENABLED=before}
 assert.equal(f.merges.length,0);assert.equal(f.report.state,'deferred_transient');assert.ok(f.report.transient_error);assert.equal(warnings.length,1);
});
for(const [name,edit] of Object.entries({
 evaluator401:f=>f.run=()=>{throw Object.assign(new Error('Command failed'),{status:1,stderr:'Error: failed to fetch PR: GitHub API error 401\n'})},
 octokit403:f=>f.github.rest.git.getRef=async()=>{throw httpError(403,'Resource not accessible')},
 badJSON:f=>f.run=()=>'not json',
}))test(`non-transient error still fails closed: ${name}`,async()=>{const f=fixture();edit(f);await assert.rejects(run(f));assert.equal(f.merges.length,0)});
test('5xx from the merge call itself is ambiguous: fails loudly, never deferred or retried',async()=>{
 const f=fixture();f.github.rest.pulls.merge=async args=>{f.merges.push(args);throw httpError(504,'Gateway Timeout')};
 await assert.rejects(run(f),/Gateway Timeout/);assert.equal(f.merges.length,1);assert.equal(f.report.state,'merge_requested');
});
test('5xx from post-merge CI dispatch still fails loudly after the confirmed merge',async()=>{
 const f=fixture();f.github.rest.actions.createWorkflowDispatch=async()=>{throw httpError(502,'Bad Gateway')};
 await assert.rejects(run(f),/Bad Gateway/);assert.equal(f.merges.length,1);assert.equal(f.report.state,'merged');
});

test('post-merge dispatch failure preserves confirmed merge and never retries merge',async()=>{const f=fixture();f.dispatchError=true;await assert.rejects(run(f),/dispatch failed/);assert.equal(f.merges.length,1);assert.equal(f.report.state,'merged');assert.equal(f.report.merged[0].commit,merged);assert.equal(f.dispatches.length,1)});

test('read-only collaborators cannot provide branch-write authorization',async()=>{const f=fixture();f.writers.push({login:'reader',permissions:{push:false,maintain:false,admin:false}});await run(f);assert.equal(f.merges.length,1)});

test('installation-token collaborator list can omit flags when explicit permission proves owner control',async()=>{
 const f=fixture();f.writers=[{login:'jfox85'},{login:'reader'}];await run(f);assert.equal(f.merges.length,1);
 assert.equal(f.report.writer_snapshots.length,2);assert.equal(f.report.writer_snapshots[0][0].permission,'admin');
});
for(const [name,edit] of Object.entries({
 missingOwner:f=>f.writers=[{login:'reader'}],emptyList:f=>f.writers=[],
 inaccessible:f=>f.permissionError=true,unknown:f=>f.permissionResult=login=>({permission:'unknown',user:{login}}),
 missingUser:f=>f.permissionResult=()=>({permission:'admin'}),wrongUser:f=>f.permissionResult=()=>({permission:'admin',user:{login:'other'}}),
 ownerReadOnly:f=>f.permissionResult=login=>({permission:'read',user:{login}}),
 hiddenWriter:f=>{f.writers.push({login:'hidden'});f.permissionResult=login=>({permission:login==='jfox85'?'admin':'write',user:{login}})},
 changedBeforeMerge:f=>f.onValidate=()=>f.permissionResult=login=>({permission:'read',user:{login}})
}))test(`explicit writer permission fails closed: ${name}`,async()=>{
 const f=fixture();edit(f);await assert.rejects(run(f));assert.equal(f.merges.length,0);assert.ok(f.report.writer_snapshots.length>=1);
});

test('missing audit credential fails closed without falling back to workflow token',async()=>{
 const f=fixture();delete f.accessAudit;await assert.rejects(run(f),/Missing read-only/);assert.equal(f.merges.length,0);
});
test('expired audit credential cannot authorize a merge',async()=>{
 const f=fixture();f.accessAudit.paginate=async()=>{throw new Error('401')};await assert.rejects(run(f),/401/);assert.equal(f.merges.length,0);
});
for(const present of [true,false])test(`workflow isolates audit token and removes it before evaluator: ${present}`,async()=>{
 const fs=require('node:fs');
 const yaml=fs.readFileSync(require('node:path').join(__dirname,'../workflows/green-lane-live.yml'),'utf8');
 const script=yaml.match(/          script: \|\n((?:            .*\n)+)/)[1].replace(/^            /gm,'');
 const processMock={env:present?{GREEN_LANE_ACCESS_AUDIT_TOKEN:'test-only-audit-credential'}:{}};
 const normal={constructor:class AuditClient{constructor(opts){assert.equal(opts.auth,'test-only-audit-credential');assert.equal(opts.request.retries,0)}}};
 let called=false;
 const requireMock=path=>{assert.equal(path,'./.github/green-lane/live.cjs');return async args=>{
  called=true;assert.equal(args.github,normal);assert.equal(processMock.env.GREEN_LANE_ACCESS_AUDIT_TOKEN,undefined);
  if(present)assert.notEqual(args.accessAudit,normal);else assert.equal(args.accessAudit,undefined);
 }};
 const AsyncFunction=Object.getPrototypeOf(async function(){}).constructor;
 await new AsyncFunction('github','context','core','require','process',script)(normal,{}, {},requireMock,processMock);assert.ok(called);
});

// Merge-queue mode: enqueue the exact evaluated head instead of merging.
function queueFixture(){
 const f=fixture();f.behind=3;f.protection.protection.required_status_checks.checks.push({context:'green-lane-queue',app_id:15368});f.queue={exists:true,inQueue:false,enqueues:[],dequeues:[],headBack:null};
 const pr=f.pr;f.pr=n=>({...pr(n),node_id:`PR_${n}`});
 f.github.graphql=async(q,v)=>{
  if(q.includes('mergeQueue(branch:"main")'))return {repository:{mergeQueue:f.queue.exists?{id:'MQ_1',configuration:{mergeMethod:'SQUASH',mergingStrategy:'ALLGREEN',maximumEntriesToBuild:1,...f.queue.config}}:null}};
  if(q.includes('dequeuePullRequest')){f.queue.dequeues.push(v.id);return {dequeuePullRequest:{mergeQueueEntry:{id:'MQE_x'}}}}
  if(q.includes('isInMergeQueue')){
   const inQ=typeof f.queue.inQueue==='function'?f.queue.inQueue(v.n):f.queue.inQueue;
   if(!q.includes('mergeQueueEntry'))return {repository:{pullRequest:{isInMergeQueue:inQ}}};
   f.queue.entryReads=(f.queue.entryReads??0)+1;
   const entry=f.queue.entry?.(f.queue.entryReads) ?? {id:'MQE_q',enqueuer:{login:f.queue.enqueuer ?? 'github-actions'}};
   return {repository:{pullRequest:{id:`PR_${v.n}`,isInMergeQueue:inQ,headRefOid:f.queue.queuedHead ?? head,mergeQueueEntry:inQ?entry:null}}};
  }
  if(q.includes('enqueuePullRequest')){assert.match(q,/expectedHeadOid:\$head/,'enqueue must pin the evaluated head');f.queue.enqueues.push(v);if(f.queue.enqueueError)throw f.queue.enqueueError;return {enqueuePullRequest:{mergeQueueEntry:{id:'MQE_1',headCommit:null,pullRequest:{number:Number(v.id.slice(3)),headRefOid:f.queue.headBack ?? v.head}}}}}
  throw new Error('unexpected graphql '+q);
 };
 return f;
}
async function runQueue(f){const before=process.env.GREEN_LANE_MERGE_QUEUE;process.env.GREEN_LANE_MERGE_QUEUE='true';try{return await run(f)}finally{if(before===undefined)delete process.env.GREEN_LANE_MERGE_QUEUE;else process.env.GREEN_LANE_MERGE_QUEUE=before}}
test('queue mode: enqueues one exact head even when behind main; never calls merge or dispatch',async()=>{
 const f=queueFixture();const r=await runQueue(f);
 assert.equal(f.merges.length,0);assert.equal(f.dispatches.length,0);
 assert.deepEqual(f.queue.enqueues,[{id:'PR_1',head}]);assert.equal(r.state,'enqueued');assert.equal(r.mode,'merge_queue');
 assert.deepEqual(r.enqueued,[{pr:1,head,entry:'MQE_1'}]);
});
test('queue mode: evaluator runs with MERGE_QUEUE so the Main Drift rule applies',async()=>{
 const f=queueFixture();const seen=[];const inner=f.run;f.run=(p,a,o)=>{seen.push(o.env.MERGE_QUEUE);return inner(p,a,o)};
 await runQueue(f);assert.deepEqual(seen,['true']);
});
test('direct mode: evaluator gets no MERGE_QUEUE flag and stale PRs still need a refresh',async()=>{
 const f=fixture();f.behind=3;const seen=[];const inner=f.run;f.run=(p,a,o)=>{seen.push(o.env.MERGE_QUEUE);return inner(p,a,o)};
 const r=await run(f);assert.deepEqual(seen,[]);assert.equal(f.merges.length,0);assert.ok(r.evaluations.every(e=>e.state==='needs_main_refresh'));
});
test('queue mode: skips PRs already in the queue',async()=>{
 const f=queueFixture();f.queue.inQueue=n=>n===1;const r=await runQueue(f);
 assert.deepEqual(f.queue.enqueues.map(e=>e.id),['PR_2']);assert.equal(r.evaluations[0].state,'already_queued');
});
test('queue mode: unknown queue membership is treated as queued (fail closed)',async()=>{
 const f=queueFixture();f.queue.inQueue=null;const r=await runQueue(f);assert.equal(f.queue.enqueues.length,0);assert.equal(r.state,'deferred');
});
test('queue mode: no merge queue on main fails closed before evaluating',async()=>{
 const f=queueFixture();f.queue.exists=false;await assert.rejects(runQueue(f),/no merge queue/);assert.equal(f.queue.enqueues.length,0);assert.equal(f.merges.length,0);
});
test('queue mode: a queue not configured as squash/ALLGREEN/one-at-a-time fails closed',async()=>{
 for(const config of [{mergeMethod:'MERGE'},{mergingStrategy:'HEADGREEN'},{maximumEntriesToBuild:5},{mergeMethod:null}]){
  const f=queueFixture();f.queue.config=config;await assert.rejects(runQueue(f),/no merge queue configured/);assert.equal(f.queue.enqueues.length,0);
 }
});
test('queue mode: queue removed during evaluation fails closed',async()=>{
 const f=queueFixture();f.onValidate=()=>{f.queue.exists=false};await assert.rejects(runQueue(f),/disappeared/);assert.equal(f.queue.enqueues.length,0);
});
test('queue mode: main moving during evaluation does not block (the queue retests)',async()=>{
 const f=queueFixture();f.onValidate=()=>{f.tip='e'.repeat(40)};const r=await runQueue(f);assert.equal(r.state,'enqueued');
});
test('queue mode: kill switch during evaluation stops the enqueue',async()=>{
 const f=queueFixture();f.onValidate=()=>{f.state='disabled_manually'};const r=await runQueue(f);assert.equal(f.queue.enqueues.length,0);assert.match(r.reason,/Kill switch/);
});
test('queue mode: entry for a different head fails loudly and is never retried',async()=>{
 const f=queueFixture();f.queue.headBack='f'.repeat(40);await assert.rejects(runQueue(f),/exact-head/);assert.equal(f.queue.enqueues.length,1);assert.equal(f.report.state,'enqueue_requested');
});
test('queue mode: 5xx from the enqueue call is ambiguous: fails loudly, never deferred or retried',async()=>{
 const f=queueFixture();f.queue.enqueueError=httpError(502,'Bad Gateway');await assert.rejects(runQueue(f),/Bad Gateway/);assert.equal(f.queue.enqueues.length,1);assert.equal(f.report.state,'enqueue_requested');
});
test('queue mode: writer audit and protection still gate the enqueue',async()=>{
 const f=queueFixture();f.writers.push({login:'hidden'});f.permissionResult=login=>({permission:login==='jfox85'?'admin':'write',user:{login}});
 await assert.rejects(runQueue(f));assert.equal(f.queue.enqueues.length,0);
 const g=queueFixture();g.protection={protected:false};await assert.rejects(runQueue(g),/protection/);assert.equal(g.queue.enqueues.length,0);
});
test('queue mode: green-lane-queue must be a required check',async()=>{
 const f=queueFixture();f.protection.protection.required_status_checks.checks=f.protection.protection.required_status_checks.checks.filter(c=>c.context!=='green-lane-queue');
 await assert.rejects(runQueue(f),/protection/);assert.equal(f.queue.enqueues.length,0);
});
test('direct mode does not require green-lane-queue',async()=>{const f=fixture();await run(f);assert.equal(f.merges.length,1)});

// Queue mode keeps authorization live until the queue merges.
test('queue mode: a queued PR that still qualifies stays queued',async()=>{
 const f=queueFixture();f.queue.inQueue=n=>n===1;const r=await runQueue(f);
 assert.deepEqual(f.queue.dequeues,[]);assert.deepEqual(r.dequeued,[]);
});
test('queue mode: a queued PR that no longer qualifies is removed from the queue',async()=>{
 const f=queueFixture();f.queue.inQueue=n=>n===1;const inner=f.result;
 f.result=n=>n===1?{...inner(n),verdict:'would_not_merge',rules:[{name:'Review Readiness',pass:false,message:'Unclassified review bodies'}]}:inner(n);
 const r=await runQueue(f);assert.deepEqual(f.queue.dequeues,['PR_1']);assert.equal(r.dequeued[0].pr,1);assert.match(r.dequeued[0].reason,/Review Readiness/);
});
test('queue mode: a hold label on a queued PR removes it from the queue',async()=>{
 const f=queueFixture();f.queue.inQueue=n=>n===1;const pr=f.pr;f.pr=n=>n===1?{...pr(n),labels:[{name:'green-lane-hold'}]}:pr(n);
 const r=await runQueue(f);assert.deepEqual(f.queue.dequeues,['PR_1']);assert.equal(r.dequeued[0].reason,'hold label');
});
test('queue mode: a queued head that differs from the PR head is removed',async()=>{
 const f=queueFixture();f.queue.inQueue=n=>n===1;f.queue.queuedHead='9'.repeat(40);
 const r=await runQueue(f);assert.deepEqual(f.queue.dequeues,['PR_1']);
});
test('queue mode: a PR Jon queued by hand is left alone',async()=>{
 const f=queueFixture();f.queue.inQueue=n=>n===1;f.queue.enqueuer='jfox85';const inner=f.result;
 f.result=n=>n===1?{...inner(n),verdict:'would_not_merge',rules:[{name:'Size',pass:false,message:'too big'}]}:inner(n);
 await runQueue(f);assert.deepEqual(f.queue.dequeues,[]);
});
test('queue mode: an entry re-queued by hand during the evaluation is left alone',async()=>{
 for(const later of [{id:'MQE_new',enqueuer:{login:'jfox85'}},{id:'MQE_new',enqueuer:{login:'github-actions'}},{id:'MQE_q',enqueuer:{login:'jfox85'}}]){
  const f=queueFixture();f.queue.inQueue=n=>n===1;f.queue.entry=i=>i>1?later:undefined;const inner=f.result;
  f.result=n=>n===1?{...inner(n),verdict:'would_not_merge',rules:[{name:'Size',pass:false,message:'too big'}]}:inner(n);
  await runQueue(f);assert.deepEqual(f.queue.dequeues,[],JSON.stringify(later));
 }
});
test('queue mode: GitHub reporting a queued PR as not mergeable does not dequeue it',async()=>{
 const f=queueFixture();f.queue.inQueue=n=>n===1;const inner=f.result;
 f.result=n=>n===1?{...inner(n),verdict:'would_not_merge',rules:[{name:'PR State',pass:false,message:'PR mergeability is unknown or conflicted'},{pass:true}]}:inner(n);
 await runQueue(f);assert.deepEqual(f.queue.dequeues,[]);
});
test('queue mode: the dequeue sweep runs before new enqueues; a mid-run kill switch only stops the enqueue',async()=>{
 const f=queueFixture();f.queue.inQueue=n=>n===1;const inner=f.result;
 f.result=n=>n===1?{...inner(n),verdict:'would_not_merge',rules:[{name:'CI',pass:false,message:'failed'}]}:inner(n);
 f.onValidate=()=>{f.state='disabled_manually'};
 await runQueue(f);assert.deepEqual(f.queue.dequeues,['PR_1']);assert.equal(f.queue.enqueues.length,0);
});
