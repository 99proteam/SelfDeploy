// SelfDeploy dashboard: a small dependency-free single-page app.
'use strict';

const $app = document.getElementById('app');
const state = { user: null, version: '', timers: [] };

// ---------- utilities ----------

const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));

async function api(method, path, body) {
  const opt = { method, headers: { 'X-SelfDeploy': '1' }, credentials: 'same-origin' };
  if (body !== undefined) {
    opt.headers['Content-Type'] = 'application/json';
    opt.body = JSON.stringify(body);
  }
  const res = await fetch(path, opt);
  if (res.status === 401 && !path.startsWith('/api/auth/')) {
    state.user = null;
    location.hash = '#/login';
    throw new Error('Please log in');
  }
  if (res.status === 204) return null;
  const data = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

function toast(msg, isErr) {
  const el = document.createElement('div');
  el.className = 'toast' + (isErr ? ' err' : '');
  el.textContent = msg;
  document.getElementById('toast').appendChild(el);
  setTimeout(() => el.remove(), isErr ? 7000 : 3500);
}

const fail = (e) => toast(e.message || String(e), true);

function every(ms, fn) {
  const id = setInterval(fn, ms);
  state.timers.push(id);
}

function ago(ts) {
  if (!ts) return '';
  const s = Math.max(0, (Date.now() - new Date(ts).getTime()) / 1000);
  if (s < 60) return 'just now';
  if (s < 3600) return Math.floor(s / 60) + 'm ago';
  if (s < 86400) return Math.floor(s / 3600) + 'h ago';
  return Math.floor(s / 86400) + 'd ago';
}

function bytes(n) {
  if (!n) return '0 B';
  const u = ['B', 'KB', 'MB', 'GB', 'TB'];
  const i = Math.min(u.length - 1, Math.floor(Math.log(n) / Math.log(1024)));
  return (n / 1024 ** i).toFixed(i ? 1 : 0) + ' ' + u[i];
}

function badge(status) {
  const map = { running: 'ok', success: 'ok', deploying: 'warn', queued: 'warn', restarting: 'warn', created: '', stopped: '', exited: 'err', failed: 'err', error: 'err' };
  return `<span class="badge ${map[status] ?? ''}">${esc(status || 'unknown')}</span>`;
}

function copyBox(text) {
  return `<div class="copy"><code>${esc(text)}</code><button class="sm" type="button" data-copy="${esc(text)}">Copy</button></div>`;
}

document.addEventListener('click', (e) => {
  const c = e.target.closest('[data-copy]');
  if (c) {
    navigator.clipboard.writeText(c.dataset.copy).then(() => toast('Copied'), () => toast('Copy failed', true));
  }
});

// formData reads a form into an object: checkboxes become booleans and
// inputs with data-num become numbers.
function formData(form) {
  const out = {};
  for (const el of form.elements) {
    if (!el.name) continue;
    if (el.type === 'checkbox') out[el.name] = el.checked;
    else if (el.type === 'radio') { if (el.checked) out[el.name] = el.value; }
    else if (el.dataset.num !== undefined || el.name.endsWith('_id')) out[el.name] = el.value === '' ? 0 : Number(el.value);
    else out[el.name] = el.value;
  }
  return out;
}

function onSubmit(form, fn) {
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const btn = form.querySelector('button[type=submit]');
    if (btn) btn.disabled = true;
    try { await fn(formData(form)); } catch (err) { fail(err); } finally { if (btn) btn.disabled = false; }
  });
}

function bind(root, sel, fn) {
  root.querySelectorAll(sel).forEach((el) => el.addEventListener('click', async (e) => {
    e.preventDefault();
    el.disabled = true;
    try { await fn(el, e); } catch (err) { fail(err); } finally { el.disabled = false; }
  }));
}

function field(label, input, hint) {
  return `<div class="field"><label>${label}</label>${input}${hint ? `<div class="hint">${hint}</div>` : ''}</div>`;
}

function input(name, value, attrs = '') {
  return `<input name="${name}" value="${esc(value)}" ${attrs}>`;
}

function select(name, options, value) {
  return `<select name="${name}">${options.map(([v, l]) => `<option value="${esc(v)}" ${String(v) === String(value) ? 'selected' : ''}>${esc(l)}</option>`).join('')}</select>`;
}

function modal(html) {
  const bg = document.createElement('div');
  bg.className = 'modal-bg';
  bg.innerHTML = `<div class="modal">${html}</div>`;
  bg.addEventListener('click', (e) => { if (e.target === bg) bg.remove(); });
  document.body.appendChild(bg);
  bg.close = () => bg.remove();
  bg.querySelectorAll('[data-close]').forEach((b) => b.addEventListener('click', bg.close));
  return bg;
}

function confirmDanger(text, extra = '') {
  return new Promise((resolve) => {
    const m = modal(`<h2>Are you sure?</h2><p>${text}</p>${extra}
      <div class="row" style="justify-content:flex-end;margin-top:16px">
      <button data-close>Cancel</button><button class="primary" id="confirm-yes" style="background:var(--err);border-color:var(--err)">Delete</button></div>`);
    m.querySelector('#confirm-yes').addEventListener('click', () => {
      const checks = {};
      m.querySelectorAll('input[type=checkbox]').forEach((c) => { checks[c.name] = c.checked; });
      m.close();
      resolve(checks);
    });
    m.querySelectorAll('[data-close]').forEach((b) => b.addEventListener('click', () => resolve(null)));
  });
}

// showTask opens a modal that follows a background task's log.
function showTask(taskId, title, onDone) {
  const m = modal(`<h2>${esc(title)}</h2><div id="task-status" style="margin-bottom:10px">${badge('running')}</div>
    <pre class="log" id="task-log">Starting...</pre>
    <div class="row" style="justify-content:flex-end;margin-top:14px"><button data-close>Close</button></div>`);
  const tick = async () => {
    try {
      const t = await api('GET', '/api/tasks/' + taskId);
      const pre = m.querySelector('#task-log');
      if (!pre) return clearInterval(id);
      pre.textContent = t.log || '...';
      pre.scrollTop = pre.scrollHeight;
      m.querySelector('#task-status').innerHTML = badge(t.status) + (t.error ? ` <span class="small">${esc(t.error)}</span>` : '');
      if (t.status !== 'running') {
        clearInterval(id);
        if (onDone) onDone(t);
      }
    } catch (e) { clearInterval(id); }
  };
  const id = setInterval(tick, 1500);
  tick();
}

// ---------- layout ----------

const NAV = [
  ['#/', 'Dashboard'], ['#/projects', 'Projects'], ['#/mail', 'Mail server'], ['#/servers', 'Servers'],
  ['#/notifications', 'Notifications'], ['#/settings', 'Settings'],
];

function shell(content) {
  const h = location.hash || '#/';
  const active = (href) => (href === '#/' ? h === '#/' : h.startsWith(href)) ? 'active' : '';
  return `<div class="topbar"><button id="menu-btn" aria-label="Menu">☰</button><strong>SelfDeploy</strong></div>
  <div class="shell">
    <aside class="sidebar" id="sidebar">
      <a class="brand" href="#/"><img src="/favicon.svg" alt="">SelfDeploy</a>
      <nav class="nav">
        ${NAV.map(([href, label]) => `<a href="${href}" class="${active(href)}">${label}</a>`).join('')}
        <div class="sep"></div>
        <a href="https://github.com/99proteam/selfdeploy" target="_blank" rel="noopener">Docs &amp; GitHub</a>
        <a href="#" id="logout">Log out</a>
      </nav>
      <div class="muted small" style="padding:12px 10px">${esc(state.user?.email || '')}<br>${esc(state.version)}</div>
    </aside>
    <main class="main" id="main">${content}</main>
  </div>`;
}

function mountShell() {
  const sb = document.getElementById('sidebar');
  document.getElementById('menu-btn')?.addEventListener('click', () => sb.classList.toggle('open'));
  sb.querySelectorAll('a').forEach((a) => a.addEventListener('click', () => sb.classList.remove('open')));
  document.getElementById('logout').addEventListener('click', async (e) => {
    e.preventDefault();
    await api('POST', '/api/auth/logout').catch(() => {});
    state.user = null;
    location.hash = '#/login';
  });
}

function pageHead(title, sub, actions = '') {
  return `<div class="page-head"><div><h1>${title}</h1>${sub ? `<div class="muted">${sub}</div>` : ''}</div><div class="row">${actions}</div></div>`;
}

// ---------- router ----------

const routes = [];
const route = (re, fn) => routes.push([re, fn]);

async function render() {
  state.timers.forEach(clearInterval);
  state.timers = [];
  document.querySelectorAll('.modal-bg').forEach((m) => m.remove());
  const hash = location.hash || '#/';
  const [path, query] = hash.slice(1).split('?');
  const params = new URLSearchParams(query || '');

  if (!state.user && path !== '/login' && path !== '/register') {
    location.hash = '#/login';
    return;
  }
  for (const [re, fn] of routes) {
    const m = path.match(re);
    if (!m) continue;
    try {
      await fn(m.slice(1), params);
    } catch (e) {
      if (state.user) {
        $app.innerHTML = shell(`<div class="card"><h2>Something went wrong</h2><p class="muted">${esc(e.message)}</p></div>`);
        mountShell();
      }
    }
    return;
  }
  $app.innerHTML = shell('<div class="card empty">Page not found</div>');
  mountShell();
}

