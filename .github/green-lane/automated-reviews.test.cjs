const {test}=require('node:test');const assert=require('node:assert/strict');
const collect=require('./automated-reviews.cjs');
const context={repo:{owner:'owner',repo:'repo'}};
function fixture(){
 const c={id:42,node_id:'node',body:'<!-- coderabbit-ai-generated:v1 -->\n<!-- This is an auto-generated comment: summarize by coderabbit.ai -->',updated_at:'2026-01-01T00:00:00Z',user:{id:136622811,login:'coderabbitai[bot]',type:'Bot'},performed_via_github_app:{id:347564}};
 const node={id:'node',databaseId:42,body:c.body,updatedAt:c.updated_at,lastEditedAt:c.updated_at,editor:{__typename:'Bot',login:'coderabbitai',databaseId:136622811}};
 return {c,node};
}
function api(f){return {rest:{issues:{listComments:async()=>({data:[f.c]})}},graphql:async()=>({nodes:[f.node]})}}
test('collector binds raw summary and editor without interpreting completion',async()=>{const f=fixture();const [c]=await collect(api(f),context,1);assert.equal(c.editor_id,136622811);assert.equal(Buffer.from(c.body,'base64').toString(),f.c.body)});
for(const field of ['id','body','databaseId','updatedAt'])test(`reject changed ${field} between REST and GraphQL`,async()=>{const f=fixture();f.node[field]='changed';await assert.rejects(collect(api(f),context,1))});
test('missing editor node fails closed',async()=>{const f=fixture();f.node=null;await assert.rejects(collect(api(f),context,1))});
test('human editor remains in snapshot so invalid summary cannot disappear',async()=>{const f=fixture();f.node.editor={__typename:'User',login:'author'};const [c]=await collect(api(f),context,1);assert.equal(c.editor_type,'User');assert.equal(c.editor_id,0)});
test('spoofed bot App cannot enter snapshot',async()=>{const f=fixture();f.c.performed_via_github_app.id++;assert.deepEqual(await collect(api(f),context,1),[])});
test('pagination includes summaries beyond first page and rejects repeated pages',async()=>{const f=fixture();let pages=0;const g=api(f);g.rest.issues.listComments=async()=>({data:++pages===1?Array.from({length:100},(_,i)=>({id:100+i,user:{type:'User'}})):[f.c]});assert.equal((await collect(g,context,1)).length,1);assert.equal(pages,2);g.rest.issues.listComments=async()=>({data:Array.from({length:100},(_,i)=>({id:100+i,user:{type:'User'}}))});await assert.rejects(collect(g,context,1))});
