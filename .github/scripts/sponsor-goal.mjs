// Reads the public GitHub Sponsors goal progress and writes a shields.io endpoint badge
// (https://shields.io/badges/endpoint-badge) that the READMEs of all 99proteam projects show.
// Runs from .github/workflows/sponsor-goal.yml; needs no token.
import { readFile, writeFile } from 'node:fs/promises';

const USER = '99proteam';
const OUT = new URL('../badges/sponsor-goal.json', import.meta.url);

const html = await fetch(`https://github.com/sponsors/${USER}`, {
  headers: { 'user-agent': 'sponsor-goal-badge (github.com/99proteam/SelfDeploy)' },
}).then((r) => {
  if (!r.ok) throw new Error(`GitHub returned ${r.status}`);
  return r.text();
});

const match = /([\d.]+)%\s*<\/span>\s*towards\s*<span[^>]*>\s*\$([\d,]+) per month/.exec(html);
if (!match) throw new Error('Could not find the sponsorship goal on the Sponsors page');
const [, percent, target] = match;

const badge = {
  schemaVersion: 1,
  label: 'monthly goal',
  message: `${percent}% of $${target}`,
  color: 'db61a2',
  namedLogo: 'githubsponsors',
  logoColor: 'white',
};
const next = JSON.stringify(badge, null, 2) + '\n';
const previous = await readFile(OUT, 'utf8').catch(() => '');
if (next !== previous) await writeFile(OUT, next);
console.log(`${percent}% towards $${target} per month${next === previous ? ' (unchanged)' : ''}`);
