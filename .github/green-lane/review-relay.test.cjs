const {test}=require('node:test');
const assert=require('node:assert/strict');
const relay=require('./review-relay.cjs');
const head='a'.repeat(40);
function fixture(){return {run:{id:1,event:'pull_request_review',workflow_id:379979062,head_repository:{full_name:'owner/repo'},head_sha:head},prs:[{number:185,state:'open',head:{sha:head,repo:{full_name:'owner/repo'}}}]};}
async function execute(f){const dispatched=[];const github={paginate:async()=>f.prs,rest:{actions:{getWorkflowRun:async()=>({data:f.run}),createWorkflowDispatch:async input=>dispatched.push(input)},repos:{get:async()=>({data:{default_branch:'trunk'}}),listPullRequestsAssociatedWithCommit:()=>{}},pulls:{get:async()=>({data:f.current||f.prs[0]})}}};await relay({github,context:{repo:{owner:'owner',repo:'repo'},payload:{workflow_run:{id:1}}}});return dispatched;}
test('stale review run dispatches current trusted default branch',async()=>{const f=fixture();assert.deepEqual(await execute(f),[{owner:'owner',repo:'repo',workflow_id:'green-lane-shadow.yml',ref:'trunk',inputs:{pr_number:'185'}}]);});
for(const [name,edit] of Object.entries({dispatch:f=>f.run.event='workflow_dispatch',recursive:f=>f.run.event='workflow_run',otherWorkflow:f=>f.run.workflow_id++,fork:f=>f.run.head_repository.full_name='fork/repo',badSHA:f=>f.run.head_sha='not-a-sha',closed:f=>f.prs[0].state='closed',changedHead:f=>f.current={...f.prs[0],head:{sha:'b'.repeat(40)}},noPR:f=>f.prs=[]}))test(`no dispatch: ${name}`,async()=>{const f=fixture();edit(f);assert.deepEqual(await execute(f),[]);});
test('duplicate associations dispatch once',async()=>{const f=fixture();f.prs.push(structuredClone(f.prs[0]));assert.equal((await execute(f)).length,1);});