// page renders a page inside the shell and runs its mount function.
function page(html, mount) {
  $app.innerHTML = shell(html);
  mountShell();
  if (mount) mount(document.getElementById('main'));
}

window.addEventListener('hashchange', render);

// ---------- auth ----------

function authPage(title, sub, form) {
  $app.innerHTML = `<div class="auth"><div class="card">
    <div class="brand" style="padding:0 0 12px"><img src="/favicon.svg" alt="">SelfDeploy</div>
    <h1>${title}</h1><p class="muted">${sub}</p>${form}</div></div>`;
}

route(/^\/login$/, async () => {
  const st = await api('GET', '/api/auth/status');
  if (st.setup_required) { location.hash = '#/register'; return; }
  authPage('Log in', 'Welcome back.', `<form id="f">
    ${field('Email', input('email', '', 'type="email" required autocomplete="username"'))}
    ${field('Password', input('password', '', 'type="password" required autocomplete="current-password"'))}
    <button class="primary" type="submit" style="width:100%">Log in</button></form>
    <p class="hint">Forgot your password? Run <code>docker exec selfdeploy selfdeploy reset-password you@example.com</code> on the server.</p>`);
  onSubmit(document.getElementById('f'), async (d) => {
    state.user = await api('POST', '/api/auth/login', d);
    location.hash = '#/';
  });
});

route(/^\/register$/, async () => {
  const st = await api('GET', '/api/auth/status');
  if (!st.setup_required) { location.hash = '#/login'; return; }
  authPage('Create your admin account', 'This is the first account on this SelfDeploy instance.', `<form id="f">
    ${field('Name', input('name', '', 'autocomplete="name"'))}
    ${field('Email', input('email', '', 'type="email" required autocomplete="username"'))}
    ${field('Password', input('password', '', 'type="password" required minlength="8" autocomplete="new-password"'), 'At least 8 characters.')}
    <button class="primary" type="submit" style="width:100%">Create account</button></form>`);
  onSubmit(document.getElementById('f'), async (d) => {
    state.user = await api('POST', '/api/auth/register', d);
    location.hash = '#/';
  });
});

// ---------- dashboard ----------

function meter(label, used, total, fmt) {
  const pct = total ? Math.min(100, (used / total) * 100) : 0;
  return `<div class="stat"><div class="label">${label}</div><div class="value">${total ? Math.round(pct) + '%' : '–'}</div>
    <div class="muted small">${total ? fmt(used) + ' of ' + fmt(total) : ''}</div><div class="meter"><div style="width:${pct}%"></div></div></div>`;
}

function resourceTiles(apps, dbs, svcs) {
  const tiles = [
    ...apps.map((a) => `<a class="tile" href="#/apps/${a.id}"><div class="title">${esc(a.name)} ${badge(a.status)}</div>
      <div class="sub">App · ${esc((a.domains || '').split(',')[0] || a.git_url || a.image)}</div></a>`),
    ...dbs.map((d) => `<a class="tile" href="#/databases/${d.id}"><div class="title">${esc(d.name)} ${badge(d.status)}</div>
      <div class="sub">${esc(d.engine)} ${esc(d.version)}</div></a>`),
    ...svcs.map((s) => `<a class="tile" href="#/services/${s.id}"><div class="title">${esc(s.name)} ${badge(s.status)}</div>
      <div class="sub">${esc(s.template)} · ${esc(s.domain)}</div></a>`),
  ];
  return tiles.length ? `<div class="grid cols-3">${tiles.join('')}</div>` : '';
}

route(/^\/$/, async () => {
  const o = await api('GET', '/api/overview');
  const m = o.metrics;
  const deploys = (o.recent_deployments || []).map((d) => `<tr>
    <td><a href="#/apps/${d.app_id}">${esc(d.app_name)}</a></td><td>${badge(d.status)}</td><td>${esc(d.trigger)}</td>
    <td class="mono">${esc((d.commit_sha || '').slice(0, 7))} <span class="muted">${esc(d.commit_message)}</span></td>
    <td class="muted">${ago(d.created_at)}</td><td><a href="#/deployments/${d.id}">Logs</a></td></tr>`).join('');
  const tiles = resourceTiles(o.apps, o.databases, o.services);
  page(`${pageHead('Dashboard', 'Everything running on your servers.', '<a class="btn primary" href="#/projects">+ New resource</a>')}
    <div class="grid cols-4" style="margin-bottom:16px">
      <div class="stat"><div class="label">Apps</div><div class="value">${o.apps.length}</div><div class="muted small">${o.projects.length} projects</div></div>
      <div class="stat"><div class="label">Databases &amp; services</div><div class="value">${o.databases.length + o.services.length}</div><div class="muted small">${o.servers.length} server(s)</div></div>
      ${meter('Memory', m.mem_used, m.mem_total, bytes)}
      ${meter('Disk', m.disk_used, m.disk_total, bytes)}
    </div>
    <div class="card"><h2>Resources</h2>${tiles || `<div class="empty">Nothing deployed yet. <a href="#/projects">Create a project</a> and deploy your first app from Git, a Docker image or a one-click template.</div>`}</div>
    <div class="card"><h2>Recent deployments</h2>${deploys ? `<div class="table-wrap"><table><thead><tr><th>App</th><th>Status</th><th>Trigger</th><th>Commit</th><th>When</th><th></th></tr></thead><tbody>${deploys}</tbody></table></div>` : '<div class="empty">No deployments yet.</div>'}</div>`);
  every(10000, () => { if (location.hash === '#/' || location.hash === '') render(); });
});

// ---------- projects ----------

route(/^\/projects$/, async () => {
  const list = await api('GET', '/api/projects');
  page(`${pageHead('Projects', 'Group related apps, databases and services.', '<button class="primary" id="new">+ New project</button>')}
    ${list.length ? `<div class="grid cols-3">${list.map((p) => `<a class="tile" href="#/projects/${p.id}"><div class="title">${esc(p.name)}</div><div class="sub">${esc(p.description) || 'No description'}</div></a>`).join('')}</div>`
      : '<div class="card empty">No projects yet. Create one to start deploying.</div>'}`, (root) => {
    bind(root, '#new', () => {
      const m = modal(`<h2>New project</h2><form id="pf">${field('Name', input('name', '', 'required'))}${field('Description', input('description', ''))}
        <div class="row" style="justify-content:flex-end"><button type="button" data-close>Cancel</button><button class="primary" type="submit">Create</button></div></form>`);
      onSubmit(m.querySelector('#pf'), async (d) => {
        const p = await api('POST', '/api/projects', d);
        m.close();
        location.hash = '#/projects/' + p.id;
      });
    });
  });
});

route(/^\/projects\/(\d+)$/, async ([id]) => {
  const d = await api('GET', '/api/projects/' + id);
  const p = d.project;
  const tiles = resourceTiles(d.apps, d.databases, d.services);
  page(`${pageHead(esc(p.name), esc(p.description), `
      <a class="btn primary" href="#/new/app?project=${p.id}">+ App</a>
      <a class="btn" href="#/new/database?project=${p.id}">+ Database</a>
      <a class="btn" href="#/new/service?project=${p.id}">+ One-click service</a>
      <button class="danger" id="del">Delete</button>`)}
    <div class="card">${tiles || '<div class="empty">This project is empty. Add an app from Git or a Docker image, a database, or a one-click service.</div>'}</div>`, (root) => {
    bind(root, '#del', async () => {
      if (!(await confirmDanger(`Delete project <b>${esc(p.name)}</b>? It must be empty.`))) return;
      await api('DELETE', '/api/projects/' + p.id);
      location.hash = '#/projects';
    });
  });
});

// ---------- apps ----------

async function serverOptions() {
  const servers = await api('GET', '/api/servers');
  return servers.map((s) => [s.id, s.name + (s.is_local ? ' (this server)' : '')]);
}

