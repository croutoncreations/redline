const {execFileSync} = require('node:child_process');
const fs = require('node:fs');
const {snapshotMatches} = require('./publish.cjs');

const checks = ['actionlint','go','homebrew','dashboard','linux-smoke','windows-smoke','macos','macos-intel'];
const sha = value => /^[a-f0-9]{40}$/.test(value || '');
const held = pr => pr.labels?.some(label => ['green-lane-hold','maintenance:skip','do-not-touch'].includes(label.name));

// Only trusted default-branch code calls this entry point. Shadow checks and
// downloaded artifacts are never inputs. A failed/ambiguous merge is not retried.
module.exports = async function live({github, accessAudit, context, core, run=execFileSync,
  validate=snapshotMatches, write=r=>fs.writeFileSync('live-result.json',JSON.stringify(r,null,2))}) {
  const report = {state:'deferred',main_tip:context.sha,merged:[],evaluations:[]};
  const finish = async reason => { report.reason=reason; await write(report); core.info(reason); return report; };
  const repo = context.repo;
  if (`${repo.owner}/${repo.repo}` !== 'croutoncreations/redline' || context.ref !== 'refs/heads/main' ||
      !['schedule','workflow_dispatch'].includes(context.eventName) || !sha(context.sha)) {
    throw new Error('Live pilot is restricted to trusted Redline main schedule/dispatch');
  }
  // Disabling the workflow is the kill switch. Unlike the run's cached vars
  // context, workflow state is readable live with the ordinary Actions token.
  const enabled = async () => {
    const {data} = await github.rest.actions.getWorkflow({...repo,workflow_id:'green-lane-live.yml'});
    return data.state === 'active';
  };
  const tip = async () => (await github.rest.git.getRef({...repo,ref:'heads/main'})).data.object.sha;
  const protectedMain = async () => {
    // GITHUB_TOKEN cannot read branch-administration details (including via
    // GraphQL). The contents-readable summary exposes protection, enforcement
    // and required App-bound checks. Strict up-to-date/conversation settings
    // remain configured and enforced natively; no admin credential is granted.
    const {data:branch} = await github.rest.repos.getBranch({...repo,branch:'main'});
    const p = branch.protection?.required_status_checks;
    return branch.protected === true && branch.protection?.enabled === true &&
      p?.enforcement_level === 'everyone' && checks.every(name =>
        p.checks?.some(c => c.context === name && c.app_id === 15368));
  };
  const trustedWriters = async () => {
    const users = await accessAudit.paginate(accessAudit.rest.repos.listCollaborators,{...repo,affiliation:'all',per_page:100});
    const snapshot = [];
    report.writer_snapshots ??= [];
    report.writer_snapshots.push(snapshot);
    // PR authorship alone does not establish control of a same-repository head.
    // The list response may omit permission flags for installation tokens. Read
    // each user's explicit repository permission; missing/unknown proof blocks.
    // Persist only public identity/permission evidence, never request headers.
    try {
      for (const user of users) {
        if (typeof user.login !== 'string' || !user.login) return false;
        const entry = {login:user.login,list_permissions:user.permissions ?? null};
        snapshot.push(entry);
        const {data} = await accessAudit.rest.repos.getCollaboratorPermissionLevel({...repo,username:user.login});
        entry.permission = data.permission ?? null;
        entry.verified_login = data.user?.login ?? null;
        if (entry.verified_login !== user.login || !['admin','maintain','write','read','triage'].includes(entry.permission)) return false;
        if (user.login === 'jfox85') {
          if (!['admin','maintain','write'].includes(entry.permission)) return false;
        } else if (!['read','triage'].includes(entry.permission) ||
          ['push','maintain','admin'].some(key => user.permissions?.[key] === true)) return false;
      }
      return snapshot.some(user => user.login === 'jfox85');
    } finally { await write(report); }
  };
  if (process.env.LIVE_PILOT_ENABLED !== 'true' || !await enabled()) return finish('Pilot is disabled; no merge attempted');
  if (!accessAudit) throw new Error('Missing read-only repository access audit credential; no merge permitted');
  if (await tip() !== context.sha) return finish('Main moved since this workflow was selected; wait for a fresh run');
  if (!await protectedMain()) throw new Error('Required main protection is missing; no merge permitted');

  if (!await trustedWriters()) throw new Error('Repository writers changed; Redline single-owner pilot is paused');

  const prs = await github.paginate(github.rest.pulls.list,{...repo,state:'open',base:'main',sort:'created',direction:'asc',per_page:100});
  for (const listed of prs) {
    if (listed.draft || held(listed) || listed.head?.repo?.full_name !== 'croutoncreations/redline') continue;
    const number = listed.number;
    if (!Number.isSafeInteger(number) || number <= 0) throw new Error('Invalid PR number');
    const {data:pr} = await github.rest.pulls.get({...repo,pull_number:number});
    if (pr.state !== 'open' || pr.draft || held(pr) || pr.base.ref !== 'main' ||
        pr.head.repo?.full_name !== 'croutoncreations/redline' || !sha(pr.head.sha) || !sha(pr.base.sha)) continue;
    // The PR-recorded base SHA can be old even when main moved. Compare ancestry
    // against the ACTUAL main tip: green head CI must include that exact commit.
    const {data:comparison} = await github.rest.repos.compareCommitsWithBasehead({...repo,basehead:`${context.sha}...${pr.head.sha}`});
    if (comparison.behind_by !== 0 || comparison.merge_base_commit?.sha !== context.sha ||
        !['ahead','identical'].includes(comparison.status)) {
      report.evaluations.push({pr:number,state:'needs_main_refresh',head:pr.head.sha});
      continue;
    }
    const result = JSON.parse(run(`${process.env.RUNNER_TEMP}/green-lane-check`,[],{
      encoding:'utf8',timeout:180000,maxBuffer:2*1024*1024,
      env:{...process.env,GREEN_LANE_ACCESS_AUDIT_TOKEN:'',REPO_OWNER:repo.owner,REPO_NAME:repo.repo,PR_NUMBER:String(number),
        EXPECTED_HEAD_SHA:pr.head.sha,EXPECTED_BASE_SHA:pr.base.sha,CONFIG_PATH:'',LIST_OPEN_PRS:'',REQUIRE_AUTOMATED_REVIEW:'true',
        DUPLICATE_SNAPSHOT_PATH:'',REPRO_PASS:'',REPRO_INCONCLUSIVE:'',REPRO_HEAD_SHA:'',REPRO_BASE_SHA:''}
    }));
    report.evaluations.push(result);
    await write(report);
    if (result.repository !== 'croutoncreations/redline' || result.pr_number !== number ||
        result.head_sha !== pr.head.sha || result.base_sha !== pr.base.sha || result.base_branch !== 'main' ||
        result.verdict !== 'would_merge' || !Array.isArray(result.rules) || result.rules.length === 0 ||
        result.rules.some(rule => rule.pass !== true || rule.inconclusive)) continue;
    if (!await validate({github,context,result,expectedHead:pr.head.sha})) continue;
    // Recheck mutable metadata after all review/check pagination has finished.
    const {data:fresh} = await github.rest.pulls.get({...repo,pull_number:number});
    if (fresh.head.sha !== result.head_sha || fresh.base.sha !== result.base_sha || fresh.base.ref !== 'main' ||
        fresh.updated_at !== result.pr_updated_at || fresh.state !== 'open' || fresh.draft || held(fresh) ||
        fresh.mergeable !== true || fresh.head.repo?.full_name !== 'croutoncreations/redline') continue;
    if (!await protectedMain()) throw new Error('Main protection changed during evaluation');
    if (!await trustedWriters()) throw new Error('Repository writer permissions changed during evaluation');
    if (await tip() !== context.sha) return finish('Main moved during evaluation; fresh evidence required');
    // Re-read live workflow state; disabling it also stops an in-flight run here.
    if (!await enabled()) return finish('Kill switch was turned off during evaluation');
    const age = Date.now() - Date.parse(result.evaluated_at);
    if (!Number.isFinite(age) || age < -60000 || age > 300000) continue;
    report.state='merge_requested'; report.attempt={pr:number,head:result.head_sha,main_tip:context.sha};
    await write(report);
    // Disable client retries in the workflow. Never try a second PR after this
    // call, including timeouts where GitHub may already have merged the first.
    const {data:merged} = await github.rest.pulls.merge({...repo,pull_number:number,sha:result.head_sha,merge_method:'squash'});
    if (merged.merged !== true || !sha(merged.sha)) throw new Error('GitHub did not confirm the exact-head squash merge');
    report.state='merged'; report.merged.push({pr:number,head:result.head_sha,commit:merged.sha});
    await write(report);
    // GITHUB_TOKEN merges suppress push-triggered workflows. Explicit dispatch
    // is the supported exception; no extra credential or merge retry is needed.
    await github.rest.actions.createWorkflowDispatch({...repo,workflow_id:'ci.yml',ref:'main'});
    report.ci_dispatched=true;
    return finish(`Merged PR #${number} at ${merged.sha}; one-merge run complete`);
  }
  return finish('No current-main, fully qualified PR; no merge attempted');
};
