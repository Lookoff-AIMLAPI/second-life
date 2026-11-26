'use strict';

// Catalog text is untrusted: it only ever reaches the page through textContent.

const OFFICIAL = 'claude-plugins-official';
const PAGE = 200;
const $ = (id) => document.getElementById(id);

function h(tag, props, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (k === 'text') {
      el.textContent = v;
    } else if (k.startsWith('on')) {
      el.addEventListener(k.slice(2), v);
    } else if (k in el && k !== 'role') {
      el[k] = v;
    } else if (v !== null && v !== undefined) {
      el.setAttribute(k, v);
    }
  }
  el.append(...kids.filter((x) => x !== null && x !== undefined));
  return el;
}

const st = {
  started: false, state: 'loading', installed: [], available: [], hasOfficial: true, hint: '',
  filter: 'anthropic', query: '', shown: PAGE, busy: '', details: {}, signedOut: false,
};
const say = (t) => { $('say').textContent = t; };
const count = (n, one) => `${n.toLocaleString('en')} ${one}${n === 1 ? '' : 's'}`;

// ---- calls

async function call(method, path, body) {
  let r;
  try {
    r = await fetch(`api/${path}`, {
      method,
      redirect: 'error',
      credentials: 'same-origin',
      referrerPolicy: 'same-origin',
      headers: { 'X-Second-Life-Picker': '1', ...(body ? { 'Content-Type': 'application/json' } : {}) },
      body: body ? JSON.stringify(body) : undefined,
    });
  } catch {
    banner('offline', 'The bridge is not running. Start it from the app menu, then reload this page.');
    throw Object.assign(new Error('The bridge is not running.'), { banner: true });
  }
  unbanner('offline');
  const data = await r.json().catch(() => ({}));
  if (r.ok) {
    if (st.signedOut) {
      st.signedOut = false;
      unbanner('signed-out');
    }
    return data;
  }
  if (r.status === 401 && path !== 'session') {
    st.signedOut = true;
    banner('signed-out', 'This page is not signed in. Choose Plugins in the app menu again, then come back to this tab.');
    throw Object.assign(new Error('Not signed in.'), { banner: true });
  }
  if (r.status === 409) {
    throw new Error('Another plugin change is running. Wait for it to finish.');
  }
  throw new Error(data.error || `The bridge answered ${r.status}.`);
}

function banner(id, text) {
  if (!$(`b-${id}`)) {
    $('alerts').append(h('div', { id: `b-${id}`, class: 'notice' }, h('p', { text })));
  }
}
const unbanner = (id) => { const b = $(`b-${id}`); if (b) { b.remove(); } };

async function signIn() {
  const code = location.hash.slice(1);
  if (!code) {
    return;
  }
  history.replaceState(null, '', location.pathname);
  try {
    await call('POST', 'session', { code });
  } catch { /* an older session may still be good */ }
}

function ask(head, text, yes) {
  return new Promise((done) => {
    const d = $('ask');
    let answer = false;
    const no = h('button', { type: 'button', text: 'Cancel', onclick: () => d.close() });
    d.replaceChildren(
      h('h2', { id: 'ask-title', text: head }),
      h('p', { id: 'ask-text', text }),
      h('div', { class: 'actions' }, no, h('button', { type: 'button', class: 'main', text: yes, onclick: () => { answer = true; d.close(); } })),
    );
    d.addEventListener('close', () => done(answer), { once: true });
    d.showModal();
    no.focus();
  });
}

// ---- data

async function load() {
  const r = await call('GET', 'plugins');
  Object.assign(st, { state: r.state, installed: r.installed || [], available: r.available || [], hasOfficial: r.hasOfficial, hint: r.installHint });
  $('where').textContent = r.configDir ? `Plugins in ${r.configDir}` : '';
}

const installedIDs = () => new Set(st.installed.map((p) => p.id));
const byID = (id) => st.available.find((p) => p.id === id);
const nameOf = (id) => (byID(id) || {}).name || id.split('@')[0];