function appForm(a, projects, servers, isNew) {
  const packs = [['auto', 'Auto-detect (Dockerfile → Compose → static → Nixpacks)'], ['dockerfile', 'Dockerfile'], ['nixpacks', 'Nixpacks (Node, Python, Go, PHP, Ruby, Rust, Java …)'], ['static', 'Static site / SPA (nginx)'], ['compose', 'Docker Compose']];
  return `<form id="app-form">
    <div class="grid cols-2">
      ${field('Name', input('name', a.name, 'required'))}
      ${field('Project', select('project_id', projects.map((p) => [p.id, p.name]), a.project_id))}
    </div>
    <div class="grid cols-2">
      ${field('Server', select('server_id', servers, a.server_id))}
      ${field('Source', select('source_type', [['git', 'Git repository'], ['image', 'Docker image']], a.source_type || 'git'))}
    </div>
    <div data-show="git">
      <div class="grid cols-2">
        ${field('Repository URL', input('git_url', a.git_url, 'placeholder="https://github.com/you/app.git"'), 'Public or private. SSH URLs (git@…) use the SelfDeploy SSH key shown under Servers.')}
        ${field('Branch', input('git_branch', a.git_branch || 'main'))}
      </div>
      ${field('Access token (private repos)', input('git_token', '', `type="password" placeholder="${a.has_git_token ? '•••••• saved, leave empty to keep' : 'GitHub/GitLab/Gitea token'}" autocomplete="off"`))}
      <div class="grid cols-2">
        ${field('Build pack', select('build_pack', packs, a.build_pack || 'auto'))}
        ${field('Base directory', input('base_dir', a.base_dir || '.'), 'For monorepos, e.g. apps/web')}
      </div>
      <div class="grid cols-2" data-show-pack="dockerfile auto">${field('Dockerfile path', input('dockerfile_path', a.dockerfile_path || 'Dockerfile'))}<div></div></div>
      <div class="grid cols-3" data-show-pack="nixpacks static auto">
        ${field('Install command', input('install_command', a.install_command, 'placeholder="optional"'))}
        ${field('Build command', input('build_command', a.build_command, 'placeholder="e.g. npm run build"'))}
        ${field('Start command', input('start_command', a.start_command, 'placeholder="optional"'))}
      </div>
      <div data-show-pack="static">${field('Publish directory', input('publish_dir', a.publish_dir || 'dist'), 'Folder with the built site (dist, build, out, public …).')}</div>
      <div class="grid cols-2" data-show-pack="compose">
        ${field('Compose file', input('compose_path', a.compose_path || 'docker-compose.yml'))}
        ${field('Service that gets the domain', input('compose_service', a.compose_service, 'placeholder="e.g. web"'))}
      </div>
    </div>
    <div data-show="image">${field('Docker image', input('image', a.image, 'placeholder="nginx:alpine or ghcr.io/you/app:latest"'))}</div>
    <div class="grid cols-2">
      ${field('Port your app listens on', input('port', a.port || 3000, 'type="number" data-num min="1" max="65535"'), 'SelfDeploy also sets PORT for you.')}
      ${field('Health check path', input('health_path', a.health_path, 'placeholder="/health (optional)"'), 'New versions only go live once this returns 2xx.')}
    </div>
    ${field('Domains', input('domains', a.domains, 'placeholder="https://app.example.com, https://www.example.com"'),
      `Comma separated. HTTPS certificates are issued automatically. ${isNew ? 'Leave empty for a free temporary <b>sslip.io</b> domain.' : ''}`)}
    <div class="grid cols-2">
      ${field('Memory limit', input('memory_limit', a.memory_limit, 'placeholder="e.g. 512m (optional)"'))}
      ${field('CPU limit', input('cpu_limit', a.cpu_limit, 'placeholder="e.g. 1.5 (optional)"'))}
    </div>
    <h3 style="margin-top:8px">Password protection</h3>
    <p class="muted small">Ask visitors for a username and password before they reach the app. Handy for staging sites and previews. Leave the username empty to keep the app public.</p>
    <div class="grid cols-2">
      ${field('Username', input('basic_auth_user', a.basic_auth_user, 'placeholder="optional" autocomplete="off"'))}
      ${field('Password', input('basic_auth_password', '', `type="password" placeholder="${a.password_protected ? '•••••• saved, leave empty to keep' : 'at least 8 characters'}" autocomplete="new-password"`))}
    </div>
    ${isNew ? `<label class="check"><input type="checkbox" name="deploy" checked> Deploy right away</label>` : `
      <label class="check"><input type="checkbox" name="auto_deploy" ${a.auto_deploy ? 'checked' : ''}> Auto deploy on git push (webhook)</label>
      <label class="check"><input type="checkbox" name="preview_deploys" ${a.preview_deploys ? 'checked' : ''}> Preview deployments for pull requests (pr&lt;N&gt;.your-domain)</label>`}
    <div class="row" style="margin-top:16px"><button class="primary" type="submit">${isNew ? 'Create app' : 'Save changes'}</button></div>
  </form>`;
}

function mountAppForm(root) {
  const form = root.querySelector('#app-form');
  const sync = () => {
    const src = form.source_type.value;
    const pack = form.build_pack.value;
    form.querySelectorAll('[data-show]').forEach((el) => { el.style.display = el.dataset.show === src ? '' : 'none'; });
    form.querySelectorAll('[data-show-pack]').forEach((el) => { el.style.display = el.dataset.showPack.split(' ').includes(pack) ? '' : 'none'; });
  };
  form.source_type.addEventListener('change', sync);
  form.build_pack.addEventListener('change', sync);
  sync();
  return form;
}

route(/^\/new\/app$/, async (_, q) => {
  const [projects, servers] = await Promise.all([api('GET', '/api/projects'), serverOptions()]);
  if (!projects.length) { location.hash = '#/projects'; toast('Create a project first'); return; }
  const a = { project_id: q.get('project') || projects[0].id, server_id: servers[0]?.[0] };
  page(`${pageHead('New app', 'Deploy from a Git repository or a Docker image.')}<div class="card">${appForm(a, projects, servers, true)}</div>`, (root) => {
    onSubmit(mountAppForm(root), async (d) => {
      const res = await api('POST', '/api/apps', d);
      toast(d.deploy ? 'App created, deployment started' : 'App created');
      location.hash = '#/apps/' + res.app.id;
    });
  });
});

const APP_TABS = [['', 'Overview'], ['config', 'Configuration'], ['env', 'Environment'], ['deployments', 'Deployments'], ['logs', 'Logs'], ['webhooks', 'Git & webhooks']];

