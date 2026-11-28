const {execFileSync} = require('node:child_process');
const fs = require('node:fs');
const {snapshotMatches} = require('./publish.cjs');

const checks = ['actionlint','go','homebrew','dashboard','linux-smoke','windows-smoke','macos','macos-intel'];
const sha = value => /^[a-f0-9]{40}$/.test(value || '');
const held = pr => pr.labels?.some(label => ['green-lane-hold','maintenance:skip','do-not-touch'].includes(label.name));
// GitHub 5xx responses, dropped connections and timeouts before any merge call
// are transient: the run defers without merging and the next run starts fresh.
// 4xx (auth, permissions, missing resources) stays a hard failure.
const transientText = /GitHub API error 5\d\d|\b5\d\d (Server Error|Bad Gateway|Service Unavailable|Gateway Time-?out)\b|fetch failed|ECONNRESET|ETIMEDOUT|ECONNREFUSED|EAI_AGAIN|socket hang up|other side closed|context deadline exceeded|Client\.Timeout|TLS handshake timeout|connection reset by peer|unexpected EOF|stream error/i;
const transient = err => {
  // Octokit errors carry the HTTP status; execFileSync errors carry an exit
  // code (< 100) in the same field, so classify those by evaluator stderr.
  if (Number.isInteger(err?.status) && err.status >= 100) return err.status >= 500 && err.status <= 599;
  if (err?.signal === 'SIGTERM' || err?.code === 'ETIMEDOUT') return true; // evaluator exceeded its time budget
  return transientText.test(`${err?.stderr ?? ''}\n${err?.message ?? ''}`);
};

// Only trusted default-branch code calls this entry point. Shadow checks and
// downloaded artifacts are never inputs. A failed/ambiguous merge is not retried.
const entryQuery = `query($owner:String!,$repo:String!,$n:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$n){id isInMergeQueue headRefOid mergeQueueEntry{id enqueuer{__typename login}}}}}`;
// Queue mode enqueues as the Green Lane Merge App (green-lane-merge[bot]).
// GraphQL reports bot logins with or without the [bot] suffix depending on the
// field; the Bot type is what a person can't spoof.
const QUEUE_BOT_LOGINS = ['green-lane-merge','green-lane-merge[bot]'];
const queuedByGreenLane = entry => entry?.enqueuer?.__typename === 'Bot' && QUEUE_BOT_LOGINS.includes(entry.enqueuer.login);