function matches() {
  const q = st.query.trim().toLowerCase();
  const have = installedIDs();
  return st.available.filter((p) => !have.has(p.id)
    && (st.filter === 'all' || p.marketplace === OFFICIAL)
    && (!q || p.name.toLowerCase().includes(q) || p.description.toLowerCase().includes(q)));
}

// ---- actions

function nextStep(name, servers) {
  if (servers.length) {
    return `${name} is installed. To sign in to ${servers.join(', ')}: open Claude from the menu, type /mcp and choose each one.`;
  }
  return `${name} is installed. It is ready in new Claude sessions.`;
}

async function install(p) {
  const yes = await ask(`Install ${p.name}?`,
    `${p.name} from ${p.marketplace} runs on this computer with your permissions. It may also install plugins it depends on. What it contains is shown after install.`,
    'Install');
  if (!yes) {
    return;
  }
  await change(p.id, 'Installing…', async () => {
    const r = await call('POST', 'plugin', { id: p.id });
    if (!r.ok) {
      return { notice: r.needsTerminal };
    }
    return { notice: nextStep(p.name, r.servers || []) };
  });
}

async function remove(p) {
  const name = nameOf(p.id);
  if (!await ask(`Remove ${name}?`, 'Its saved data stays.', 'Remove')) {
    return;
  }
  await change(p.id, 'Removing…', async () => {
    await call('DELETE', 'plugin', { id: p.id });
    return { notice: `${name} is removed.` };
  });
}

async function catalog(method, doing) {
  await change('catalog', doing, async () => {
    await call(method, 'catalog', {});
    return { notice: method === 'POST' ? 'The catalog is added.' : 'The catalog is up to date.' };
  });
}

async function change(id, doing, run) {
  if (st.busy) {
    return;
  }
  st.busy = id;
  st.doing = doing;
  st.notice = '';
  st.error = '';
  draw();
  say(doing);
  try {
    const r = await run();
    st.notice = r.notice;
    await load();
  } catch (e) {
    if (!e.banner) {
      st.error = e.message;
    }
  }
  st.busy = '';
  draw();
  say(st.error || st.notice);
}

async function details(id) {
  if (st.details[id] !== undefined) {
    return;
  }
  st.details[id] = 'Loading…';
  draw();
  try {
    const r = await call('POST', 'plugin-details', { id });
    st.details[id] = r.text || 'Claude Code has nothing more to say about it.';
  } catch (e) {
    st.details[id] = e.message;
  }
  draw();
}

// ---- drawing

function button(label, id, act, main) {
  const b = h('button', { type: 'button', class: main ? 'main' : '', text: st.busy === id ? st.doing : label, onclick: () => { if (!st.busy) { act(); } } });
  if (st.busy) {
    b.setAttribute('aria-disabled', 'true');
  }
  return b;
}

function installedRow(p) {
  const name = nameOf(p.id);
  const open = st.details[p.id] !== undefined;
  const meta = [p.id, p.scope ? `for ${p.scope === 'user' ? 'all projects' : `this ${p.scope}`}` : 'loaded for one session', p.enabled ? '' : 'turned off']
    .filter(Boolean).join(' · ');
  return h('li', { class: 'row plugin' },
    h('div', {},
      h('h3', { text: name }),
      h('p', { class: 'small quiet', text: meta }),
      p.mcpServers.length ? h('p', { class: 'small', text: `Services: ${p.mcpServers.join(', ')}. Sign in with /mcp in Claude.` }) : null,
      h('button', { type: 'button', class: 'plain small', 'aria-expanded': String(open), text: open ? 'Contents' : 'Show contents', onclick: () => details(p.id) }),
      open ? h('pre', { class: 'details', text: st.details[p.id] }) : null),
    p.scope ? button('Remove', p.id, () => remove(p)) : null);
}

function catalogRow(p) {
  const meta = [p.marketplace === OFFICIAL ? 'Anthropic' : 'Anthropic Directory', p.installs ? `${count(p.installs, 'install')}` : ''].filter(Boolean).join(' · ');
  return h('li', { class: 'row plugin' },
    h('div', {},
      h('h3', { text: p.name }),
      p.description ? h('p', { text: p.description }) : null,
      h('p', { class: 'small quiet', text: meta }),
      p.needsTerminal ? h('p', { class: 'small', text: `It runs a command you need to see first. In Terminal: claude plugin install ${p.id}` }) : null),
    p.needsTerminal ? null : button('Install', p.id, () => install(p), true));
}

