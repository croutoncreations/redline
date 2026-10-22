const fs = require('node:fs');
const {createHash} = require('node:crypto');

// This report is never merge authorization. The publisher executes trusted
// base code only; invalidation must clear both the verdict and the advisory bit.
async function reviewSnapshot(github, context, number, head, base) {
  const {owner, repo} = context.repo;
  const reviews = (await github.paginate(github.rest.pulls.listReviews, {owner,repo,pull_number:number,per_page:100}))
    .map(r=>({id:r.id,user:r.user.login,state:r.state,commit:r.commit_id})).sort((a,b)=>a.id-b.id);
  const threads=[]; const seen=new Set(); let cursor=null;
  for (let page=0;page<100;page++) {
    const response=await github.graphql(`query($owner:String!,$repo:String!,$number:Int!,$cursor:String){repository(owner:$owner,name:$repo){pullRequest(number:$number){headRefOid baseRefOid reviewThreads(first:100,after:$cursor){totalCount nodes{id isResolved isOutdated} pageInfo{hasNextPage endCursor}}}}}`,{owner,repo,number,cursor});
    const pr=response.repository?.pullRequest;
    if (!pr || pr.headRefOid!==head || pr.baseRefOid!==base || !pr.reviewThreads?.pageInfo) throw new Error('Review snapshot changed or incomplete');
    for (const t of pr.reviewThreads.nodes) threads.push({id:t.id,resolved:t.isResolved,outdated:t.isOutdated});
    if (!pr.reviewThreads.pageInfo.hasNextPage) {
      if (threads.length!==pr.reviewThreads.totalCount || new Set(threads.map(t=>t.id)).size!==threads.length) throw new Error('Incomplete review-thread pagination');
      threads.sort((a,b)=>a.id<b.id?-1:a.id>b.id?1:0);
      return createHash('sha256').update(JSON.stringify({threads,reviews})).digest('hex');
    }
    cursor=pr.reviewThreads.pageInfo.endCursor;
    if (!cursor || seen.has(cursor)) throw new Error('Review-thread pagination stalled');
    seen.add(cursor);
  }
  throw new Error('Review-thread pagination did not finish');
}

async function checkSnapshot(github, context, result) {
 const {owner,repo}=context.repo;
 const evidence=[];const seen=new Set();let count=0;
 for(let page=1;page<=100;page++){
  const {data}=await github.rest.checks.listForRef({owner,repo,ref:result.head_sha,per_page:100,page});
  count+=data.check_runs.length;
  for(const r of data.check_runs){
   const receipt=r.name==='green-lane-provenance' && (result.provenance_apps||[]).includes(r.app?.id);
   if(!receipt && !Object.hasOwn(result.check_sources||{},r.name))continue;
   if(seen.has(r.id))throw new Error('Duplicate check run');seen.add(r.id);
   evidence.push({id:r.id,name:r.name,status:r.status,conclusion:r.conclusion||'',head:r.head_sha,app:r.app.id,
    details:Buffer.from(r.details_url||'','utf8').toString('base64'),text:Buffer.from(receipt?(r.output?.text||''):'','utf8').toString('base64')});
  }
  if(data.check_runs.length<100){
   if(count!==data.total_count)throw new Error('Incomplete check-run pagination');
   evidence.sort((a,b)=>a.id-b.id);
   return createHash('sha256').update(JSON.stringify(evidence)).digest('hex');
  }
 }
 throw new Error('Check-run pagination did not finish');
}

module.exports = async function publish({github, context, result, checkId, expectedHead, writeResult = r => fs.writeFileSync('evaluation-result.json', JSON.stringify(r, null, 2))}) {
  const {owner, repo} = context.repo;
  if (!Number.isSafeInteger(result.pr_number) || result.pr_number <= 0) throw new Error('Invalid result PR number');
  const {data: current} = await github.rest.pulls.get({owner, repo, pull_number: result.pr_number});
  let checksMatch=false;
  try{checksMatch=result.check_snapshot===await checkSnapshot(github,context,result)}catch{checksMatch=false}
  let reviewsMatch=false;
  try { reviewsMatch=result.review_snapshot===await reviewSnapshot(github,context,result.pr_number,result.head_sha,result.base_sha); }
  catch { reviewsMatch=false; }
  const age = Date.now() - Date.parse(result.evaluated_at);
  if (!checksMatch || !reviewsMatch || result.repository !== `${owner}/${repo}` || current.head.sha !== result.head_sha ||
      current.base.sha !== result.base_sha || current.base.ref !== result.base_branch ||
      current.updated_at !== result.pr_updated_at || current.state !== 'open' || current.draft ||
      current.mergeable !== true || (expectedHead && expectedHead !== result.head_sha) ||
      !Number.isFinite(age) || age < -60000 || age > 300000) {
    result.verdict = 'would_not_merge';
    result.advisory_candidate = false;
    result.candidate_message = 'Snapshot changed or expired before publication; rerun required';
    result.rules.push({name: 'Publication Snapshot', pass: false, inconclusive: true, message: result.candidate_message});
    result.summary = result.candidate_message;
  }
  // A candidate never changes the authorization verdict or check conclusion.
  const candidate = result.advisory_candidate === true;
  let summary = `# ${result.verdict.toUpperCase()} (shadow only)\n\n`;
  summary += `**Advisory candidate:** ${candidate ? 'yes' : 'no'} — never merge authorization.\n\n${result.candidate_message}\n\n`;
  summary += `PR #${result.pr_number} · head \`${result.head_sha}\` · base \`${result.base_sha}\`\n\n`;
  for (const rule of result.rules) summary += `### ${rule.pass ? 'PASS' : rule.inconclusive ? 'INCONCLUSIVE' : 'FAIL'}: ${rule.name}\n${rule.message}\n\n`;
  summary += `${result.summary}\n\nShadow only. No merge or approval action is taken. Automatic merging has not been enabled.`;
  await writeResult(result);
  const serialized = JSON.stringify(result);
  const output = {title: `Green Lane Shadow: ${result.verdict}`, summary: summary.slice(0,10000),
    text: serialized.length <= 12000 ? serialized : 'Full evaluation JSON is preserved in the workflow artifact.'};
  const check = {owner, repo, status: 'completed', conclusion: result.verdict === 'would_merge' ? 'success' : 'neutral', output};
  if (checkId) await github.rest.checks.update({...check, check_run_id: checkId});
  else await github.rest.checks.create({...check, name: 'green-lane-shadow', head_sha: result.head_sha});
  return result;
};