async function live({github, accessAudit, queueClient, context, core, run=execFileSync,
  validate=snapshotMatches, write=r=>fs.writeFileSync('live-result.json',JSON.stringify(r,null,2))}) {
  // GREEN_LANE_MERGE_QUEUE=true: main requires GitHub's merge queue. PRs may
  // be behind main (the queue tests each one combined with the latest main),
  // and the worker enqueues the exact evaluated head instead of merging.
  const queue = process.env.GREEN_LANE_MERGE_QUEUE === 'true';
  const report = {state:'deferred',mode:queue?'merge_queue':'direct',main_tip:context.sha,merged:[],enqueued:[],evaluations:[]};
  const finish = async reason => { report.reason=reason; await write(report); core.info(reason); return report; };
  const repo = context.repo;
  if (`${repo.owner}/${repo.repo}` !== 'croutoncreations/redline' || context.ref !== 'refs/heads/main' ||
      !['schedule','workflow_dispatch','workflow_run'].includes(context.eventName) || !sha(context.sha)) {
    throw new Error('Live pilot is restricted to trusted Redline main schedule/dispatch/CI completion');
  }
  // Set immediately before the merge API call. From then on no error counts as
  // transient: a timed-out merge may have succeeded, so it fails loudly and is
  // never retried.
  let mergeCalled = false; // also set before the enqueue call
  try {
    return await select();
  } catch (err) {
    if (mergeCalled || !transient(err)) throw err;
    const cause = String(err?.stderr || err?.message || err).split('\n').map(l => l.trim()).filter(Boolean).at(-1) ?? 'unknown';
    report.state = 'deferred_transient';
    report.transient_error = cause.slice(0, 500);
    core.warning(`Green Lane deferred after a transient GitHub error before any merge call: ${report.transient_error}`);
    return finish('Transient GitHub error before any merge call; no merge attempted. The next run re-evaluates from scratch.');
  }

  async function select() {
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
    // Queue mode also needs green-lane-queue required: it re-decides each
    // queued PR at merge time, so authorization holds until the merge.
    const required = queue ? [...checks,'green-lane-queue'] : checks;
    return branch.protected === true && branch.protection?.enabled === true &&
      p?.enforcement_level === 'everyone' && required.every(name =>
        p.checks?.some(c => c.context === name && c.app_id === 15368));
  };
  // Queue mode needs the queue to exist: otherwise an enqueue request fails,
  // and if the queue were removed later, nothing would test the combination.
  // The queue gate's guarantees assume a squash queue that builds one entry
  // at a time and only merges entries whose own checks passed (ALLGREEN).
  const queueReady = async () => {
    const r = await github.graphql(`query($owner:String!,$repo:String!){repository(owner:$owner,name:$repo){mergeQueue(branch:"main"){id configuration{mergeMethod mergingStrategy maximumEntriesToBuild}}}}`,{owner:repo.owner,repo:repo.repo});
    const q = r?.repository?.mergeQueue, c = q?.configuration;
    return typeof q?.id === 'string' && c?.mergeMethod === 'SQUASH' && c?.mergingStrategy === 'ALLGREEN' && c?.maximumEntriesToBuild === 1;
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
  // Queue entries made with GITHUB_TOKEN start no merge_group CI; only the App can enqueue.
  if (queue && typeof queueClient?.graphql !== 'function') throw new Error('Merge-queue mode needs the Green Lane Merge App token; no merge permitted');
  if (queue && !await queueReady()) throw new Error('Merge-queue mode is on but main has no merge queue configured as squash, ALLGREEN, one entry at a time; no merge permitted');

  if (!await trustedWriters()) throw new Error('Repository writers changed; Redline single-owner pilot is paused');

  const prs = await github.paginate(github.rest.pulls.list,{...repo,state:'open',base:'main',sort:'created',direction:'asc',per_page:100});
  // Queue mode: authorization must hold until the queue merges, not just at
  // enqueue. Every run re-evaluates each queued PR this worker enqueued and
  // removes it from the queue if it no longer qualifies (a new review body,
  // an unresolved thread, a hold label, a changed head...).
  if (queue) await revalidateQueued(prs);
  for (const listed of prs) {
    if (listed.draft || held(listed) || listed.head?.repo?.full_name !== 'croutoncreations/redline') continue;
    const number = listed.number;
    if (!Number.isSafeInteger(number) || number <= 0) throw new Error('Invalid PR number');
    const {data:pr} = await github.rest.pulls.get({...repo,pull_number:number});
    if (pr.state !== 'open' || pr.draft || held(pr) || pr.base.ref !== 'main' ||
        pr.head.repo?.full_name !== 'croutoncreations/redline' || !sha(pr.head.sha) || !sha(pr.base.sha)) continue;
    if (queue) {
      // Already queued (by us earlier, or by Jon): the queue owns it now.
      const q = await github.graphql(`query($owner:String!,$repo:String!,$n:Int!){repository(owner:$owner,name:$repo){pullRequest(number:$n){isInMergeQueue}}}`,{owner:repo.owner,repo:repo.repo,n:number});
      if (q?.repository?.pullRequest?.isInMergeQueue !== false) {
        report.evaluations.push({pr:number,state:'already_queued',head:pr.head.sha});
        continue;
      }
    } else {
      // The PR-recorded base SHA can be old even when main moved. Compare ancestry
      // against the ACTUAL main tip: green head CI must include that exact commit.
      const {data:comparison} = await github.rest.repos.compareCommitsWithBasehead({...repo,basehead:`${context.sha}...${pr.head.sha}`});
      if (comparison.behind_by !== 0 || comparison.merge_base_commit?.sha !== context.sha ||
          !['ahead','identical'].includes(comparison.status)) {
        report.evaluations.push({pr:number,state:'needs_main_refresh',head:pr.head.sha});
        continue;
      }
    }
    const result = await evaluate(number, pr.head.sha, pr.base.sha);
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
    // In queue mode main may move freely: the queue retests against its tip.
    if (!queue && await tip() !== context.sha) return finish('Main moved during evaluation; fresh evidence required');
    if (queue && !await queueReady()) throw new Error('Merge queue disappeared during evaluation; no merge permitted');
    // Re-read live workflow state; disabling it also stops an in-flight run here.
    if (!await enabled()) return finish('Kill switch was turned off during evaluation');
    const age = Date.now() - Date.parse(result.evaluated_at);
    if (!Number.isFinite(age) || age < -60000 || age > 300000) continue;
    report.state=queue?'enqueue_requested':'merge_requested'; report.attempt={pr:number,head:result.head_sha,main_tip:context.sha};
    await write(report);
    if (queue) {
      // expectedHeadOid pins the exact evaluated head: GitHub refuses the
      // request if the branch moved. Never retried, like a merge call.
      mergeCalled = true;
      // Verify through the entry's pull request: headCommit is the queue's
      // own (possibly not yet built) merge-group commit, not the PR head.
      const r = await queueClient.graphql(`mutation($id:ID!,$head:GitObjectID!){enqueuePullRequest(input:{pullRequestId:$id,expectedHeadOid:$head}){mergeQueueEntry{id enqueuer{__typename login} pullRequest{number headRefOid}}}}`,{id:fresh.node_id,head:result.head_sha});
      const entry = r?.enqueuePullRequest?.mergeQueueEntry;
      if (typeof entry?.id !== 'string' || entry.pullRequest?.number !== number || entry.pullRequest?.headRefOid !== result.head_sha || !queuedByGreenLane(entry)) throw new Error('GitHub did not confirm the exact-head merge-queue entry');
      report.state='enqueued'; report.enqueued.push({pr:number,head:result.head_sha,entry:entry.id});
      // The queue merges later, as github-merge-queue[bot], once the required
      // checks pass on its merge-group commit. That includes green-lane-queue
      // (ci.yml), which re-runs this evaluation and Main Drift at merge time.
      return finish(`Enqueued PR #${number} at ${result.head_sha}; one-enqueue run complete`);
    }
    // Disable client retries in the workflow. Never try a second PR after this
    // call, including timeouts where GitHub may already have merged the first.
    mergeCalled = true;
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
  return finish(queue ? 'No fully qualified PR; nothing enqueued' : 'No current-main, fully qualified PR; no merge attempted');

  async function evaluate(number, head, base) {
    return JSON.parse(run(`${process.env.RUNNER_TEMP}/green-lane-check`,[],{
      encoding:'utf8',timeout:180000,maxBuffer:2*1024*1024,
      env:{...process.env,GREEN_LANE_ACCESS_AUDIT_TOKEN:'',REPO_OWNER:repo.owner,REPO_NAME:repo.repo,PR_NUMBER:String(number),
        EXPECTED_HEAD_SHA:head,EXPECTED_BASE_SHA:base,CONFIG_PATH:'',LIST_OPEN_PRS:'',REQUIRE_AUTOMATED_REVIEW:'true',MERGE_QUEUE:queue?'true':'',
        DUPLICATE_SNAPSHOT_PATH:'',REPRO_PASS:'',REPRO_INCONCLUSIVE:'',REPRO_HEAD_SHA:'',REPRO_BASE_SHA:''}
    }));
  }

  function sweepError(pr, step, error) {
    const message = String(error?.message ?? error).slice(0,300);
    (report.sweep_errors ??= []).push({pr,step,error:message});
    core.warning(`Queue sweep could not ${step} PR #${pr}: ${message}`);
  }
  async function revalidateQueued(open) {
    report.dequeued = [];
    for (const listed of open) {
      const number = listed.number;
      if (!Number.isSafeInteger(number) || number <= 0) throw new Error('Invalid PR number');
      // A failed read can't prove who owns the entry, so it is recorded and
      // the entry left alone (the required green-lane-queue gate still
      // decides its merge); the sweep moves on to the next PR.
      let node;
      try { node = (await github.graphql(entryQuery,{owner:repo.owner,repo:repo.repo,n:number}))?.repository?.pullRequest; }
      catch (error) { sweepError(number, 'read', error); continue; }
      if (node?.isInMergeQueue !== true) continue;
      // Only PRs this worker queued; a PR Jon queued by hand is his call.
      if (!queuedByGreenLane(node.mergeQueueEntry)) continue;
      let reason = null;
      // A failure re-checking one entry must not stop the sweep, and an entry
      // that can't be re-checked is removed (the safe direction).
      try {
        const {data:pr} = await github.rest.pulls.get({...repo,pull_number:number});
        if (held(pr)) reason = 'hold label';
        else if (pr.state !== 'open' || pr.draft || pr.head.sha !== node.headRefOid) reason = 'PR state or head changed';
        else {
          const result = await evaluate(number, pr.head.sha, pr.base.sha);
          const failed = (result.rules ?? []).filter(rule => rule.pass !== true || rule.inconclusive);
          // PR State can fail for queued PRs only because GitHub reports them as
          // not cleanly mergeable while queued; every other rule must still pass.
          const blocking = failed.filter(rule => !(rule.name === 'PR State' && /mergeab/i.test(rule.message ?? '')));
          if (result.pr_number !== number || result.head_sha !== pr.head.sha || !Array.isArray(result.rules) || result.rules.length === 0 || blocking.length)
            reason = blocking.map(rule => `${rule.name}: ${rule.message}`).join('; ') || 'evaluation did not match the queued PR';
        }
      } catch (error) {
        reason = `re-check failed: ${String(error?.message ?? error).slice(0,300)}`;
      }
      if (!reason) continue;
      // The evaluation can take minutes. Jon may have removed and re-queued the
      // PR by hand meanwhile; leave his entry alone. The mutation takes the PR
      // id, not the entry id, so re-read the entry just before removing it.
      try {
        const again = (await github.graphql(entryQuery,{owner:repo.owner,repo:repo.repo,n:number}))?.repository?.pullRequest;
        if (again?.isInMergeQueue !== true || again.mergeQueueEntry?.id !== node.mergeQueueEntry.id || !queuedByGreenLane(again.mergeQueueEntry)) continue;
        // Dequeue is the safe direction, so it is not gated on the kill switch.
        await queueClient.graphql(`mutation($id:ID!){dequeuePullRequest(input:{id:$id}){mergeQueueEntry{id}}}`,{id:node.id});
      } catch (error) { sweepError(number, 'dequeue', error); continue; }
      report.dequeued.push({pr:number,head:node.headRefOid,reason:String(reason).slice(0,500)});
      await write(report);
      core.warning(`Removed PR #${number} from the merge queue: ${reason}`);
    }
  }
  }
}
module.exports = Object.assign(live, {transient});
