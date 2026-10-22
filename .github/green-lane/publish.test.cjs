const {test} = require('node:test');
const assert = require('node:assert/strict');
const {createHash}=require('node:crypto');
const publish = require('./publish.cjs');
const baselineCheck={id:7,name:'go',status:'completed',conclusion:'success',head_sha:'head',app:{id:15368},details_url:'https://github.com/owner/repo/actions/runs/5/job/7'};
const baselineDigest=createHash('sha256').update(JSON.stringify([{id:7,name:'go',status:'completed',conclusion:'success',head:'head',app:15368,details:Buffer.from(baselineCheck.details_url).toString('base64'),text:''}])).digest('hex');
const fixture = () => ({
  checks:[structuredClone(baselineCheck)],
  result: {repository:'owner/repo', pr_number:1, head_sha:'head', base_sha:'base', base_branch:'main', pr_updated_at:'now', evaluated_at:new Date().toISOString(), verdict:'would_not_merge', advisory_candidate:true, candidate_message:'policy blocked', check_snapshot:baselineDigest,check_sources:{go:{app_id:15368,workflow_id:1}},provenance_apps:[77], review_snapshot:createHash('sha256').update(JSON.stringify({automated:[],threads:[],reviews:[]})).digest('hex'), rules:[], summary:'blocked'},
  current: {head:{sha:'head'},base:{sha:'base',ref:'main'},updated_at:'now',state:'open',draft:false,mergeable:true}
});
async function run(f) {
 let posted, written;
 const github={paginate:async()=>f.reviews||[],graphql:async(query)=>query.includes('nodes(ids:')?{nodes:f.summaryNodes||[]}:({repository:{pullRequest:{headRefOid:f.current.head.sha,baseRefOid:f.current.base.sha,reviewThreads:{totalCount:(f.threads||[]).length,nodes:f.threads||[],pageInfo:{hasNextPage:false}}}}}),rest:{issues:{listComments:async()=>({data:f.comments||[]})},pulls:{get:async()=>({data:f.current}),listReviews:()=>{}},checks:{update:async c=>{posted=c},listForRef:async()=>({data:{total_count:(f.checks||[]).length,check_runs:f.checks||[]}})}}};
 await publish({context:{repo:{owner:'owner',repo:'repo'}}, result:f.result, expectedHead:'head', checkId:7, writeResult:r=>{written=structuredClone(r)}, github});
 return {posted,written};
}
test('advisory flag cannot override a blocked verdict',async()=>{const {posted,written}=await run(fixture());assert.equal(posted.conclusion,'neutral');assert.equal(written.advisory_candidate,true)});
for (const [name,edit] of Object.entries({head:f=>f.current.head.sha='new',base:f=>f.current.base.sha='new',branch:f=>f.current.base.ref='other',reviewActivity:f=>f.current.updated_at='later',draft:f=>f.current.draft=true,closed:f=>f.current.state='closed',conflict:f=>f.current.mergeable=false,unknownMergeability:f=>f.current.mergeable=null,expired:f=>f.result.evaluated_at='2000-01-01T00:00:00Z',repo:f=>f.result.repository='other/repo',threadReopened:f=>f.threads=[{id:'thread',isResolved:false,isOutdated:true}],reviewChanged:f=>f.reviews=[{id:1,user:{login:'reviewer'},state:'CHANGES_REQUESTED',commit_id:'head'}],ciRerun:f=>{f.checks[0].status='in_progress';f.checks[0].conclusion=null},receiptPending:f=>f.checks.push({id:3,name:'green-lane-provenance',status:'queued',head_sha:'head',app:{id:77}})})) {
 test(`publication invalidates ${name}`,async()=>{const f=fixture();f.result.verdict='would_merge';edit(f);const {posted,written}=await run(f);assert.equal(posted.conclusion,'neutral');assert.equal(written.advisory_candidate,false);assert.equal(written.verdict,'would_not_merge')});
}

 test('current passing policy publishes a positive shadow report without approval or issuer provisioning',async()=>{const f=fixture();f.result.verdict='would_merge';f.result.candidate_message='All policy rules pass; shadow only';const {posted,written}=await run(f);assert.equal(posted.conclusion,'success');assert.equal(written.verdict,'would_merge')});

for(const change of ['edit','delete','human editor','running'])test(`publication invalidates summary ${change}`,async()=>{
 const f=fixture();const body='<!-- codex-pull-request-review-summary -->\noriginal';
 const comment={id:42,node_id:'node',body,updated_at:'2026-01-01T00:00:00Z',user:{id:199175422,login:'chatgpt-codex-connector[bot]',type:'Bot'},performed_via_github_app:{id:1144995}};
 const node={id:'node',databaseId:42,body,updatedAt:comment.updated_at,lastEditedAt:null,editor:null};
 f.comments=[comment];f.summaryNodes=[node];
 const canonical={id:42,user_id:199175422,user:'chatgpt-codex-connector[bot]',user_type:'Bot',app_id:1144995,body:Buffer.from(body).toString('base64'),updated_at:comment.updated_at,editor_id:0,editor_user:'',editor_type:'',last_edited_at:''};
 f.result.review_snapshot=createHash('sha256').update(JSON.stringify({automated:[canonical],threads:[],reviews:[]})).digest('hex');f.result.verdict='would_merge';
 let r=await run(structuredClone(f));assert.equal(r.posted.conclusion,'success');
 if(change==='delete')f.comments=[];
 else if(change==='human editor'){node.editor={__typename:'User',login:'author'};node.lastEditedAt=comment.updated_at}
 else{comment.body+='\n'+change;node.body=comment.body}
 r=await run(f);assert.equal(r.posted.conclusion,'neutral');assert.equal(r.written.verdict,'would_not_merge');
});
