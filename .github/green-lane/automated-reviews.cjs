// Only App-owned bot summaries are evidence. The evaluator parses completion;
// the publisher independently re-fetches and hashes the exact authenticated text.
function trusted(c) {
 if(c.id<=0 || c.user?.type!=='Bot')return false;
 return (c.user.id===136622811 && c.user.login==='coderabbitai[bot]' && c.performed_via_github_app?.id===347564 && c.body.startsWith('<!-- coderabbit-ai-generated:v1 -->') && c.body.includes('\n<!-- This is an auto-generated comment: summarize by coderabbit.ai -->')) ||
 (c.user.id===199175422 && c.user.login==='chatgpt-codex-connector[bot]' && c.performed_via_github_app?.id===1144995 && c.body.startsWith('<!-- codex-pull-request-review-summary -->'));
}
module.exports=async function automatedReviews(github,context,number){
 const {owner,repo}=context.repo; const rows=[];const seen=new Set();let finished=false;
 for(let page=1;page<=100;page++){
  const {data}=await github.rest.issues.listComments({owner,repo,issue_number:number,per_page:100,page});
  for(const c of data){
   if(!Number.isSafeInteger(c.id)||c.id<=0||seen.has(c.id))throw new Error('Invalid or repeated issue-comment page');seen.add(c.id);
   if(trusted(c)){if(!c.node_id)throw new Error('Missing summary node ID');rows.push(c)}
  }
  if(data.length<100){finished=true;break}
 }
 if(!finished)throw new Error('Issue-comment pagination did not finish');
 const evidence=[];
 for(let start=0;start<rows.length;start+=100){
  const batch=rows.slice(start,start+100);
  const result=await github.graphql('query($ids:[ID!]!){nodes(ids:$ids){... on IssueComment{id databaseId body updatedAt lastEditedAt editor{__typename login ... on Bot{databaseId}}}}}',{ids:batch.map(c=>c.node_id)});
  if(!result.nodes||result.nodes.length!==batch.length)throw new Error('Summary editor query incomplete');
  for(let i=0;i<batch.length;i++){
   const c=batch[i],n=result.nodes[i];
   if(!n||n.id!==c.node_id||n.databaseId!==c.id||n.body!==c.body||n.updatedAt!==c.updated_at)throw new Error('Summary changed during editor verification');
   evidence.push({id:c.id,user_id:c.user.id,user:c.user.login,user_type:c.user.type,app_id:c.performed_via_github_app.id,body:Buffer.from(c.body,'utf8').toString('base64'),updated_at:c.updated_at,editor_id:n.editor?.databaseId||0,editor_user:n.editor?.login||'',editor_type:n.editor?.__typename||'',last_edited_at:n.lastEditedAt||''});
  }
 }
 return evidence.sort((a,b)=>a.id-b.id);
};