function claudeMissing() {
  const code = h('code', { text: st.hint });
  return h('section', { class: 'notice' },
    h('p', { text: st.state === 'old-claude' ? 'Update Claude Code to use plugins.' : 'Claude Code is not installed. Install it in Terminal, then reload this page:' }),
    st.state === 'old-claude' ? null : code);
}

function draw() {
  const main = $('main');
  if (st.state !== 'ok') {
    main.replaceChildren($('alerts'), claudeMissing());
    return;
  }
  const status = [];
  if (st.notice) {
    status.push(h('p', { class: 'notice', text: st.notice }));
  }
  if (st.error) {
    status.push(h('p', { class: 'notice alert', text: st.error }));
  }

  const inst = st.installed.length
    ? h('section', { 'aria-labelledby': 'h-inst' }, h('h2', { id: 'h-inst', text: 'Installed' }), h('ul', { class: 'rows' }, ...st.installed.map(installedRow)))
    : null;

  const search = h('input', { type: 'search', id: 'q', value: st.query, placeholder: 'Search by name or what it does', 'aria-label': 'Search plugins' });
  search.addEventListener('input', () => { st.query = search.value; st.shown = PAGE; drawList(); });
  const radio = (value, text) => {
    const input = h('input', { type: 'radio', name: 'filter', value, checked: st.filter === value });
    input.addEventListener('change', () => { st.filter = value; st.shown = PAGE; drawList(); });
    return h('label', { class: 'radio' }, input, text);
  };
  const tools = h('div', { class: 'tools' },
    h('fieldset', { class: 'filter' }, h('legend', { class: 'unseen', text: 'Which plugins' }), radio('anthropic', 'Anthropic'), radio('all', 'All')),
    button('Update catalog', 'catalog', () => catalog('PUT', 'Updating…')));
  const list = h('div', { id: 'list' });

  main.replaceChildren($('alerts'), ...status, ...(inst ? [inst] : []),
    h('section', { 'aria-labelledby': 'h-cat' }, h('h2', { id: 'h-cat', text: 'Catalog' }), search, tools, list));
  drawList();
}

function drawList() {
  const list = $('list');
  if (!list) {
    return;
  }
  const parts = [];
  if (st.filter === 'all') {
    parts.push(h('p', { class: 'small quiet', text: 'These include plugins that Anthropic did not write.' }));
  }
  if (st.filter === 'anthropic' && !st.hasOfficial) {
    parts.push(h('div', { class: 'notice' }, h('p', { text: 'Anthropic’s catalog is not added to Claude Code yet.' }),
      button('Get the catalog', 'catalog', () => catalog('POST', 'Adding…'), true)));
    list.replaceChildren(...parts);
    return;
  }
  const found = matches();
  parts.push(h('p', { class: 'small quiet', text: found.length ? `${count(found.length, 'plugin')}${found.length > st.shown ? `, showing ${st.shown.toLocaleString('en')}` : ''}` : 'No plugin matches.' }));
  parts.push(h('ul', { class: 'rows' }, ...found.slice(0, st.shown).map(catalogRow)));
  if (found.length > st.shown) {
    parts.push(h('button', { type: 'button', class: 'more', text: `Show ${Math.min(PAGE, found.length - st.shown)} more`, onclick: () => { st.shown += PAGE; drawList(); } }));
  }
  list.replaceChildren(...parts);
}

async function start() {
  try {
    await load();
    st.started = true;
    draw();
  } catch (e) {
    $('loading').hidden = e.banner;
    if (!e.banner) {
      $('loading').textContent = `Could not load the plugins: ${e.message} Reload the page to try again.`;
    }
  }
}

addEventListener('hashchange', async () => {
  await signIn();
  start();
});
addEventListener('focus', () => { if (st.signedOut) { start(); } });
signIn().then(start);
