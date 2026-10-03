module.exports = async function background({github, context, core, pr, env = process.env}) {
const repo = context.repo;
const number = pr.number;
const readMarkdown = async (path, ref) => {
  const {data} = await github.rest.repos.getContent({...repo, path, ref});
  if (Array.isArray(data) || data.type !== 'file' || data.encoding !== 'base64') {
    throw new Error(`Cannot read Markdown context: ${path}`);
  }
  if (data.size > 24000) throw new Error(`Context file exceeds 24 KB: ${path}`);
  return Buffer.from(data.content, 'base64').toString('utf8');
};
const policy = await readMarkdown('docs/ai-review-intent.md', env.POLICY_SHA);
const result = await github.graphql(`query($owner:String!, $repo:String!, $number:Int!) {
  repository(owner:$owner, name:$repo) { pullRequest(number:$number) {
    closingIssuesReferences(first:10) {
      nodes { number title body url author { login } }
      pageInfo { hasNextPage }
    }
  } }
}`, {...repo, number});
const issues = result.repository.pullRequest.closingIssuesReferences;
if (issues.pageInfo.hasNextPage) throw new Error('More than 10 closing issues; narrow the review context');
const heading = `${policy}\n\nPR #${number}: ${pr.title}\nBase: ${pr.base.sha}\nHead: ${pr.head.sha}`;
const sources = [{label: 'Implementer description (not human confirmation)', text: pr.body || '(none)'}];
for (const issue of issues.nodes) {
  sources.push({label: `Linked issue #${issue.number} by ${issue.author?.login || 'unknown'}: ${issue.title}\n${issue.url}`, text: issue.body});
}
if (!issues.nodes.length) sources.push({label: 'Requirement limitation', text: 'No closing issue linked; requirement evidence is limited.'});
// Only repository-relative Markdown links; never fetch arbitrary URLs.
const paths = [...new Set([...((pr.body || '').matchAll(/\]\(((?:\.plans|docs)\/[^\s)#]+\.md)(?:#[^\s)]*)?\)/g))].map(m => m[1]))];
if (paths.length > 4) throw new Error('More than four design links; narrow the review context');
for (const path of paths) {
  if (path.split('/').includes('..')) throw new Error('Design path escapes its directory');
  sources.push({label: `Established design: ${path} at ${pr.base.sha}`, text: await readMarkdown(path, pr.base.sha)});
}
// OCR v1.12.11 has an 8,000-character background ceiling. Preserve
// every source label; explicitly mark excerpts rather than dropping sources.
const overhead = Array.from(heading + sources.map(s => s.label).join('')).length;
const allowance = Math.floor((7800 - overhead) / sources.length) - 100;
if (allowance < 100) throw new Error('Too many/long context references; narrow the PR');
const excerpt = text => {
  const chars = Array.from(text || '');
  return chars.length <= allowance ? text : chars.slice(0, allowance).join('') +
    '\n[EXCERPT: source continues; do not claim complete requirement coverage.]';
};
const background = [heading, ...sources.map(s => `${s.label}\n${excerpt(s.text)}`)].join('\n\n---\n\n');
if (Array.from(background).length > 8000) throw new Error('Background exceeds OCR limit');
return background;
};