route(/^\/apps\/(\d+)(?:\/(\w+))?$/, async ([id, tab = '']) => {
  const v = await api('GET', '/api/apps/' + id);
  const a = v.app;
  const tabs = `<div class="tabs">${APP_TABS.map(([t, l]) => `<a href="#/apps/${id}${t ? '/' + t : ''}" class="${t === tab ? 'active' : ''}">${l}</a>`).join('')}</div>`;
  const head = pageHead(`${esc(a.name)} ${badge(a.status)}`, `${a.source_type === 'image' ? esc(a.image) : esc(a.git_url) + ' · ' + esc(a.git_branch)}`, `
    <button class="primary" id="deploy">Deploy</button>
    <button id="restart">Restart</button>
    ${a.status === 'stopped' ? '<button id="start">Start</button>' : '<button id="stop">Stop</button>'}
    <button id="clone">Clone</button>
    <button class="danger" id="delete">Delete</button>`);
  const actions = (root) => {
    bind(root, '#deploy', async () => {
      const d = await api('POST', `/api/apps/${id}/deploy`);
      location.hash = '#/deployments/' + d.id;
    });
    for (const act of ['restart', 'stop', 'start']) {
      bind(root, '#' + act, async () => { await api('POST', `/api/apps/${id}/${act}`); toast(`${act} done`); render(); });
    }
    bind(root, '#clone', () => {
      const m = modal(`<h2>Clone ${esc(a.name)}</h2>
        <p class="muted small">Copies the build settings and environment variables into a new app with its own domain and webhook secret. Useful for staging copies.</p>
        <form id="clone-form">${field('Name', input('name', a.name + ' copy', 'required'))}
          ${field('Project', `<select name="project_id" id="clone-project"><option value="${a.project_id}">Same project</option></select>`)}
          <label class="check"><input type="checkbox" name="deploy" checked> Deploy right away</label>
          <div class="row" style="justify-content:flex-end;margin-top:12px"><button type="button" data-close>Cancel</button><button class="primary" type="submit">Clone app</button></div></form>`);
      api('GET', '/api/projects').then((ps) => {
        const sel = m.querySelector('#clone-project');
        sel.innerHTML = ps.map((p) => `<option value="${p.id}" ${p.id === a.project_id ? 'selected' : ''}>${esc(p.name)}</option>`).join('');
      }).catch(() => {});
      onSubmit(m.querySelector('#clone-form'), async (d) => {
        const res = await api('POST', `/api/apps/${id}/clone`, d);
        m.close();
        toast(d.deploy ? 'App cloned, deployment started' : 'App cloned');
        location.hash = '#/apps/' + res.app.id;
      });
    });
    bind(root, '#delete', async () => {
      const r = await confirmDanger(`Delete <b>${esc(a.name)}</b>, its containers and images?`,
        '<label class="check"><input type="checkbox" name="volumes"> Also delete its volumes (data)</label>');
      if (!r) return;
      await api('DELETE', `/api/apps/${id}${r.volumes ? '?volumes=1' : ''}`);
      location.hash = '#/projects/' + a.project_id;
    });
  };

  if (tab === '') {
    const deps = await api('GET', `/api/apps/${id}/deployments?limit=5`);
    const doms = (a.domains || '').split(',').filter(Boolean);
    page(`${head}${tabs}<div class="grid cols-2">
      <div class="card"><h2>Domains</h2>${doms.length ? doms.map((d) => `<div style="margin-bottom:6px"><a href="${esc(d)}" target="_blank" rel="noopener">${esc(d)}</a></div>`).join('') : '<p class="muted">No domain. Add one under Configuration.</p>'}
        <button class="sm" id="gen">+ Add temporary sslip.io domain</button></div>
      <div class="card"><h2>Details</h2><table>
        <tr><td class="muted">Build pack</td><td>${esc(a.source_type === 'image' ? 'Docker image' : a.build_pack)}</td></tr>
        <tr><td class="muted">Port</td><td>${a.port}</td></tr>
        <tr><td class="muted">Internal host</td><td class="mono">${esc(a.slug)}:${a.port}</td></tr>
        <tr><td class="muted">Current image</td><td class="mono break">${esc(a.current_image || '–')}</td></tr>
        <tr><td class="muted">Auto deploy</td><td>${a.auto_deploy ? 'On' : 'Off'}${a.preview_deploys ? ' · PR previews on' : ''}</td></tr>
        <tr><td class="muted">Access</td><td>${v.password_protected ? `🔒 Password protected (user <b>${esc(a.basic_auth_user)}</b>)` : 'Public'}</td></tr>
      </table></div></div>
      <div class="card"><h2>Latest deployments</h2>${deploymentTable(deps, false)}</div>`, (root) => {
      actions(root);
      bind(root, '#gen', async () => { await api('POST', `/api/apps/${id}/generate-domain`); toast('Domain added, redeploy to apply'); render(); });
      every(5000, async () => { if (deps.some((d) => d.status === 'running' || d.status === 'queued') || a.status === 'deploying') render(); });
    });
  } else if (tab === 'config') {
    const [projects, servers] = await Promise.all([api('GET', '/api/projects'), serverOptions()]);
    page(`${head}${tabs}<div class="card">${appForm({ ...a, has_git_token: v.has_git_token, password_protected: v.password_protected }, projects, servers, false)}</div>`, (root) => {
      actions(root);
      onSubmit(mountAppForm(root), async (d) => {
        d.project_id = Number(d.project_id);
        d.server_id = Number(d.server_id);
        await api('PUT', '/api/apps/' + id, d);
        toast('Saved. Redeploy to apply changes.');
        render();
      });
    });
  } else if (tab === 'env') {
    const vars = await api('GET', `/api/apps/${id}/env`);
    page(`${head}${tabs}<div class="card"><h2>Environment variables</h2>
      <p class="muted small">Available at runtime. Tick "build" to also pass a variable as a build argument. Changes apply on the next deploy.</p>
      <div id="env-rows"></div>
      <div class="row" style="margin-top:10px"><button id="add">+ Add variable</button><button id="paste">Paste .env</button><button class="primary" id="save">Save</button></div></div>`, (root) => {
      actions(root);
      const box = root.querySelector('#env-rows');
      const addRow = (k = '', val = '', b = false) => {
        const row = document.createElement('div');
        row.className = 'row';
        row.style.marginBottom = '6px';
        row.innerHTML = `<input class="grow mono" placeholder="KEY" value="${esc(k)}" style="max-width:260px"><input class="grow mono" placeholder="value" value="${esc(val)}">
          <label class="check small"><input type="checkbox" ${b ? 'checked' : ''}>build</label><button class="sm danger" type="button">✕</button>`;
        row.querySelector('button').addEventListener('click', () => row.remove());
        box.appendChild(row);
      };
      vars.forEach((x) => addRow(x.key, x.value, x.is_build));
      if (!vars.length) addRow();
      bind(root, '#add', () => addRow());
      bind(root, '#paste', () => {
        const m = modal(`<h2>Paste .env</h2><textarea id="envtext" placeholder="KEY=value" style="min-height:220px"></textarea>
          <div class="row" style="justify-content:flex-end;margin-top:12px"><button data-close>Cancel</button><button class="primary" id="imp">Import</button></div>`);
        m.querySelector('#imp').addEventListener('click', () => {
          for (const line of m.querySelector('#envtext').value.split('\n')) {
            const t = line.trim();
            if (!t || t.startsWith('#') || !t.includes('=')) continue;
            const i = t.indexOf('=');
            let val = t.slice(i + 1).trim();
            if (/^(["']).*\1$/.test(val)) val = val.slice(1, -1);
            addRow(t.slice(0, i).replace(/^export\s+/, '').trim(), val);
          }
          m.close();
        });
      });
      bind(root, '#save', async () => {
        const out = [];
        box.querySelectorAll('.row').forEach((r) => {
          const [k, val] = r.querySelectorAll('input:not([type=checkbox])');
          if (k.value.trim()) out.push({ key: k.value.trim(), value: val.value, is_build: r.querySelector('input[type=checkbox]').checked });
        });
        await api('PUT', `/api/apps/${id}/env`, out);
        toast('Variables saved. Redeploy to apply.');
      });
    });
  } else if (tab === 'deployments') {
    const deps = await api('GET', `/api/apps/${id}/deployments?limit=50`);
    page(`${head}${tabs}<div class="card">${deploymentTable(deps, true)}</div>`, (root) => {
      actions(root);
      bind(root, '[data-rollback]', async (el) => {
        const d = await api('POST', `/api/deployments/${el.dataset.rollback}/rollback`);
        location.hash = '#/deployments/' + d.id;
      });
      every(5000, () => { if (deps.some((d) => d.status === 'running' || d.status === 'queued')) render(); });
    });
  } else if (tab === 'logs') {
    page(`${head}${tabs}<div class="card"><div class="row" style="justify-content:space-between;margin-bottom:10px"><h2 style="margin:0">Runtime logs</h2>
      <label class="check small"><input type="checkbox" id="follow" checked> Auto refresh</label></div><pre class="log" id="logs">Loading...</pre></div>`, (root) => {
      actions(root);
      const pre = root.querySelector('#logs');
      const load = async () => {
        if (!root.querySelector('#follow')?.checked && pre.textContent !== 'Loading...') return;
        const r = await api('GET', `/api/apps/${id}/logs?tail=500`).catch((e) => ({ error: e.message }));
        const atBottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 30;
        pre.textContent = r.error ? r.error : (r.logs || 'No output yet.');
        if (atBottom) pre.scrollTop = pre.scrollHeight;
      };
      load();
      every(4000, load);
    });
  } else if (tab === 'webhooks') {
    const w = v.webhooks;
    page(`${head}${tabs}<div class="card"><h2>Automatic deployments</h2>
      <p class="muted">Push to <b>${esc(a.git_branch)}</b> to deploy. Auto deploy is <b>${a.auto_deploy ? 'on' : 'off'}</b>${a.preview_deploys ? ' and pull requests get preview URLs' : ''} (change it under Configuration).</p>
      <h3>Webhook secret</h3>${copyBox(a.webhook_secret)}
      <h3 style="margin-top:18px">GitHub</h3><p class="muted small">Repo → Settings → Webhooks → Add webhook. Content type <b>application/json</b>, paste the secret, select "Pushes" and "Pull requests".</p>${copyBox(w.github)}
      <h3 style="margin-top:18px">GitLab</h3><p class="muted small">Settings → Webhooks. Put the secret in "Secret token", tick Push and Merge request events.</p>${copyBox(w.gitlab)}
      <h3 style="margin-top:18px">Gitea / Forgejo</h3><p class="muted small">Settings → Webhooks → Gitea. Paste the secret, trigger on Push and Pull Request.</p>${copyBox(w.gitea)}
      <h3 style="margin-top:18px">Bitbucket</h3><p class="muted small">Repository settings → Webhooks, trigger "Repository push".</p>${copyBox(w.bitbucket)}
      <h3 style="margin-top:18px">Any CI (GitHub Actions, GitLab CI, cron …)</h3><p class="muted small">POST or GET this URL to deploy. Add <code>&amp;commit=SHA</code> to pin a commit.</p>${copyBox(w.deploy)}
      ${copyBox(`curl -X POST "${w.deploy}"`)}</div>`, actions);
  }
});

function deploymentTable(deps, withRollback) {
  if (!deps.length) return '<div class="empty">No deployments yet.</div>';
  return `<div class="table-wrap"><table><thead><tr><th>#</th><th>Status</th><th>Trigger</th><th>Commit</th><th>When</th><th></th></tr></thead><tbody>
    ${deps.map((d) => `<tr><td><a href="#/deployments/${d.id}">#${d.id}</a></td><td>${badge(d.status)}</td>
      <td>${esc(d.trigger)}${d.pr_number ? ` · PR #${d.pr_number}` : ''}</td>
      <td class="mono">${esc((d.commit_sha || '').slice(0, 7))} <span class="muted">${esc(d.commit_message)}</span></td>
      <td class="muted">${ago(d.created_at)}</td>
      <td style="text-align:right"><a href="#/deployments/${d.id}">Logs</a>
      ${withRollback && d.status === 'success' && d.image && !d.pr_number ? ` · <a href="#" data-rollback="${d.id}">Rollback</a>` : ''}</td></tr>`).join('')}
  </tbody></table></div>`;
}

route(/^\/deployments\/(\d+)$/, async ([id]) => {
  const d = await api('GET', '/api/deployments/' + id);
  page(`${pageHead(`Deployment #${d.id} <span id="dstatus">${badge(d.status)}</span>`, `${esc(d.trigger)} · ${ago(d.created_at)}${d.commit_sha ? ' · ' + esc(d.commit_sha.slice(0, 7)) + ' ' + esc(d.commit_message) : ''}`,
    `<a class="btn" href="#/apps/${d.app_id}">Back to app</a>`)}<div class="card"><pre class="log" id="dlog"></pre></div>`, (root) => {
    const pre = root.querySelector('#dlog');
    const show = (x) => {
      const atBottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 30;
      pre.textContent = x.logs || 'Waiting for the build to start...';
      if (atBottom) pre.scrollTop = pre.scrollHeight;
      root.querySelector('#dstatus').innerHTML = badge(x.status);
    };
    show(d);
    pre.scrollTop = pre.scrollHeight;
    every(1500, async () => {
      const cur = root.querySelector('#dstatus')?.textContent;
      if (cur !== 'running' && cur !== 'queued') return;
      show(await api('GET', '/api/deployments/' + id));
    });
  });
});

// ---------- databases ----------

const ENGINES = [
  ['postgres', 'PostgreSQL', 'The reliable relational database.'],
  ['mysql', 'MySQL', 'The popular open-source SQL database.'],
  ['mariadb', 'MariaDB', 'Community-developed MySQL fork.'],
  ['mongodb', 'MongoDB', 'Document database.'],
  ['redis', 'Redis', 'In-memory cache, queue and key-value store.'],
];

route(/^\/new\/database$/, async (_, q) => {
  const [projects, servers] = await Promise.all([api('GET', '/api/projects'), serverOptions()]);
  if (!projects.length) { location.hash = '#/projects'; toast('Create a project first'); return; }
  page(`${pageHead('New database', 'One click, with automatic credentials and scheduled backups.')}
    <form id="dbf" class="card">
      <div class="grid cols-3" style="margin-bottom:16px">${ENGINES.map(([v, l, d], i) => `<label class="tile" style="cursor:pointer"><div class="title"><span><input type="radio" name="engine" value="${v}" ${i === 0 ? 'checked' : ''}> ${l}</span></div><div class="sub">${d}</div></label>`).join('')}</div>
      <div class="grid cols-2">
        ${field('Name', input('name', '', 'placeholder="e.g. main-db"'))}
        ${field('Project', select('project_id', projects.map((p) => [p.id, p.name]), q.get('project') || projects[0].id))}
      </div>
      <div class="grid cols-3">
        ${field('Server', select('server_id', servers, servers[0]?.[0]))}
        ${field('Version tag', input('version', '', 'placeholder="default"'))}
        ${field('Public port', input('public_port', '', 'type="number" data-num placeholder="off"'), 'Expose to the internet only if needed.')}
      </div>
      ${field('Backup schedule', input('backup_cron', '0 3 * * *'), 'Cron syntax (UTC), e.g. <code>0 3 * * *</code> daily at 03:00 or <code>@hourly</code>. Empty disables.')}
      <button class="primary" type="submit">Create database</button></form>`, (root) => {
    onSubmit(root.querySelector('#dbf'), async (d) => {
      d.project_id = Number(d.project_id);
      d.server_id = Number(d.server_id);
      const r = await api('POST', '/api/databases', d);
      location.hash = '#/databases/' + r.database.id;
      showTask(r.task_id, 'Starting ' + r.database.name, () => render());
    });
  });
});

route(/^\/databases\/(\d+)(?:\/(\w+))?$/, async ([id, tab = '']) => {
  const v = await api('GET', '/api/databases/' + id);
  const d = v.database;
  const tabs = `<div class="tabs">${[['', 'Overview'], ['backups', 'Backups'], ['logs', 'Logs']].map(([t, l]) => `<a href="#/databases/${id}${t ? '/' + t : ''}" class="${t === tab ? 'active' : ''}">${l}</a>`).join('')}</div>`;
  const head = pageHead(`${esc(d.name)} ${badge(d.status)}`, `${esc(d.engine)} ${esc(d.version)}`, `
    <button id="backup">Backup now</button><button id="restart">Restart</button>
    ${d.status === 'stopped' ? '<button id="start">Start</button>' : '<button id="stop">Stop</button>'}<button class="danger" id="delete">Delete</button>`);
  const actions = (root) => {
    for (const act of ['restart', 'start', 'backup']) {
      bind(root, '#' + act, async () => {
        const r = await api('POST', `/api/databases/${id}/${act}`);
        showTask(r.task_id, act === 'backup' ? 'Backing up ' + d.name : 'Starting ' + d.name, () => render());
      });
    }
    bind(root, '#stop', async () => { await api('POST', `/api/databases/${id}/stop`); render(); });
    bind(root, '#delete', async () => {
      const r = await confirmDanger(`Delete database <b>${esc(d.name)}</b>?`, '<label class="check"><input type="checkbox" name="volumes"> Also delete all its data (cannot be undone)</label>');
      if (!r) return;
      await api('DELETE', `/api/databases/${id}${r.volumes ? '?volumes=1' : ''}`);
      location.hash = '#/projects/' + d.project_id;
    });
  };
  if (tab === '') {
    page(`${head}${tabs}<div class="card"><h2>Connection</h2>
      <label>Internal URL (for apps on the same server)</label>${copyBox(v.internal_url)}
      <div class="grid cols-3" style="margin-top:12px">
        <div><label>Host</label>${copyBox(v.internal_host)}</div><div><label>User</label>${copyBox(d.db_user)}</div><div><label>Password</label>${copyBox(d.db_password)}</div>
      </div>
      ${v.public_url ? `<label style="margin-top:12px">Public URL</label>${copyBox(v.public_url)}` : '<p class="muted small" style="margin-top:12px">Not reachable from the internet. Set a public port below to expose it.</p>'}
      <p class="hint">Tip: add the internal URL as <code>DATABASE_URL</code> in your app's environment.</p></div>
      <form class="card" id="dbs"><h2>Settings</h2><div class="grid cols-2">
        ${field('Name', input('name', d.name))}
        ${field('Public port', input('public_port', d.public_port || '', 'type="number" data-num placeholder="off"'))}
        ${field('Backup schedule (cron, UTC)', input('backup_cron', d.backup_cron, 'placeholder="0 3 * * *"'))}
        ${field('Backups to keep', input('backup_keep', d.backup_keep, 'type="number" data-num min="1"'))}</div>
        <label class="check"><input type="checkbox" name="backup_s3" ${d.backup_s3 ? 'checked' : ''}> Upload backups to S3 storage (configure under Settings)</label>
        <button class="primary" type="submit" style="margin-top:12px">Save</button></form>`, (root) => {
      actions(root);
      onSubmit(root.querySelector('#dbs'), async (x) => { await api('PUT', '/api/databases/' + id, x); toast('Saved'); render(); });
    });
  } else if (tab === 'backups') {
    const list = await api('GET', `/api/databases/${id}/backups`);
    page(`${head}${tabs}<div class="card">${list.length ? `<div class="table-wrap"><table><thead><tr><th>When</th><th>Status</th><th>Size</th><th>Note</th><th></th></tr></thead><tbody>
      ${list.map((b) => `<tr><td>${new Date(b.created_at).toLocaleString()}</td><td>${badge(b.status)}</td><td>${bytes(b.size)}</td><td class="muted small">${esc(b.message)}</td>
        <td style="text-align:right">${b.status === 'success' ? `<a href="/api/backups/${b.id}/download">Download</a> · <a href="#" data-restore="${b.id}">Restore</a> · ` : ''}<a href="#" data-del="${b.id}">Delete</a></td></tr>`).join('')}
      </tbody></table></div>` : '<div class="empty">No backups yet. Click "Backup now" or set a schedule.</div>'}</div>`, (root) => {
      actions(root);
      bind(root, '[data-restore]', async (el) => {
        if (!(await confirmDanger('Restore this backup? Current data will be overwritten.'))) return;
        const r = await api('POST', `/api/backups/${el.dataset.restore}/restore`);
        showTask(r.task_id, 'Restoring backup');
      });
      bind(root, '[data-del]', async (el) => { await api('DELETE', '/api/backups/' + el.dataset.del); render(); });
    });
  } else if (tab === 'logs') {
    const r = await api('GET', `/api/databases/${id}/logs?tail=300`);
    page(`${head}${tabs}<div class="card"><pre class="log">${esc(r.error || r.logs || 'No output.')}</pre></div>`, actions);
  }
});

// ---------- services ----------

route(/^\/new\/service$/, async (_, q) => {
  const [templates, projects, servers] = await Promise.all([api('GET', '/api/templates'), api('GET', '/api/projects'), serverOptions()]);
  if (!projects.length) { location.hash = '#/projects'; toast('Create a project first'); return; }
  page(`${pageHead('One-click services', 'Popular open-source apps, ready in a minute.')}
    <div class="grid cols-3">${templates.map((t) => `<a class="tile" href="#" data-t="${t.id}"><div class="title">${esc(t.name)} <span class="badge plain">${esc(t.category)}</span></div><div class="sub" style="white-space:normal">${esc(t.description)}</div></a>`).join('')}</div>`, (root) => {
    bind(root, '[data-t]', (el) => {
      const t = templates.find((x) => x.id === el.dataset.t);
      const m = modal(`<h2>Deploy ${esc(t.name)}</h2><form id="sf">
        ${field('Name', input('name', t.name))}
        <div class="grid cols-2">${field('Project', select('project_id', projects.map((p) => [p.id, p.name]), q.get('project') || projects[0].id))}
        ${field('Server', select('server_id', servers, servers[0]?.[0]))}</div>
        ${field('Domain', input('domain', '', 'placeholder="https://app.example.com"'), 'Leave empty for a free temporary sslip.io domain.')}
        <input type="hidden" name="template" value="${esc(t.id)}">
        <div class="row" style="justify-content:flex-end"><button type="button" data-close>Cancel</button><button class="primary" type="submit">Deploy</button></div></form>`);
      onSubmit(m.querySelector('#sf'), async (d) => {
        d.project_id = Number(d.project_id);
        d.server_id = Number(d.server_id);
        const r = await api('POST', '/api/services', d);
        m.close();
        location.hash = '#/services/' + r.service.id;
        showTask(r.task_id, 'Deploying ' + r.service.name, () => render());
      });
    });
  });
});

route(/^\/services\/(\d+)$/, async ([id]) => {
  const v = await api('GET', '/api/services/' + id);
  const s = v.service;
  const creds = Object.entries(v.credentials || {});
  page(`${pageHead(`${esc(s.name)} ${badge(s.status)}`, esc(v.template.name || s.template), `
      <button class="primary" id="deploy">Redeploy</button>${s.status === 'stopped' ? '<button id="start">Start</button>' : '<button id="stop">Stop</button>'}<button class="danger" id="delete">Delete</button>`)}
    <div class="grid cols-2">
      <div class="card"><h2>Access</h2>${s.domain ? `<p><a href="${esc(s.domain)}" target="_blank" rel="noopener">${esc(s.domain)}</a></p>` : '<p class="muted">No domain</p>'}
        ${v.notes ? `<div class="callout small">${esc(v.notes)}</div>` : ''}
        <form id="domf">${field('Domain', input('domain', s.domain), 'Changing the domain redeploys the service.')}<button type="submit">Save domain</button></form></div>
      <div class="card"><h2>Credentials</h2>${creds.length ? creds.map(([k, val]) => `<label>${esc(k)}</label>${copyBox(val)}<div style="height:8px"></div>`).join('') : '<p class="muted">No generated credentials.</p>'}</div>
    </div>
    <div class="card"><div class="row" style="justify-content:space-between;margin-bottom:10px"><h2 style="margin:0">Logs</h2><button class="sm" id="reload">Refresh</button></div><pre class="log" id="logs">Loading...</pre></div>`, (root) => {
    const loadLogs = async () => {
      const r = await api('GET', `/api/services/${id}/logs?tail=200`).catch((e) => ({ error: e.message }));
      root.querySelector('#logs').textContent = r.error || r.logs || 'No output.';
    };
    loadLogs();
    bind(root, '#reload', loadLogs);
    bind(root, '#deploy', async () => { const r = await api('POST', `/api/services/${id}/deploy`); showTask(r.task_id, 'Deploying ' + s.name, () => render()); });
    bind(root, '#start', async () => { await api('POST', `/api/services/${id}/start`); render(); });
    bind(root, '#stop', async () => { await api('POST', `/api/services/${id}/stop`); render(); });
    bind(root, '#delete', async () => {
      const r = await confirmDanger(`Delete <b>${esc(s.name)}</b>?`, '<label class="check"><input type="checkbox" name="volumes"> Also delete its data volumes</label>');
      if (!r) return;
      await api('DELETE', `/api/services/${id}${r.volumes ? '?volumes=1' : ''}`);
      location.hash = '#/projects/' + s.project_id;
    });
    onSubmit(root.querySelector('#domf'), async (d) => {
      const r = await api('PUT', '/api/services/' + id, d);
      showTask(r.task_id, 'Redeploying ' + s.name, () => render());
    });
  });
});

// ---------- mail ----------

route(/^\/mail$/, async () => {
  const m = await api('GET', '/api/mail');
  const installed = m.status !== 'not installed';
  const setup = `<form class="card" id="enable"><h2>${installed ? 'Mail server settings' : 'Set up your mail server'}</h2>
    <p class="muted">Send and receive email on your own domains: SMTP, IMAP, spam filtering, DKIM, SPF and DMARC, with TLS certificates issued automatically.</p>
    <div class="callout warn small">Before you start: point an <b>A record</b> for the mail hostname to <b>${esc(m.server_ip || 'your server IP')}</b>, make sure your provider allows outbound port 25, and set reverse DNS (PTR) to the hostname.</div>
    ${field('Mail hostname', input('hostname', m.hostname, 'placeholder="mail.example.com" required'))}
    <div class="row"><button class="primary" type="submit">${installed ? 'Apply / renew certificate' : 'Install mail server'}</button>${installed ? '<button type="button" class="danger" id="disable">Stop mail server</button>' : ''}</div></form>`;
  const boxes = (domainId) => m.mailboxes.filter((b) => b.domain_id === domainId);
  const domains = (m.domains || []).map(({ domain: d, dns }) => `<div class="card"><div class="row" style="justify-content:space-between"><h2 style="margin:0">${esc(d.domain)}</h2>
      <div class="row"><button class="sm" data-dkim="${d.id}">${d.dkim_record ? 'Regenerate' : 'Generate'} DKIM key</button><button class="sm danger" data-deldomain="${d.id}">Remove</button></div></div>
      <h3 style="margin-top:14px">DNS records to add</h3><div class="table-wrap"><table><thead><tr><th>Type</th><th>Name</th><th>Value</th></tr></thead><tbody>
      ${dns.map((r) => `<tr><td><b>${esc(r.type)}</b></td><td class="mono break">${esc(r.name)}</td><td class="mono break">${r.priority ? esc(r.priority) + ' ' : ''}${esc(r.value)} <button class="sm" data-copy="${esc(r.value)}">Copy</button><div class="hint">${esc(r.note)}</div></td></tr>`).join('')}
      </tbody></table></div>${d.dkim_record ? '' : '<p class="hint">Create a mailbox first, then generate the DKIM key.</p>'}
      <h3 style="margin-top:14px">Mailboxes</h3>${boxes(d.id).map((b) => `<div class="row" style="justify-content:space-between;padding:6px 0;border-bottom:1px solid var(--border)"><span>${esc(b.address)}</span>
        <span class="row"><button class="sm" data-pw="${b.id}">Change password</button><button class="sm danger" data-delbox="${b.id}">Delete</button></span></div>`).join('') || '<p class="muted small">No mailboxes yet.</p>'}
      <form class="row" data-addbox="${esc(d.domain)}" style="margin-top:10px"><input class="grow" name="user" placeholder="name" required><span class="muted">@${esc(d.domain)}</span>
        <input class="grow" name="password" type="password" placeholder="password (min 8)" minlength="8" required autocomplete="new-password"><button type="submit">Add mailbox</button></form></div>`).join('');
  page(`${pageHead(`Mail server ${badge(m.status)}`, 'Your own email server for your domains.')}
    ${setup}
    ${installed ? `<div class="card"><h2>Connect your mail client or app</h2><div class="grid cols-2">
        <div><label>SMTP (sending)</label>${copyBox(`${m.smtp.host}:587 (STARTTLS) or :465 (TLS)`)}</div>
        <div><label>IMAP (reading)</label>${copyBox(`${m.smtp.host}:993 (TLS)`)}</div>
        <div><label>From your apps on this server</label>${copyBox(`smtp://${m.smtp.internal_host}:25`)}<div class="hint">No login needed from containers on the SelfDeploy network.</div></div>
        <div><label>Webmail</label><p class="small">Deploy <a href="#/projects">Roundcube</a> from one-click services.</p></div></div></div>
      <form class="card row" id="adddomain"><input class="grow" name="domain" placeholder="example.com" required><button class="primary" type="submit">+ Add domain</button></form>
      ${domains}
      <div class="card"><h2>Aliases / forwarding</h2>${(m.aliases || []).map((a) => `<div class="row" style="justify-content:space-between;padding:6px 0;border-bottom:1px solid var(--border)"><span>${esc(a.source)} → ${esc(a.destination)}</span><button class="sm danger" data-delalias="${a.id}">Delete</button></div>`).join('') || '<p class="muted small">No aliases.</p>'}
        <form class="row" id="addalias" style="margin-top:10px"><input class="grow" name="source" placeholder="info@example.com" required><span>→</span><input class="grow" name="destination" placeholder="you@gmail.com" required><button type="submit">Add alias</button></form></div>` : ''}`, (root) => {
    onSubmit(root.querySelector('#enable'), async (d) => {
      const r = await api('POST', '/api/mail/enable', d);
      showTask(r.task_id, 'Setting up the mail server', () => render());
    });
    bind(root, '#disable', async () => { if (await confirmDanger('Stop the mail server? Mail data is kept.')) { await api('POST', '/api/mail/disable'); render(); } });
    const af = root.querySelector('#adddomain');
    if (af) onSubmit(af, async (d) => { await api('POST', '/api/mail/domains', d); render(); });
    const al = root.querySelector('#addalias');
    if (al) onSubmit(al, async (d) => { await api('POST', '/api/mail/aliases', d); render(); });
    root.querySelectorAll('[data-addbox]').forEach((f) => onSubmit(f, async (d) => {
      await api('POST', '/api/mail/mailboxes', { address: `${d.user}@${f.dataset.addbox}`, password: d.password });
      toast('Mailbox created');
      render();
    }));
    bind(root, '[data-dkim]', async (el) => { await api('POST', `/api/mail/domains/${el.dataset.dkim}/dkim`); toast('DKIM key generated, add the TXT record'); render(); });
    bind(root, '[data-deldomain]', async (el) => { if (await confirmDanger('Remove this domain and all its mailboxes?')) { await api('DELETE', '/api/mail/domains/' + el.dataset.deldomain); render(); } });
    bind(root, '[data-delbox]', async (el) => { if (await confirmDanger('Delete this mailbox and its mail?')) { await api('DELETE', '/api/mail/mailboxes/' + el.dataset.delbox); render(); } });
    bind(root, '[data-delalias]', async (el) => { await api('DELETE', '/api/mail/aliases/' + el.dataset.delalias); render(); });
    bind(root, '[data-pw]', (el) => {
      const md = modal(`<h2>Change password</h2><form id="pwf">${field('New password', input('password', '', 'type="password" minlength="8" required autocomplete="new-password"'))}
        <div class="row" style="justify-content:flex-end"><button type="button" data-close>Cancel</button><button class="primary" type="submit">Save</button></div></form>`);
      onSubmit(md.querySelector('#pwf'), async (d) => { await api('PUT', `/api/mail/mailboxes/${el.dataset.pw}/password`, d); md.close(); toast('Password changed'); });
    });
  });
});

// ---------- servers ----------

route(/^\/servers$/, async () => {
  const [servers, key] = await Promise.all([api('GET', '/api/servers'), api('GET', '/api/ssh-key')]);
  page(`${pageHead('Servers', 'Deploy to this machine or to any server you can reach over SSH.', '<button class="primary" id="add">+ Add server</button>')}
    <div class="card"><div class="table-wrap"><table><thead><tr><th>Name</th><th>Address</th><th>Public IP</th><th>Proxy</th><th></th></tr></thead><tbody>
    ${servers.map((s) => `<tr><td><b>${esc(s.name)}</b>${s.is_local ? ' <span class="badge plain">this server</span>' : ''}</td>
      <td class="mono">${s.is_local ? 'local docker' : esc(`${s.user}@${s.host}:${s.port}`)}</td><td class="mono">${esc(s.public_ip)}</td><td>${badge(s.proxy_status)}</td>
      <td style="text-align:right"><button class="sm" data-validate="${s.id}">Test</button> <button class="sm" data-proxy="${s.id}">Start proxy</button> <button class="sm" data-stats="${s.id}">Stats</button> <button class="sm" data-edit="${s.id}">Edit</button>
      ${s.is_local ? '' : `<button class="sm danger" data-del="${s.id}">Remove</button>`}</td></tr>`).join('')}
    </tbody></table></div></div>
    <div class="card"><h2>SelfDeploy SSH key</h2><p class="muted small">Add this public key to <code>~/.ssh/authorized_keys</code> on remote servers (they need Docker installed), and as a deploy key on private repos cloned over SSH.</p>${key.public_key ? copyBox(key.public_key) : '<p class="muted">Not available.</p>'}</div>`, (root) => {
    const editModal = (s = {}) => {
      const m = modal(`<h2>${s.id ? 'Edit server' : 'Add server'}</h2><form id="sf">
        ${field('Name', input('name', s.name, 'required'))}
        ${s.is_local ? '' : `<div class="grid cols-3">${field('Host / IP', input('host', s.host, 'required'))}${field('SSH user', input('user', s.user || 'root'))}${field('SSH port', input('port', s.port || 22, 'type="number" data-num'))}</div>`}
        ${field('Public IP', input('public_ip', s.public_ip), 'Used for temporary sslip.io domains and DNS instructions.')}
        <div class="row" style="justify-content:flex-end"><button type="button" data-close>Cancel</button><button class="primary" type="submit">Save</button></div></form>`);
      onSubmit(m.querySelector('#sf'), async (d) => {
        await api(s.id ? 'PUT' : 'POST', s.id ? '/api/servers/' + s.id : '/api/servers', d);
        m.close();
        render();
      });
    };
    bind(root, '#add', () => editModal());
    bind(root, '[data-edit]', (el) => editModal(servers.find((s) => s.id === Number(el.dataset.edit))));
    bind(root, '[data-validate]', async (el) => {
      const r = await api('POST', `/api/servers/${el.dataset.validate}/validate`);
      r.ok ? toast('Connected. Docker ' + r.docker_version) : toast(r.error, true);
    });
    bind(root, '[data-proxy]', async (el) => { const r = await api('POST', `/api/servers/${el.dataset.proxy}/proxy`); showTask(r.task_id, 'Starting proxy', () => render()); });
    bind(root, '[data-del]', async (el) => { if (await confirmDanger('Remove this server from SelfDeploy?')) { await api('DELETE', '/api/servers/' + el.dataset.del); render(); } });
    bind(root, '[data-stats]', async (el) => {
      const r = await api('GET', `/api/servers/${el.dataset.stats}/stats`);
      const mm = r.metrics;
      modal(`<h2>Server stats</h2><div class="grid cols-3" style="margin-bottom:14px">
        <div class="stat"><div class="label">CPUs</div><div class="value">${mm.cpus || '–'}</div><div class="muted small">load ${mm.load1 ?? '–'}</div></div>
        ${meter('Memory', mm.mem_used, mm.mem_total, bytes)}${meter('Disk', mm.disk_used, mm.disk_total, bytes)}</div>
        ${r.error ? `<p class="muted">${esc(r.error)}</p>` : `<div class="table-wrap"><table><thead><tr><th>Container</th><th>CPU</th><th>Memory</th><th>Net I/O</th></tr></thead><tbody>
        ${(r.containers || []).map((c) => `<tr><td class="mono">${esc(c.name)}</td><td>${esc(c.cpu)}</td><td>${esc(c.mem)}</td><td>${esc(c.net_io)}</td></tr>`).join('')}</tbody></table></div>`}
        <div class="row" style="justify-content:flex-end;margin-top:12px"><button data-close>Close</button></div>`);
    });
  });
});

// ---------- notifications ----------

const EVENTS = [['deploy_success', 'Deploy succeeded'], ['deploy_failed', 'Deploy failed'], ['backup_success', 'Backup succeeded'], ['backup_failed', 'Backup failed']];
const KINDS = [['discord', 'Discord webhook'], ['slack', 'Slack webhook'], ['telegram', 'Telegram bot'], ['email', 'Email'], ['webhook', 'Custom webhook (JSON)']];

route(/^\/notifications$/, async () => {
  const list = await api('GET', '/api/notifications');
  page(`${pageHead('Notifications', 'Get told when deployments or backups succeed or fail.', '<button class="primary" id="add">+ Add channel</button>')}
    <div class="card">${list.length ? `<div class="table-wrap"><table><thead><tr><th>Name</th><th>Type</th><th>Events</th><th>Status</th><th></th></tr></thead><tbody>
    ${list.map((n) => `<tr><td>${esc(n.name)}</td><td>${esc(n.kind)}</td><td class="small">${esc(n.events.replaceAll(',', ', '))}</td><td>${n.enabled ? badge('running').replace('running', 'on') : badge('stopped').replace('stopped', 'off')}</td>
      <td style="text-align:right"><button class="sm" data-test="${n.id}">Send test</button> <button class="sm" data-edit="${n.id}">Edit</button> <button class="sm danger" data-del="${n.id}">Delete</button></td></tr>`).join('')}
    </tbody></table></div>` : '<div class="empty">No channels yet.</div>'}</div>`, (root) => {
    const edit = (n = { kind: 'discord', events: 'deploy_success,deploy_failed,backup_failed', enabled: true }) => {
      const ev = n.events.split(',');
      const m = modal(`<h2>${n.id ? 'Edit' : 'Add'} channel</h2><form id="nf">
        <div class="grid cols-2">${field('Name', input('name', n.name))}${field('Type', select('kind', KINDS, n.kind))}</div>
        ${field('Target', input('target', n.target, 'required'), 'Webhook URL, Telegram bot token, or email address (email uses the SMTP settings).')}
        ${field('Telegram chat id', input('extra', n.extra))}
        <label>Events</label>${EVENTS.map(([k, l]) => `<label class="check"><input type="checkbox" data-ev="${k}" ${ev.includes(k) ? 'checked' : ''}> ${l}</label>`).join('')}
        <label class="check" style="margin-top:8px"><input type="checkbox" name="enabled" ${n.enabled ? 'checked' : ''}> Enabled</label>
        <div class="row" style="justify-content:flex-end;margin-top:12px"><button type="button" data-close>Cancel</button><button class="primary" type="submit">Save</button></div></form>`);
      const f = m.querySelector('#nf');
      onSubmit(f, async (d) => {
        d.events = [...f.querySelectorAll('[data-ev]')].filter((c) => c.checked).map((c) => c.dataset.ev).join(',');
        await api(n.id ? 'PUT' : 'POST', n.id ? '/api/notifications/' + n.id : '/api/notifications', d);
        m.close();
        render();
      });
    };
    bind(root, '#add', () => edit());
    bind(root, '[data-edit]', (el) => edit(list.find((n) => n.id === Number(el.dataset.edit))));
    bind(root, '[data-test]', async (el) => { await api('POST', `/api/notifications/${el.dataset.test}/test`); toast('Test sent'); });
    bind(root, '[data-del]', async (el) => { if (await confirmDanger('Delete this channel?')) { await api('DELETE', '/api/notifications/' + el.dataset.del); render(); } });
  });
});

// ---------- settings ----------

route(/^\/settings(?:\/(\w+))?$/, async ([tab = '']) => {
  const isAdmin = state.user?.is_admin;
  const tabsList = [['', 'General'], ['backups', 'Backup storage'], ['smtp', 'Email (SMTP)'], ['users', 'Users'], ['tokens', 'API tokens'], ['account', 'Account']]
    .filter(([t]) => isAdmin || ['tokens', 'account'].includes(t));
  if (!isAdmin && !['tokens', 'account'].includes(tab)) tab = 'account';
  const tabs = `<div class="tabs">${tabsList.map(([t, l]) => `<a href="#/settings${t ? '/' + t : ''}" class="${t === tab ? 'active' : ''}">${l}</a>`).join('')}</div>`;
  const head = pageHead('Settings', '');
  const s = isAdmin ? await api('GET', '/api/settings') : {};
  const settingsForm = (fields) => `<form class="card" id="sf">${fields}<button class="primary" type="submit">Save</button></form>`;
  const saveSettings = (root) => onSubmit(root.querySelector('#sf'), async (d) => { await api('PUT', '/api/settings', d); toast('Saved'); render(); });

  if (tab === '') {
    page(`${head}${tabs}${settingsForm(`
      ${field('Dashboard domain', input('instance_domain', s.instance_domain, 'placeholder="https://deploy.example.com"'), 'Serve this dashboard on your own domain with HTTPS. Point an A record at this server first.')}
      ${field('Wildcard domain for new resources', input('wildcard_domain', s.wildcard_domain, 'placeholder="apps.example.com"'), 'With a <code>*.apps.example.com</code> DNS record, new apps get <code>https://name.apps.example.com</code>. Empty = free <b>sslip.io</b> temporary domains.')}
      ${field("Let's Encrypt email", input('acme_email', s.acme_email, 'type="email" placeholder="you@example.com"'), 'Optional ACME account contact. Certificates renew automatically.')}
      <div class="row" style="margin-bottom:14px"><span class="muted small">Theme:</span><button type="button" class="sm" data-theme="light">Light</button><button type="button" class="sm" data-theme="dark">Dark</button><button type="button" class="sm" data-theme="">System</button></div>`)}`, (root) => {
      saveSettings(root);
      root.querySelectorAll('[data-theme]').forEach((b) => b.addEventListener('click', () => setTheme(b.dataset.theme)));
    });
  } else if (tab === 'backups') {
    page(`${head}${tabs}${settingsForm(`<p class="muted">Any S3-compatible storage: AWS S3, Cloudflare R2, Backblaze B2, Wasabi, MinIO, DigitalOcean Spaces.</p>
      <div class="grid cols-2">${field('Endpoint', input('s3_endpoint', s.s3_endpoint, 'placeholder="https://s3.amazonaws.com"'))}${field('Region', input('s3_region', s.s3_region, 'placeholder="us-east-1"'))}
      ${field('Bucket', input('s3_bucket', s.s3_bucket))}${field('Access key', input('s3_access_key', s.s3_access_key, 'autocomplete="off"'))}</div>
      ${field('Secret key', input('s3_secret_key', s.s3_secret_key, 'type="password" autocomplete="off"'))}`)}`, saveSettings);
  } else if (tab === 'smtp') {
    page(`${head}${tabs}${settingsForm(`<p class="muted">Used to send email notifications. You can point it at the built-in mail server (port 587) or any provider.</p>
      <div class="grid cols-2">${field('SMTP host', input('smtp_host', s.smtp_host))}${field('Port', input('smtp_port', s.smtp_port, 'placeholder="587"'))}
      ${field('Username', input('smtp_user', s.smtp_user, 'autocomplete="off"'))}${field('Password', input('smtp_password', s.smtp_password, 'type="password" autocomplete="off"'))}</div>
      ${field('From address', input('smtp_from', s.smtp_from, 'placeholder="SelfDeploy <noreply@example.com>"'))}`)}`, saveSettings);
  } else if (tab === 'users') {
    const users = await api('GET', '/api/users');
    page(`${head}${tabs}<div class="card"><div class="table-wrap"><table><thead><tr><th>Email</th><th>Name</th><th>Role</th><th></th></tr></thead><tbody>
      ${users.map((u) => `<tr><td>${esc(u.email)}</td><td>${esc(u.name)}</td><td>${u.is_admin ? 'Admin' : 'Member'}</td><td style="text-align:right">${u.id === state.user.id ? '' : `<button class="sm danger" data-del="${u.id}">Remove</button>`}</td></tr>`).join('')}
      </tbody></table></div></div>
      <form class="card" id="uf"><h2>Add user</h2><div class="grid cols-3">${field('Email', input('email', '', 'type="email" required'))}${field('Name', input('name', ''))}${field('Password', input('password', '', 'type="password" minlength="8" required autocomplete="new-password"'))}</div>
      <label class="check"><input type="checkbox" name="is_admin"> Admin (can manage servers, settings and users)</label><button class="primary" type="submit" style="margin-top:12px">Add user</button></form>`, (root) => {
      onSubmit(root.querySelector('#uf'), async (d) => { await api('POST', '/api/users', d); toast('User added'); render(); });
      bind(root, '[data-del]', async (el) => { if (await confirmDanger('Remove this user?')) { await api('DELETE', '/api/users/' + el.dataset.del); render(); } });
    });
  } else if (tab === 'tokens') {
    const tokens = await api('GET', '/api/tokens');
    page(`${head}${tabs}<div class="card"><p class="muted">Use tokens with <code>Authorization: Bearer &lt;token&gt;</code>, for example to deploy from CI: <code>curl -X POST -H "Authorization: Bearer $TOKEN" ${esc(location.origin)}/api/apps/ID/deploy</code></p>
      ${tokens.map((t) => `<div class="row" style="justify-content:space-between;padding:6px 0;border-bottom:1px solid var(--border)"><span>${esc(t.name)} <span class="muted small">${ago(t.created_at)}</span></span><button class="sm danger" data-del="${t.id}">Revoke</button></div>`).join('') || '<p class="muted small">No tokens.</p>'}
      <form class="row" id="tf" style="margin-top:12px"><input class="grow" name="name" placeholder="Token name, e.g. GitHub Actions"><button class="primary" type="submit">Create token</button></form><div id="newtok" style="margin-top:12px"></div></div>`, (root) => {
      onSubmit(root.querySelector('#tf'), async (d) => {
        const r = await api('POST', '/api/tokens', d);
        root.querySelector('#newtok').innerHTML = `<div class="callout">Copy this token now, it will not be shown again.</div>${copyBox(r.token)}`;
      });
      bind(root, '[data-del]', async (el) => { await api('DELETE', '/api/tokens/' + el.dataset.del); render(); });
    });
  } else if (tab === 'account') {
    page(`${head}${tabs}<form class="card" id="pf"><h2>Change password</h2>${field('Current password', input('current', '', 'type="password" required autocomplete="current-password"'))}
      ${field('New password', input('new', '', 'type="password" minlength="8" required autocomplete="new-password"'))}<button class="primary" type="submit">Change password</button></form>`, (root) => {
      onSubmit(root.querySelector('#pf'), async (d) => { await api('POST', '/api/me/password', d); toast('Password changed'); root.querySelector('#pf').reset(); });
    });
  }
});

// ---------- theme & boot ----------

function setTheme(t) {
  try { t ? localStorage.setItem('sd-theme', t) : localStorage.removeItem('sd-theme'); } catch (e) { /* storage unavailable */ }
  t ? document.documentElement.setAttribute('data-theme', t) : document.documentElement.removeAttribute('data-theme');
}

(async function boot() {
  try { const t = localStorage.getItem('sd-theme'); if (t) document.documentElement.setAttribute('data-theme', t); } catch (e) { /* ignore */ }
  try {
    const st = await api('GET', '/api/auth/status');
    state.user = st.user || null;
    state.version = st.version || '';
    if (st.setup_required) location.hash = '#/register';
  } catch (e) { /* offline: the login page shows the error */ }
  render();
})();
