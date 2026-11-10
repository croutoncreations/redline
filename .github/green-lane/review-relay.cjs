// A workflow_run receiver executes from the default branch even when the
// originating review workflow definition came from a stale PR merge ref.
module.exports = async function relay({github, context}) {
  const {owner, repo} = context.repo;
  const {data: run} = await github.rest.actions.getWorkflowRun({owner, repo, run_id:context.payload.workflow_run.id});
  if (run.event !== 'pull_request_review' || run.workflow_id !== 379979062 ||
      run.head_repository?.full_name !== `${owner}/${repo}` || !/^[0-9a-f]{40}$/.test(run.head_sha)) return;
  const {data: repository} = await github.rest.repos.get({owner, repo});
  const associated = await github.paginate(github.rest.repos.listPullRequestsAssociatedWithCommit, {owner,repo,commit_sha:run.head_sha,per_page:100});
  const numbers = [...new Set(associated.filter(pr=>pr.state==='open' && pr.head?.sha===run.head_sha && pr.head?.repo?.full_name===`${owner}/${repo}`).map(pr=>pr.number))];
  if (numbers.length > 20 || numbers.some(n=>!Number.isSafeInteger(n)||n<=0)) throw new Error('Unexpected review PR association');
  for (const number of numbers) {
    // Re-fetch before dispatch so closed or changed heads are not replayed.
    const {data: pr} = await github.rest.pulls.get({owner,repo,pull_number:number});
    if (pr.state !== 'open' || pr.head.sha !== run.head_sha || pr.head.repo?.full_name !== `${owner}/${repo}`) continue;
    await github.rest.actions.createWorkflowDispatch({owner,repo,workflow_id:'green-lane-shadow.yml',ref:repository.default_branch,inputs:{pr_number:String(number)}});
  }
};
