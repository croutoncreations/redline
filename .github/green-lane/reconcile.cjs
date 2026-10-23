const {execFileSync} = require('node:child_process');
const fs = require('node:fs');
const publish = require('./publish.cjs');

// This job never checks out PR code, consumes repro artifacts, or holds merge
// credentials. Required CI supplies the test gate for all code changes.
module.exports = async ({github, context, core, run=execFileSync, publishReport=publish}) => {
  const prs = await github.paginate(github.rest.pulls.list, {...context.repo, state:'open', per_page:100});
  let selected = prs;
  if (process.env.RECONCILE_PR_NUMBERS) {
    const numbers = JSON.parse(process.env.RECONCILE_PR_NUMBERS);
    if (!Array.isArray(numbers) || numbers.length > 5 || new Set(numbers).size !== numbers.length || numbers.some(n => !Number.isSafeInteger(n) || n <= 0)) throw new Error('Invalid reconciliation shard');
    // PRs closed after planning need no refresh; the closed-event job invalidates them.
    selected = prs.filter(pr => numbers.includes(pr.number));
  } else if (prs.length > 5) throw new Error('More than five open PRs; reconciliation requires a planned shard');
  let failures = 0;
  for (const pr of selected) {
    try {
      if (!Number.isSafeInteger(pr.number) || pr.number <= 0) throw new Error('Invalid PR number');
      const raw = run(`${process.env.RUNNER_TEMP}/green-lane-check`, [], {
        encoding:'utf8', timeout:180000, maxBuffer:2*1024*1024,
        env:{...process.env, PR_NUMBER:String(pr.number), EXPECTED_HEAD_SHA:pr.head.sha, EXPECTED_BASE_SHA:pr.base.sha, REPRO_PASS:'',REPRO_INCONCLUSIVE:'',REPRO_HEAD_SHA:'',REPRO_BASE_SHA:''}
      });
      await publishReport({github,context,result:JSON.parse(raw),expectedHead:pr.head.sha,writeResult:r=>fs.writeFileSync(`evaluation-${pr.number}.json`,JSON.stringify(r,null,2))});
    } catch (err) {
      failures++;
      // Clear a stale positive report even when collection fails. Do not
      // leave the last candidate on the current head as the newest signal.
      core.warning(`PR #${pr.number}: reconciliation failed`);
      await github.rest.checks.create({...context.repo,name:'green-lane-shadow',head_sha:pr.head.sha,status:'completed',conclusion:'neutral',output:{title:'Green Lane Shadow: unavailable',summary:'Evidence collection failed; no candidate or merge authorization. A fresh evaluation is required.'}});
    }
  }
  if (failures) core.setFailed(`${failures} PR evaluations failed closed`);
};
