# Security policy

SelfDeploy runs with access to the Docker socket, so security issues matter a lot.

## Reporting a vulnerability

Please **do not open a public issue**. Report it privately through
[GitHub security advisories](https://github.com/99proteam/SelfDeploy/security/advisories/new).
Include the version (`docker exec selfdeploy selfdeploy version`), steps to reproduce and the impact.
You should get a reply within a few days.

## Hardening tips

- Set **Settings → Dashboard domain** so the dashboard is served over HTTPS, then block port 8000 in your firewall.
- Create the admin account right after installing: until then, anyone who reaches port 8000 can claim it.
- Keep preview deployments for trusted repositories. Pull requests from forks are never built.
- Re-run the installer regularly to get updates.
