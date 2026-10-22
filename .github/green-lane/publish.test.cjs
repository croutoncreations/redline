const {test} = require('node:test');
const assert = require('node:assert/strict');
const {createHash}=require('node:crypto');
const publish = require('./publish.cjs');
const fixture = () => ({
  result: {repository:'owner/repo', pr_number:1, head_sha:'head', base_sha:'base', base_branch:'main', pr_updated_at:'now', evaluated_at:new Date().toISOString(), verdict:'would_not_merge', advisory_candidate:true, candidate_message:'provenance pending', review_snapshot:createHash('sha256').update(JSON.stringify({threads:[],reviews:[]})).digest('hex'), rules:[], summary:'blocked'},
  current: {head:{sha:'head'},base:{sha:'base',ref:'main'},updated_at:'now',state:'open',draft:false,mergeable:true}
});
async function run(f) {
 let posted, written;
 const github={paginate:async()=>f.reviews||[],graphql:async()=>({repository:{pullRequest:{headRefOid:f.current.head.sha,baseRefOid:f.current.base.sha,reviewThreads:{totalCount:(f.threads||[]).length,nodes:f.threads||[],pageInfo:{hasNextPage:false}}}}}),rest:{pulls:{get:async()=>({data:f.current}),listReviews:()=>{}},checks:{update:async c=>{posted=c}}}};
 await publish({context:{repo:{owner:'owner',repo:'repo'}}, result:f.result, expectedHead:'head', checkId:7, writeResult:r=>{written=structuredClone(r)}, github});
 return {posted,written};
}
test('advisory candidate cannot turn missing provenance into success',async()=>{const {posted,written}=await run(fixture());assert.equal(posted.conclusion,'neutral');assert.equal(written.advisory_candidate,true)});
for (const [name,edit] of Object.entries({head:f=>f.current.head.sha='new',base:f=>f.current.base.sha='new',branch:f=>f.current.base.ref='other',reviewActivity:f=>f.current.updated_at='later',draft:f=>f.current.draft=true,closed:f=>f.current.state='closed',conflict:f=>f.current.mergeable=false,unknownMergeability:f=>f.current.mergeable=null,expired:f=>f.result.evaluated_at='2000-01-01T00:00:00Z',repo:f=>f.result.repository='other/repo',threadReopened:f=>f.threads=[{id:'thread',isResolved:false,isOutdated:true}],reviewChanged:f=>f.reviews=[{id:1,user:{login:'reviewer'},state:'CHANGES_REQUESTED',commit_id:'head'}]})) {
 test(`publication invalidates ${name}`,async()=>{const f=fixture();f.result.verdict='would_merge';edit(f);const {posted,written}=await run(f);assert.equal(posted.conclusion,'neutral');assert.equal(written.advisory_candidate,false);assert.equal(written.verdict,'would_not_merge')});
}
