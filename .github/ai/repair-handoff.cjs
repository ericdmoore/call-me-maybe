// A successful git push can precede the PR API's updated head. Only wait for
// the pre-repair SHA to catch up; an unrelated commit is a real stale run.
module.exports = async function repairHandoff({github, context, core, sha, env = process.env,
  sleep = ms => new Promise(resolve => setTimeout(resolve, ms))}) {
  const number = Number(env.PR_NUMBER);
  const previous = env.REPAIR_HEAD_SHA;
  const ref = env.REPAIR_HEAD_REF;
  if (!Number.isSafeInteger(number) || number < 1 || !/^[a-f0-9]{40}$/.test(sha || '') ||
      !/^[a-f0-9]{40}$/.test(previous || '') || sha === previous || !ref?.startsWith('refs/heads/') || ref === 'refs/heads/') {
    throw new Error('Invalid repair handoff identity');
  }
  const branch = ref.slice('refs/heads/'.length);
  for (let attempt = 0; attempt < 6; attempt++) {
    const {data: pr} = await github.rest.pulls.get({...context.repo, pull_number: number});
    if (pr.state !== 'open' || pr.draft || pr.head.repo?.full_name !== `${context.repo.owner}/${context.repo.repo}` ||
        pr.head.ref !== branch) {
      throw new Error('PR no longer eligible after repair push; no follow-up validation queued');
    }
    const {data: remote} = await github.rest.git.getRef({...context.repo, ref: `heads/${branch}`});
    const heads = [pr.head.sha, remote.object.sha];
    if (heads.some(head => head !== sha && head !== previous)) {
      throw new Error(`Head changed after repair push (expected ${sha}, PR ${pr.head.sha}, branch ${remote.object.sha}); no claim of CI coverage`);
    }
    if (heads.every(head => head === sha)) return pr;
    if (attempt < 5) {
      core.info(`Waiting for GitHub to report repair ${sha} (PR ${pr.head.sha}, branch ${remote.object.sha})`);
      await sleep(2000);
    }
  }
  throw new Error(`Repair ${sha} was pushed, but GitHub still reports the pre-repair head after bounded retries; follow-up validation was not queued`);
};
