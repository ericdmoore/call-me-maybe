const {route} = require('./models.cjs');
module.exports = async function authorize({github, context, core, env = process.env}) {
  if (!env.POLICY_SHA || !env.DEFAULT_MODEL) throw new Error('POLICY_SHA and DEFAULT_MODEL are required');
  const repo = context.repo;
  const number = Number(env.PR_NUMBER);
  if (!Number.isSafeInteger(number) || number < 1) throw new Error('Invalid PR number');
  const {data: pr} = await github.rest.pulls.get({...repo, pull_number: number});
  if (pr.state !== 'open' || pr.draft || pr.head.repo?.full_name !== `${repo.owner}/${repo.repo}`) {
    throw new Error('Only open, ready, same-repository PRs may use Alpaca');
  }
  if (context.payload.pull_request && context.payload.pull_request.head.sha !== pr.head.sha) {
    throw new Error('Stale event: PR head changed');
  }
  if (env.EXPECTED_HEAD && env.EXPECTED_HEAD !== pr.head.sha) throw new Error('Stale requested head');
  let model = env.DEFAULT_MODEL;
  if (env.MODE === 'repair') {
    const id = Number(env.COMMENT_ID);
    if (!Number.isSafeInteger(id) || id < 1) throw new Error('Invalid comment ID');
    const inline = env.COMMENT_KIND === 'inline';
    if (!inline && env.COMMENT_KIND !== 'issue') throw new Error('Invalid comment kind');
    const {data: comment} = await (inline ? github.rest.pulls.getReviewComment : github.rest.issues.getComment)({...repo, comment_id: id});
    const target = inline ? comment.pull_request_url : comment.issue_url;
    if (!target?.endsWith(`/${number}`) || comment.user.type !== 'User' || !/^\/oc(?:\s|$)/.test(comment.body)) {
      throw new Error('Not a human /oc request on this PR');
    }
    const {data: permission} = await github.rest.repos.getCollaboratorPermissionLevel({...repo, username: comment.user.login});
    if (!['admin', 'maintain', 'write'].includes(permission.permission)) throw new Error('Repository write access required');
    if (inline && comment.commit_id !== pr.head.sha) throw new Error('Inline request is on an outdated head; request again on the current PR');
    const override = comment.body.match(/^\/oc --model (\S+)(?:\s|$)/);
    if (comment.body.startsWith('/oc --model') && !override) throw new Error('Missing exact model ID');
    model = override?.[1] || model;
    route(model); // Validate before claiming the request.
    const marker = `<!-- cmm-oc-request:${env.COMMENT_KIND}:${id} -->`;
    const history = await github.paginate(github.rest.issues.listComments, {...repo, issue_number: number, per_page: 100});
    if (history.some(c => c.user.login === 'github-actions[bot]' && c.body?.includes(marker))) {
      throw new Error('This request already had its one pass; post a new /oc request');
    }
    core.setOutput('request', comment.body);
    core.setOutput('source', comment.html_url);
    core.setOutput('marker', marker);
  }
  const selected = route(model);
  for (const [key, value] of Object.entries({number, head: pr.head.sha, base: pr.base.sha,
    ref: `refs/heads/${pr.head.ref}`, branch: pr.head.ref, model, provider: selected.provider,
    model_id: selected.model, url: selected.url})) core.setOutput(key, value);
  return pr;
};
