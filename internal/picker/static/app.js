'use strict';

// Model text is untrusted: it only ever reaches the page through textContent.

const SMALL = 100000;
const CODEX = 'codex';
const AIMLAPI = 'aimlapi';
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

const app = {
  started: false, slots: [], codex: [], defaults: null, saved: null, draft: null,
  models: { status: 'loading', list: [], hidden: 0, error: '' },
  key: { status: 'loading', present: false, ok: false, detail: '' },
  endpoints: [], presets: [], // saved at once, not part of the draft
  lists: {}, // by endpoint id
  epKeys: { status: 'loading', has: {} },
  formOpen: () => false,
  desktop: false, offerDesktop: false, desktopResult: '', saving: false, saveError: '', signedOut: false,
};
let redraw = [];
const draw = () => redraw.forEach((f) => f());
const say = (t) => { $('say').textContent = t; };
const copy = (x) => JSON.parse(JSON.stringify(x));
const title = (s) => s.charAt(0).toUpperCase() + s.slice(1);
const same = (a, b) => a.provider === b.provider && a.model === b.model;
const label = (t) => t.name || t.model;
const endpoint = (id) => app.endpoints.find((e) => e.id === id);
const who = (t) => (t.provider === CODEX ? 'Codex' : t.provider === AIMLAPI ? 'AIMLAPI' : (endpoint(t.provider) || {}).name || 'an endpoint that is gone');
const custom = (t) => t.provider !== CODEX && t.provider !== AIMLAPI;
const target = (provider, model, name, context) => ({ provider, model, ...(name ? { name } : {}), ...(context ? { context } : {}) }); // the shape Go sends back
const equal = (a, b) => same(a, b) && (a.context || 0) === (b.context || 0);
const PROTOCOLS = { chat: 'OpenAI Chat Completions', responses: 'OpenAI Responses', anthropic: 'Anthropic Messages' };
const dot = (t) => (/[.!?]$/.test(t) ? t : `${t}.`);
const count = (n, one) => `${n} ${one}${n === 1 ? '' : 's'}`;

const leftOut = (n) => `${n} more left out: ${n === 1 ? 'its id is not usable here or repeats' : 'their ids are not usable here or repeat'}`;

function place(address) { // the host an address names, as it is written
  const m = /^https?:\/\/([^/?#\\\s]+)/i.exec(address);
  return m ? `${m[1]}${/^(localhost|127(\.\d+){3}|\[::1\])(:\d*)?$/i.test(m[1]) ? ' on this computer' : ''}` : '';
}

function tokens(n) { // rounds down: 99,999 must not read as 100k
  if (n >= 1e6) {
    return `${Math.floor(n / 1e4) / 100}M`;
  }
  return n >= 1000 ? `${Math.floor(n / 1000)}k` : String(n);
}

// ---- calls

async function call(method, path, body) {
  let r;
  try {
    r = await fetch(`api/${path}`, {
      method,
      redirect: 'error',
      credentials: 'same-origin',
      referrerPolicy: 'same-origin', // keeps Origin real in every browser
      headers: { 'X-Second-Life-Picker': '1', ...(body ? { 'Content-Type': 'application/json' } : {}) },
      body: body ? JSON.stringify(body) : undefined,
    });
  } catch {
    offline();
    throw Object.assign(new Error('The bridge is not running.'), { banner: true });
  }
  online();
  const data = await r.json().catch(() => ({}));
  if (r.status === 409) {
    return { ...data, conflict: true };
  }
  if (r.ok) {
    if (app.signedOut) {
      app.signedOut = false;
      unbanner('signed-out');
      say('Signed in again.');
      draw();
    }
    return data;
  }
  if (r.status === 401 && path !== 'session') {
    app.signedOut = true;
    banner('signed-out', app.started
      ? 'This page is no longer signed in. Open the model picker from the app menu again, then come back to this tab. Changes you have not saved are kept here.'
      : 'This page is not signed in: its link was used or is too old. Open the model picker from the app menu again.');
    draw();
    throw Object.assign(new Error('Not signed in.'), { banner: true });
  }
  throw Object.assign(new Error(data.error || `The bridge answered ${r.status}.`), { status: r.status });
}

function banner(id, text, button, act) {
  if ($(`b-${id}`)) {
    return;
  }
  $('alerts').append(h('div', { id: `b-${id}`, class: 'notice' }, h('p', { text }),
    button ? h('button', { type: 'button', text: button, onclick: act }) : null));
}
const unbanner = (id) => { const b = $(`b-${id}`); if (b) { b.remove(); } };

let retry = 0;
const ping = () => call('GET', 'state').then(() => { if (!app.started) { start(); } }).catch(() => {});

function offline() {
  banner('offline', 'The bridge is not running, so nothing can be loaded or saved. Start it from the app menu. This page reconnects by itself.', 'Try now', ping);
  clearTimeout(retry);
  retry = setTimeout(ping, 4000);
}

function online() {
  clearTimeout(retry);
  if ($('b-offline')) {
    unbanner('offline');
    app.saveError = '';
    draw();
    say('The bridge is running again.');
    if (app.started && app.models.status === 'error') {
      loadModels();
    }
    if (app.started && app.key.status === 'error') {
      loadKey();
    }
    if (app.started) {
      loadLists((l) => l.status === 'error');
    }
  }
}

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

async function start() {
  try {
    const s = await call('GET', 'state');
    Object.assign(app, { slots: s.slots, codex: s.codex, defaults: s.defaults, desktop: s.desktop, started: true });
    app.saved = { slots: s.config.slots || {}, extra: s.config.extra || [] };
    app.draft = copy(app.saved);
    app.endpoints = s.config.endpoints || [];
    app.presets = s.presets || [];
    build();
    loadModels();
    loadKey();
    loadLists(() => true);
    loadEndpointKeys();
  } catch (e) {
    $('loading').hidden = e.banner;
    if (!e.banner) {
      $('loading').textContent = `Could not load your settings: ${e.message} Reload the page to try again.`;
    }
  }
}

async function loadModels() {
  app.models = { status: 'loading', list: [], hidden: 0, error: '' };
  draw();
  try {
    const r = await call('GET', 'models');
    const byDate = (a, b) => (b.releasedAt || '').localeCompare(a.releasedAt || '') || a.name.localeCompare(b.name);
    app.models = { status: 'ok', hidden: r.hidden, error: '', list: r.models.sort(byDate).map((m) => ({
      target: target(AIMLAPI, m.id, m.name, m.context),
      developer: m.developer || '', description: m.description || '', outputMax: m.outputMax || 0,
      find: `${m.name} ${m.id} ${m.developer || ''}`.toLowerCase(),
    })) };
  } catch (e) {
    app.models = { status: 'error', list: [], hidden: 0, error: e.message };
  }
  draw();
}

async function loadKey() {
  app.key = { status: 'loading', present: false, ok: false, detail: '' };
  draw();
  try {
    app.key = { status: 'ok', ...(await call('GET', 'key')) };
  } catch (e) {
    app.key = { status: 'error', present: false, ok: false, detail: e.message };
  }
  draw();
}

async function loadList(id) {
  const mine = (app.lists[id] || { run: 0 }).run + 1;
  app.lists[id] = { status: 'loading', run: mine, list: [], hidden: 0, error: '' };
  draw();
  let got;
  try {
    const r = await call('POST', 'endpoint-models', { id });
    const name = (endpoint(id) || {}).name || '';
    got = { status: 'ok', run: mine, error: '', hidden: r.hidden, list: r.models.sort((a, b) => a.name.localeCompare(b.name)).map((m) => ({
      target: target(id, m.id, m.name === m.id ? '' : m.name, m.context),
      developer: m.developer || '', description: m.description || '', outputMax: m.outputMax || 0,
      find: `${m.name} ${m.id} ${m.developer || ''} ${name}`.toLowerCase(),
    })) };
  } catch (e) {
    got = { status: 'error', run: mine, list: [], hidden: 0, error: e.message };
  }
  if (app.lists[id] && app.lists[id].run === mine) {
    app.lists[id] = got;
    draw();
  }
}

function loadLists(which) {
  for (const e of app.endpoints) {
    if (which(app.lists[e.id] || { status: 'error' })) {
      loadList(e.id);
    }
  }
}

async function loadEndpointKeys() {
  if (!app.endpoints.length) {
    app.epKeys = { status: 'ok', has: {} };
    return;
  }
  try {
    app.epKeys = { status: 'ok', has: (await call('GET', 'endpoint-key')).keys };
  } catch {
    app.epKeys = { status: 'error', has: {} };
  }
  draw();
}

// took takes in a config the bridge saved at once and keeps the unsaved choices. One endpoint may have a new id.
function took(r, from) {
  const draft = app.draft;
  app.saved = { slots: r.config.slots, extra: r.config.extra || [] };
  app.endpoints = r.config.endpoints || [];
  for (const id of Object.keys(app.lists)) {
    if (!endpoint(id)) {
      delete app.lists[id];
    }
  }
  const move = (t) => (t.provider === from && r.id ? { ...t, provider: r.id } : t);
  const alive = (t) => !custom(t) || endpoint(t.provider);
  for (const s of app.slots) {
    const t = move(draft.slots[s] || app.defaults.slots[s]);
    draft.slots[s] = alive(t) ? t : copy(app.saved.slots[s]);
  }
  draft.extra = draft.extra.map(move).filter(alive);
  if ('key' in r) {
    app.epKeys.has[r.id] = r.key;
  }
}

// ---- draft

const clean = (c) => ({ slots: Object.fromEntries(app.slots.map((s) => [s, c.slots[s]])), extra: c.extra });
const dirty = () => JSON.stringify(clean(app.draft)) !== JSON.stringify(clean(app.saved));

function changes() {
  const slots = app.slots.filter((s) => !app.saved.slots[s] || !equal(app.saved.slots[s], app.draft.slots[s])).length;
  const added = app.draft.extra.filter((t) => !app.saved.extra.some((x) => same(x, t))).length;
  const gone = app.saved.extra.filter((t) => !app.draft.extra.some((x) => same(x, t))).length;
  const sized = app.saved.extra.filter((t) => app.draft.extra.some((x) => same(x, t) && !equal(x, t))).length;
  return slots + added + gone + sized;
}

async function save() {
  if (!dirty() || app.saving || app.signedOut) {
    return;
  }
  app.saving = true;
  app.saveError = '';
  draw();
  try {
    const sent = copy(app.draft);
    const r = await call('PUT', 'config', clean(sent));
    const untouched = JSON.stringify(clean(app.draft)) === JSON.stringify(clean(sent));
    app.saved = { slots: r.config.slots, extra: r.config.extra || [] };
    if (untouched) {
      app.draft = copy(app.saved);
    }
    app.desktop = r.desktop;
    app.offerDesktop = r.desktop;
    app.desktopResult = '';
    say('Saved.');
  } catch (e) {
    app.saveError = `Not saved: ${e.message}`;
    say(app.saveError);
  }
  app.saving = false;
  draw();
}

// ---- pieces

let boxes = 0; // for ids

// off greys a button but leaves it focusable, so the keyboard keeps its place.
function off(button, yes) {
  if (yes) {
    button.setAttribute('aria-disabled', 'true');
  } else {
    button.removeAttribute('aria-disabled');
  }
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

function info(t) {
  const list = t.provider === AIMLAPI ? app.models.list : custom(t) ? (app.lists[t.provider] || { list: [] }).list : [];
  return list.find((m) => m.target.model === t.model);
}

function notes(t) {
  const m = info(t);
  const size = t.context || (m && !custom(t) ? m.target.context : 0); // an endpoint's size is what is in its field
  const out = [];
  const parts = [t.provider === CODEX ? 'Codex, uses your ChatGPT limits.' : 'AIMLAPI, paid from your AIMLAPI balance.'];
  if (custom(t)) {
    parts[0] = `${dot(who(t))} Prompts, files and tool output go to ${place((endpoint(t.provider) || { baseUrl: '' }).baseUrl) || 'its address'}.`;
  }
  parts.push(size ? `Holds ${tokens(size)} tokens${m && m.outputMax ? `, writes up to ${tokens(m.outputMax)}` : ''}.` : custom(t) ? '' : 'Context size unknown.');
  out.push(h('p', { text: parts.join(' ').trim() }));
  if (m && m.description) {
    out.push(h('p', {}, h('bdi', { text: m.description })));
  }
  if (size && size < SMALL) {
    out.push(warning(`It holds ${tokens(size)} tokens, under 100k. That is small for coding: long sessions will not fit.`));
  }
  const l = app.lists[t.provider];
  if (custom(t) && !size) {
    out.push(warning('With no known context size the bridge cannot tell when a conversation gets too long for this model. Type the size if you know it.'));
  }
  if (custom(t) && l && l.status === 'ok' && l.list.length && !m) {
    out.push(warning(`${who(t)} does not list this model. Test it before you rely on it.`));
  }
  if (custom(t) && l && l.status === 'error') {
    out.push(warning(`Its models could not be listed: ${dot(l.error)}`));
  }
  if (t.provider === AIMLAPI && app.models.status === 'ok' && !m) {
    out.push(warning('AIMLAPI does not list this model now. Test it before you rely on it.'));
  }
  if (t.provider === AIMLAPI && app.key.status === 'ok' && !app.key.ok) {
    const go = h('button', { type: 'button', class: 'plain', text: app.key.present ? 'Replace the key' : 'Add a key', onclick: () => $('key').focus() });
    out.push(warning(app.key.present ? 'Your AIMLAPI key does not work, so this model will not answer.' : 'You have no AIMLAPI key, so this model will not answer.', go));
  }
  return out;
}

const warning = (text, ...more) => h('p', { class: 'alert' }, h('b', { text: 'Warning: ' }), text, ...more.flatMap((x) => [' ', x]));

// sizer is the field for the context size of an endpoint's model.
function sizer(name, get, set) {
  const id = `s${++boxes}`;
  const fault = h('p', { id: `${id}-fault`, class: 'alert', role: 'alert' });
  const input = h('input', { id, type: 'text', inputMode: 'numeric', autocomplete: 'off', placeholder: 'unknown, such as 128k', 'aria-describedby': fault.id });
  const el = h('div', { class: 'size-field' }, h('label', { htmlFor: id, text: 'Context size in tokens' }, h('span', { class: 'unseen', text: ` for ${name}` })), input, fault);
  const read = () => { // undefined is not a size
    const s = input.value.toLowerCase().replace(/[\s,_]/g, '');
    const m = /^(\d+(?:\.\d+)?)([km]?)$/.exec(s);
    const n = m ? Math.round(Number(m[1]) * { k: 1e3, m: 1e6, '': 1 }[m[2]]) : 0;
    return s === '' ? 0 : n >= 1000 && n <= 1e7 ? n : undefined;
  };
  const wrong = (yes) => {
    fault.textContent = yes ? 'Type a number from 1,000 to 10,000,000, such as 128000 or 128k.' : '';
    input.setAttribute('aria-invalid', String(yes));
  };
  input.addEventListener('input', () => {
    const n = read();
    if (n !== undefined) {
      wrong(false);
      set(n);
      draw();
    }
  });
  input.addEventListener('change', () => wrong(read() === undefined));
  let shown = null;
  return { el, update() {
    const t = get();
    el.hidden = !custom(t);
    const typing = document.activeElement === input || input.getAttribute('aria-invalid') === 'true';
    if (!shown || !same(shown, t) || !(typing || read() === (t.context || 0))) {
      wrong(false);
      input.value = t.context || '';
    }
    shown = copy(t);
  } };
}

// tester sends one tiny request to the chosen model, saved or not.
function tester(name, get) {
  const result = h('p', { role: 'status' });
  const cost = h('p', { text: 'A test costs a fraction of a cent.' });
  cost.id = `cost${++boxes}`;
  let run = 0;
  let last = null;
  const button = h('button', { type: 'button', text: 'Test', 'aria-label': `Test ${name}`, onclick: async () => {
    if (button.hasAttribute('aria-disabled')) {
      return;
    }
    const t = get();
    const mine = ++run;
    off(button, true);
    button.textContent = 'Testing…';
    result.className = '';
    result.textContent = `Waiting for ${label(t)}…`;
    let ok = false;
    let text;
    try {
      const r = await call('POST', 'test', t);
      ok = r.ok;
      text = ok ? `Answered in ${r.ms} ms.${r.detail ? ` ${r.detail}` : ''}` : `No answer: ${r.detail || 'unknown error'}`;
    } catch (e) {
      text = `Test failed: ${e.message}`;
    }
    if (mine !== run) {
      return;
    }
    idle();
    result.className = ok ? '' : 'alert';
    result.textContent = text;
  } });
  const idle = () => {
    off(button, false);
    button.textContent = 'Test';
  };
  return { button, result, cost, update() {
    const t = get();
    if (last && !same(last, t)) { // the result was about another model
      run++;
      idle();
      result.textContent = '';
    }
    last = t;
    cost.hidden = t.provider === CODEX;
    const text = custom(t) ? `A test sends one short request to ${who(t)}.` : 'A test costs a fraction of a cent.';
    if (cost.textContent !== text) {
      cost.textContent = text;
    }
    button.setAttribute('aria-describedby', cost.hidden ? '' : cost.id); // hidden text would still be read
  } };
}

const USABLE = /^[A-Za-z0-9][A-Za-z0-9._:/[\]-]{0,127}$/; // as in package route

// combo is an editable ARIA combobox with a grouped list. Focus stays in the input.
function combo({ name, placeholder, groups, value, pick, has }) {
  const id = `c${++boxes}`;
  const list = h('div', { id: `${id}-list`, role: 'listbox', class: 'list', 'aria-label': name });
  const note = h('p', { class: 'note' });
  const popup = h('div', { class: 'popup', hidden: true }, list, note);
  const input = h('input', {
    id, type: 'text', role: 'combobox', autocomplete: 'off', spellcheck: false, placeholder: placeholder || '', 'aria-label': name,
    'aria-autocomplete': 'list', 'aria-expanded': 'false', 'aria-controls': `${id}-list`, autocapitalize: 'off',
  });
  const el = h('div', { class: 'combo' }, input, popup);
  let open = false;
  let typed = false;
  let items = [];
  let at = -1;

  const shown = () => { const v = value(); return v ? label(v) : ''; };

  function mark(i) {
    if (items[at]) {
      items[at].el.classList.remove('on');
    }
    at = i;
    const it = items[i];
    if (!it) {
      input.removeAttribute('aria-activedescendant');
      return;
    }
    it.el.classList.add('on');
    input.setAttribute('aria-activedescendant', it.el.id);
    it.el.scrollIntoView({ block: 'nearest' });
  }

  function fill() {
    const words = typed ? input.value.toLowerCase().split(/\s+/).filter(Boolean) : [];
    const v = value();
    items = [];
    const parts = [];
    let listed = 0;
    const all = groups();
    const own = typed ? input.value.trim() : '';
    all.forEach((g, gi) => {
      const hits = g.items.filter((m) => words.every((w) => m.find.includes(w)));
      listed += hits.length;
      if (g.free && USABLE.test(own) && !g.known(own)) {
        hits.push({ target: target(g.free, own), free: g.name });
      }
      if (!hits.length) {
        return;
      }
      const head = h('div', { id: `${id}-g${gi}`, class: 'group' }, g.name, h('span', { text: ` ${g.hint}` }));
      const box = h('div', { role: 'group', 'aria-labelledby': head.id }, head);
      for (const m of hits) {
        const t = m.target;
        const chosen = Boolean(v && same(v, t));
        const i = items.length;
        const option = h('div', { id: `${id}-o${i}`, role: 'option', class: 'option', 'aria-selected': String(chosen) },
          h('bdi', { class: 'name', text: m.free ? `Use the id “${own}”` : label(t) }),
          h('span', { class: 'size', text: t.context ? `${tokens(t.context)} tokens` : 'size unknown' }),
          h('bdi', { class: 'more', text: m.free ? `Not in the list of ${m.free}` : [t.name && t.model, m.developer].filter(Boolean).join(' · ') }));
        option.addEventListener('click', () => take(i));
        items.push({ el: option, target: t, chosen });
        box.append(option);
      }
      parts.push(box);
    });
    list.replaceChildren(...parts);
    const tell = [];
    if (!listed) {
      tell.push(words.length ? `No model matches “${input.value.trim()}”.` : 'No models to choose.');
    }
    if (app.models.status === 'loading') {
      tell.push('AIMLAPI models are loading…');
    } else if (app.models.status === 'error') {
      tell.push('AIMLAPI models could not be loaded.');
    } else if (!app.models.list.length) {
      tell.push('AIMLAPI lists no models now.');
    }
    tell.push(...all.map((g) => g.tell).filter(Boolean));
    if (own && has && !items.some((i) => i.target.model.toLowerCase() === own.toLowerCase()) && has({ model: own })) {
      tell.push(`“${own}” is an extra model already.`);
    }
    note.textContent = tell.join(' ');
    note.hidden = !tell.length;
    const start = words.length ? 0 : Math.max(0, items.findIndex((i) => i.chosen));
    at = -1;
    mark(items.length ? start : -1);
    if (typed) {
      say(`${count(listed, 'model')}${items.length > listed ? '. You can also use the id as typed' : ''}`);
    }
  }

  function show() {
    if (open) {
      return;
    }
    open = true;
    popup.hidden = false;
    input.setAttribute('aria-expanded', 'true');
    const box = input.getBoundingClientRect();
    const tall = (window.visualViewport || {}).height || innerHeight;
    const up = tall - box.bottom < 300 && box.top > tall - box.bottom;
    popup.classList.toggle('up', up);
    list.style.maxHeight = `${Math.min(352, Math.max(160, (up ? box.top : tall - box.bottom) - 16))}px`;
    fill();
  }

  function hide() {
    open = false;
    typed = false;
    popup.hidden = true;
    list.replaceChildren();
    items = [];
    at = -1;
    input.setAttribute('aria-expanded', 'false');
    input.removeAttribute('aria-activedescendant');
    input.value = shown();
  }

  function take(i) {
    const it = items[i];
    if (!it) {
      return;
    }
    hide();
    pick(it.target);
    input.value = shown();
    input.select();
  }

  popup.addEventListener('mousedown', (e) => e.preventDefault()); // keeps focus in the input
  input.addEventListener('focus', () => input.select()); // typing replaces the name
  input.addEventListener('click', () => { if (!open) { show(); input.select(); } });
  input.addEventListener('input', () => { typed = true; if (open) { fill(); } else { show(); } });
  input.addEventListener('blur', hide);
  input.addEventListener('keydown', (e) => {
    if (e.isComposing) {
      return;
    }
    const step = { ArrowDown: 1, ArrowUp: -1, PageDown: 8, PageUp: -8 }[e.key];
    if (step) {
      e.preventDefault();
      if (!open) {
        show();
        input.select();
      } else if (items.length) {
        const n = items.length;
        mark(Math.abs(step) === 1 ? (at + step + n) % n : Math.min(n - 1, Math.max(0, at + step)));
      }
    } else if (e.key === 'Enter' && open) {
      e.preventDefault();
      take(at);
    } else if (e.key === 'Escape' && open) {
      e.preventDefault();
      hide();
      input.select();
    }
  });
  return { el, input, update() {
    if (open) {
      const keep = items[at];
      fill();
      const i = keep ? items.findIndex((x) => same(x.target, keep.target)) : -1;
      if (i >= 0) {
        mark(i);
      }
    } else {
      input.value = shown();
    }
  } };
}

const codexGroup = () => ({ name: 'Codex', hint: 'your ChatGPT limits', items: app.codex.map((t) => ({ target: t, developer: '', find: `${t.name} ${t.model} codex openai`.toLowerCase() })) });
const aimlapiGroup = (skip) => ({ name: 'AIMLAPI', hint: 'paid from your AIMLAPI balance', items: skip ? app.models.list.filter((m) => !skip(m.target)) : app.models.list });

function endpointGroups(skip) {
  return app.endpoints.map((e) => {
    const l = app.lists[e.id] || { status: 'loading', list: [] };
    const tell = { loading: 'models are loading…', error: 'models could not be loaded; type a model id.', ok: l.list.length ? '' : 'no models listed; type a model id.' }[l.status];
    return {
      name: e.name, hint: place(e.baseUrl), tell: tell && `${e.name}: ${tell}`, free: e.id,
      items: skip ? l.list.filter((m) => !skip(m.target)) : l.list,
      known: (model) => l.list.some((m) => m.target.model === model) || Boolean(skip && skip({ model })),
    };
  });
}

function slotRow(slot) {
  const name = title(slot);
  const get = () => app.draft.slots[slot] || app.defaults.slots[slot];
  const box = combo({
    name: `Model that answers for ${name}`, value: get,
    groups: () => [codexGroup(), aimlapiGroup(), ...endpointGroups()],
    pick: (t) => { app.draft.slots[slot] = copy(t); say(`${name}: ${label(t)}. Not saved yet.`); draw(); },
  });
  const t = tester(name, get);
  const size = sizer(name, get, (n) => {
    const v = get();
    app.draft.slots[slot] = target(v.provider, v.model, v.name, n);
  });
  const now = h('p', { class: 'now' });
  const meta = h('div');
  const el = h('li', { class: 'row slot' },
    h('div', { class: 'who' }, h('h3', { text: name }), now),
    h('div', { class: 'link', 'aria-hidden': 'true' }, h('i'), h('i')),
    h('div', { class: 'pick' }, box.el, h('div', { class: 'meta' }, meta, size.el, t.cost, t.result)),
    h('div', { class: 'try' }, t.button));
  return { el, update() {
    const saved = app.saved.slots[slot];
    const changed = !saved || !equal(saved, get());
    el.classList.toggle('changed', changed);
    now.replaceChildren('Now: ', h('bdi', { text: saved ? label(saved) : 'nothing' }), changed ? '. Change not saved.' : '');
    meta.replaceChildren(...notes(get()));
    box.update();
    t.update();
    size.update();
  } };
}

function extraRow(target, remove) {
  const t = tester(label(target), () => target);
  const get = () => app.draft.extra.find((x) => same(x, target)) || target;
  const size = sizer(label(target), get, (n) => {
    const v = get();
    if (n) {
      v.context = n;
    } else {
      delete v.context;
    }
  });
  const meta = h('div');
  const el = h('li', { class: 'row extra' },
    h('div', {}, h('p', { class: 'name' }, h('bdi', { text: label(target) })), h('code', { text: target.model }),
      h('div', { class: 'meta' }, meta, size.el, t.cost, t.result)),
    h('div', { class: 'actions' }, t.button,
      h('button', { type: 'button', text: 'Remove', 'aria-label': `Remove ${label(target)}`, onclick: remove })));
  return { el, target, update() {
    const more = notes(get());
    const id = target.model.toLowerCase();
    const hit = app.slots.find((s) => id.includes(s));
    if (target.provider === AIMLAPI && !id.includes('/') && (hit || app.codex.some((c) => c.model === id))) {
      more.push(warning(`Requests for ${target.model} will go to AIMLAPI${hit ? ` instead of your ${title(hit)} choice` : ' instead of Codex'}.`));
    }
    if (custom(target) && !id.includes('/') && (hit || id.startsWith('gpt-'))) {
      more.push(warning(`Requests for ${target.model} will go to ${who(target)}${hit ? ` instead of your ${title(hit)} choice` : ' instead of Codex'}.`));
    }
    if (!app.saved.extra.some((x) => equal(x, get()))) {
      more.unshift(h('p', {}, h('b', { text: 'Not saved yet.' })));
    }
    meta.replaceChildren(...more);
    t.update();
    size.update();
  } };
}

function extrasSection() {
  const list = h('ul', { class: 'rows' });
  const empty = h('p', { class: 'quiet', text: 'No extra models.' });
  let rows = [];
  const has = (t) => app.draft.extra.some((x) => x.model.toLowerCase() === t.model.toLowerCase());
  const full = h('p', { class: 'quiet', text: 'You have 100 extra models, the most this page keeps.' });
  const box = combo({
    name: 'Add a model', placeholder: 'Search by name, id or developer', value: () => null, has,
    groups: () => [aimlapiGroup(has), ...endpointGroups(has)],
    pick: (t) => { app.draft.extra.push(copy(t)); say(`${label(t)} added. Not saved yet.`); draw(); box.input.scrollIntoView({ block: 'nearest' }); },
  });
  const add = h('div', { class: 'add' }, h('label', { htmlFor: box.input.id, text: 'Add a model' }), box.el);
  const el = h('section', { 'aria-labelledby': 'h-extra' },
    h('div', { class: 'head' }, h('h2', { id: 'h-extra', text: 'Extra models' })),
    h('p', { class: 'quiet', text: 'Models from AIMLAPI or your endpoints that you can ask for by their own id, next to the four above.' }),
    list, empty, add, full);

  function remove(target) {
    const i = app.draft.extra.findIndex((x) => same(x, target));
    app.draft.extra.splice(i, 1);
    say(`${label(target)} removed. Not saved yet.`);
    draw();
    const next = rows[Math.min(i, rows.length - 1)];
    (next ? next.el.querySelector('.actions button:last-child') : box.input).focus();
  }
  return { el, update() {
    rows = app.draft.extra.map((t) => rows.find((r) => same(r.target, t)) || extraRow(t, () => remove(t)));
    const els = rows.map((r) => r.el);
    if (els.length !== list.children.length || els.some((x, i) => x !== list.children[i])) {
      list.replaceChildren(...els);
    }
    rows.forEach((r) => r.update());
    empty.hidden = rows.length > 0;
    list.hidden = rows.length === 0;
    add.hidden = rows.length >= 100;
    full.hidden = !add.hidden;
    box.update();
  } };
}

function keySection() {
  const state = h('p', { role: 'status' });
  const again = h('button', { type: 'button', class: 'plain', text: 'Check again', onclick: loadKey });
  const lab = h('label', { htmlFor: 'key' });
  const input = h('input', { id: 'key', type: 'password', autocomplete: 'off', spellcheck: false, autocapitalize: 'off', 'aria-describedby': 'key-note' });
  const fault = h('p', { class: 'alert', role: 'alert' });
  let busy = false;

  async function change(method, body, done) {
    if (busy) {
      return;
    }
    busy = true;
    fault.textContent = '';
    draw();
    try {
      app.key = { status: 'ok', ...(await call(method, 'key', body)) };
      input.value = '';
      say(done);
    } catch (e) {
      fault.textContent = `${method === 'PUT' ? 'Key not saved' : 'Key not deleted'}: ${e.message}`;
    }
    busy = false;
    draw();
  }
  function put() {
    if (!input.value.trim()) {
      fault.textContent = 'Paste a key first.';
      input.focus();
      return;
    }
    change('PUT', { key: input.value }, 'Key saved.');
  }
  const ok = h('button', { type: 'button', text: 'Save key', onclick: put });
  const del = h('button', { type: 'button', text: 'Delete key', onclick: async () => {
    const used = app.slots.filter((s) => (app.saved.slots[s] || {}).provider === AIMLAPI).map(title);
    const extra = app.saved.extra.filter((t) => t.provider === AIMLAPI).length;
    if (extra) {
      used.push(extra === 1 ? '1 extra model' : `${extra} extra models`);
    }
    const text = used.length ? `These use AIMLAPI and will stop answering: ${used.join(', ')}.` : 'No saved model uses it now.';
    if (await ask('Delete the AIMLAPI key?', `${text} The bridge forgets the key; to use AIMLAPI again you paste it again.`, 'Delete key')) {
      await change('DELETE', undefined, 'Key deleted.');
      input.focus();
    }
  } });
  input.addEventListener('keydown', (e) => { if (e.key === 'Enter') { put(); } });
  const el = h('section', { 'aria-labelledby': 'h-key' },
    h('div', { class: 'head' }, h('h2', { id: 'h-key', text: 'AIMLAPI key' }), again),
    state,
    h('div', { class: 'field' }, h('div', {}, lab), input, ok, del),
    h('p', { id: 'key-note', class: 'quiet small', text: 'Saved at once. The bridge keeps it and never shows it again.' }), fault);
  return { el, update() {
    const k = app.key;
    const bad = k.status === 'error' || (k.status === 'ok' && k.present && !k.ok);
    state.className = bad ? 'alert' : '';
    let text = 'Checking the key…';
    if (k.status === 'error') {
      text = `Could not check the key: ${k.detail}`;
    } else if (k.status === 'ok' && !k.present) {
      text = 'No key saved. You need one only for AIMLAPI models.';
    } else if (k.status === 'ok' && k.ok) {
      text = 'A key is saved and works.';
    } else if (k.status === 'ok') {
      text = k.detail ? `A key is saved, but it does not work: ${k.detail}` : 'A key is saved, but AIMLAPI rejected it.';
    }
    if (state.textContent !== text) {
      state.textContent = text;
    }
    lab.textContent = k.present ? 'Replace the key' : 'Paste your key';
    del.hidden = !k.present;
    again.hidden = k.status === 'loading' || (k.status === 'ok' && !k.present);
    ok.disabled = busy || app.signedOut;
    del.disabled = busy || app.signedOut;
  } };
}

// uses are the choices on an endpoint, saved or not.
function uses(id) {
  const on = (c) => ({ slots: app.slots.filter((s) => (c.slots[s] || {}).provider === id), extra: c.extra.filter((t) => t.provider === id) });
  const now = on(app.saved);
  const next = on(app.draft);
  const slots = app.slots.filter((s) => now.slots.includes(s) || next.slots.includes(s));
  const extra = [...now.extra, ...next.extra.filter((t) => !now.extra.some((x) => same(x, t)))].length;
  return { slots, extra, text: [...slots.map(title), ...(extra ? [count(extra, 'extra model')] : [])].join(', ') };
}

async function removeEndpoint(e, after) {
  const u = uses(e.id);
  let text = 'No Claude name uses it. Its address and its key are forgotten.';
  if (u.text) {
    text = `It is used by: ${u.text}.`;
    if (u.slots.length) {
      text += ` ${u.slots.map((s) => `${title(s)} goes to ${label(app.defaults.slots[s])}`).join(', ')}.`;
    }
    if (u.extra) {
      text += ` ${u.extra === 1 ? 'The extra model is' : 'The extra models are'} removed.`;
    }
    text += ' Its address and its key are forgotten. This happens at once.';
  }
  if (!await ask(`Remove ${e.name}?`, text, u.text ? 'Move and remove' : 'Remove')) {
    return;
  }
  try {
    const r = await call('DELETE', 'endpoint', { id: e.id, move: Boolean(u.text) });
    if (r.conflict) {
      throw new Error('another window just chose one of its models. Reload this page.');
    }
    const before = JSON.stringify(app.saved);
    took(r);
    if (before !== JSON.stringify(app.saved)) {
      app.desktop = r.desktop;
      app.offerDesktop = r.desktop;
    }
    say(`${e.name} removed.`);
    draw();
    after('');
  } catch (x) {
    after(`Not removed: ${dot(x.message)}`);
    draw();
  }
}

function endpointRow(id, edit, removed) {
  const name = h('bdi');
  const address = h('code');
  const kind = h('span');
  const state = h('p', { role: 'status' });
  const key = h('p');
  const used = h('p');
  const fault = h('p', { class: 'alert', role: 'alert' });
  const check = h('button', { type: 'button', text: 'Check', onclick: () => { if (!check.hasAttribute('aria-disabled')) { loadList(id); } } });
  const change = h('button', { type: 'button', text: 'Edit', onclick: () => edit(id, change) });
  const gone = h('button', { type: 'button', text: 'Remove', onclick: () => removeEndpoint(endpoint(id), (text) => { fault.textContent = text; if (!text) { removed(); } }) });
  const slot = h('div', { class: 'form-slot' });
  const el = h('li', { class: 'row extra' },
    h('div', {}, h('p', { class: 'name' }, name), h('p', { class: 'where' }, address, ' ', kind), h('div', { class: 'meta' }, state, key, used, fault)),
    h('div', { class: 'actions' }, check, change, gone), slot);
  return { el, id, slot, change, check, update() {
    const e = endpoint(id);
    const l = app.lists[id] || { status: 'loading', list: [], hidden: 0 };
    name.textContent = e.name;
    address.textContent = e.baseUrl;
    kind.textContent = PROTOCOLS[e.protocol];
    let text = 'Asking for its models…';
    if (l.status === 'error') {
      text = `Its models could not be listed: ${dot(l.error)} You can still type model ids.`;
    } else if (l.status === 'ok') {
      text = l.list.length ? `${count(l.list.length, 'model')} listed.` : 'It answers but lists no usable models. Type model ids where you choose a model.';
      if (l.hidden) {
        text += ` ${leftOut(l.hidden)}.`;
      }
    }
    state.className = l.status === 'error' ? 'alert' : '';
    if (state.textContent !== text) {
      state.textContent = text;
    }
    key.textContent = { loading: 'Looking for its key…', error: 'Could not find out whether it has a key.', ok: app.epKeys.has[id] ? 'A key is saved.' : 'No key.' }[app.epKeys.status];
    const u = uses(id);
    used.hidden = !u.text;
    used.textContent = `Used by: ${u.text}.`;
    off(check, l.status === 'loading' || app.signedOut);
    for (const [b, verb] of [[check, 'Check'], [change, 'Edit'], [gone, 'Remove']]) {
      b.setAttribute('aria-label', `${verb} ${e.name}`);
    }
  } };
}

// endpointForm adds an endpoint, or edits the one it is opened for.
function endpointForm(closed) {
  let id = '';
  let busy = '';
  let first = '';
  let run = 0;
  const head = h('h3');
  const name = h('input', { id: 'ep-name', type: 'text', autocomplete: 'off', maxLength: 60 });
  const address = h('input', { id: 'ep-address', type: 'text', inputMode: 'url', autocomplete: 'off', spellcheck: false, autocapitalize: 'off', maxLength: 300, 'aria-describedby': 'ep-address-note' });
  const goes = h('p', { id: 'ep-address-note', class: 'quiet small' });
  const radios = Object.entries(PROTOCOLS).map(([value, text]) => {
    const input = h('input', { type: 'radio', name: 'ep-protocol', value });
    return { input, el: h('label', { class: 'radio' }, input, text) };
  });
  const key = h('input', { id: 'ep-key', type: 'password', autocomplete: 'off', spellcheck: false, autocapitalize: 'off', maxLength: 600, 'aria-describedby': 'ep-key-note' });
  const keyLabel = h('label', { htmlFor: 'ep-key' });
  const keyNote = h('p', { id: 'ep-key-note', class: 'quiet small', role: 'status' });
  const forget = h('button', { type: 'button', class: 'plain', text: 'Delete the saved key', onclick: async () => {
    if (busy || !await ask('Delete this key?', `${endpoint(id).name} is asked without a key until you type one again.`, 'Delete key')) {
      return;
    }
    work('forget', async () => {
      took(await call('DELETE', 'endpoint-key', { id }));
      app.epKeys.has[id] = false;
      say('Key deleted.');
      key.focus();
    }, 'Key not deleted');
  } });
  const fault = h('p', { class: 'alert', role: 'alert' });
  const result = h('p', { role: 'status' });
  const fill = h('div', { class: 'presets', role: 'group', 'aria-label': 'Fill in the form for' }, h('span', { class: 'quiet small', 'aria-hidden': 'true', text: 'Fill in for' }),
    ...app.presets.map((p) => h('button', { type: 'button', text: p.name, onclick: () => {
      name.value = p.name;
      address.value = p.baseUrl;
      radios.forEach((x) => { x.input.checked = x.input.value === p.protocol; });
      typed();
      say(`Filled in for ${p.name}. Nothing is saved yet.`);
    } })));
  const check = h('button', { type: 'button', text: 'Check', onclick: tryIt });
  const ok = h('button', { type: 'button', class: 'main', onclick: put });
  const cancel = h('button', { type: 'button', class: 'plain', text: 'Cancel', onclick: async () => {
    if (!changed() || await ask('Discard what you typed?', 'The form has changes that are not saved.', 'Discard')) {
      shut();
      closed('');
    }
  } });
  const el = h('div', { class: 'form', role: 'group', 'aria-labelledby': 'ep-head' }, head, fill,
    h('div', {}, h('label', { htmlFor: 'ep-name', text: 'Name' }), name),
    h('div', {}, h('label', { htmlFor: 'ep-address', text: 'Address, up to the version' }), address, goes),
    h('fieldset', {}, h('legend', { text: 'What it speaks' }), ...radios.map((x) => x.el)),
    h('div', {}, keyLabel, key, keyNote, forget),
    h('div', { class: 'actions' }, ok, check, cancel), fault, result);
  head.id = 'ep-head';

  const protocol = () => (radios.find((x) => x.input.checked) || { input: { value: '' } }).input.value;
  const base = () => address.value.trim().replace(/\/+$/, '');
  const moved = () => { const e = endpoint(id); return Boolean(e) && (e.baseUrl !== base() || e.protocol !== protocol()); };
  const now = () => JSON.stringify([name.value, address.value, protocol(), key.value]);
  const changed = () => el.isConnected && now() !== first;

  function shut() {
    run++;
    key.value = '';
    el.remove();
    id = '';
  }

  function typed() { // an old result is about something else
    run++;
    if (busy === 'check') {
      busy = '';
    }
    result.textContent = '';
    fault.textContent = '';
    update();
  }

  function missing(forCheck) {
    for (const [empty, input, text] of [[!forCheck && !name.value.trim(), name, 'Give it a name.'], [!base(), address, 'Type its address.'], [!protocol(), radios[0].input, 'Choose what it speaks.']]) {
      if (empty) {
        result.textContent = '';
        fault.textContent = text;
        input.focus();
        return true;
      }
    }
    return false;
  }

  async function work(what, f, failed) {
    const mine = ++run;
    busy = what;
    let status = 0;
    fault.textContent = '';
    result.textContent = '';
    draw();
    try {
      await f(() => mine === run);
    } catch (e) {
      if (mine === run) {
        fault.textContent = `${failed}: ${dot(e.message)}`;
        status = e.status;
      }
    }
    if (mine === run) {
      busy = '';
    }
    draw();
    if (el.isConnected) {
      (fault.textContent ? fault : result).scrollIntoView({ block: 'nearest' });
    }
    return status;
  }

  function tryIt() {
    if (busy || missing(true)) {
      return;
    }
    const typedKey = key.value.trim() ? { key: key.value } : {};
    const body = id && !moved() ? { id, ...typedKey } : { baseUrl: base(), protocol: protocol(), ...typedKey }; // by id alone, the saved key may be used
    work('check', async (current) => {
      result.textContent = `Asking ${place(base()) || 'it'} for its models…`;
      const r = await call('POST', 'endpoint-models', body);
      if (current()) {
        result.textContent = r.models.length
          ? `It answers and lists ${count(r.models.length, 'model')}${r.hidden ? `. ${leftOut(r.hidden)}` : ''}.`
          : 'It answers but lists no usable models. Save it and type model ids where you choose a model.';
      }
    }, 'Check failed').then((status) => {
      if (status === 502) { // the server, not what was typed
        result.textContent = 'If it has no list, you can save it anyway and type model ids where you choose a model.';
      }
    });
  }

  function put() {
    if (busy || app.signedOut || missing(false)) {
      return;
    }
    const was = id;
    const body = { name: name.value, baseUrl: base(), protocol: protocol(), ...(key.value.trim() ? { key: key.value } : {}) };
    work('save', async () => {
      const r = await call(was ? 'PUT' : 'POST', 'endpoint', was ? { id: was, ...body } : body);
      took(r, was);
      shut();
      loadList(r.id);
      say(`${body.name.trim()} saved.`);
      closed(r.id);
    }, 'Not saved').then(() => { // the key may be gone although the rest is as it was
      if (was && fault.textContent) {
        loadEndpointKeys();
      }
    });
  }

  for (const x of [name, address, key]) {
    x.addEventListener('keydown', (e) => { if (e.key === 'Enter' && !e.isComposing) { put(); } });
    x.addEventListener('input', typed);
  }
  radios.forEach((x) => x.input.addEventListener('change', typed));

  function update() {
    const e = endpoint(id);
    if (id && !e) { // removed
      shut();
    }
    if (!el.isConnected) {
      return;
    }
    head.textContent = e ? `Edit ${e.name}` : 'New endpoint';
    fill.hidden = Boolean(e);
    ok.textContent = busy === 'save' ? 'Saving…' : 'Save endpoint';
    check.textContent = busy === 'check' ? 'Checking…' : 'Check';
    off(ok, busy || app.signedOut);
    off(check, busy || app.signedOut);
    const to = place(address.value.trim());
    goes.textContent = `Claude’s prompts, files and tool output go to ${to || 'this address'}. Plain http is allowed only for this computer.`;
    const has = Boolean(e) && app.epKeys.has[id];
    keyLabel.textContent = has && !moved() ? 'Replace its key (optional)' : 'Key (optional)';
    forget.hidden = !has || moved();
    const text = has && moved()
      ? 'The address or what it speaks changed. Saving deletes the saved key, which was for the old one. Type a key again if this one needs it.'
      : 'The bridge keeps the key in the system’s key store, sends it only to this address and never shows it again.';
    keyNote.classList.toggle('alert', has && moved());
    if (keyNote.textContent !== text) {
      keyNote.textContent = text;
    }
  }
  return { el, update, changed, editing: () => id, open(where, endpointId) {
    run++;
    busy = '';
    id = endpointId;
    const e = endpoint(id) || { name: '', baseUrl: '', protocol: '' };
    name.value = e.name;
    address.value = e.baseUrl;
    key.value = '';
    radios.forEach((x) => { x.input.checked = x.input.value === e.protocol; });
    fault.textContent = '';
    result.textContent = '';
    where.append(el);
    first = now();
    draw();
    name.focus();
    el.scrollIntoView({ block: 'nearest' });
  } };
}

function endpointsSection() {
  const list = h('ul', { class: 'rows' });
  const empty = h('p', { class: 'quiet', text: 'No endpoints.' });
  const full = h('p', { class: 'quiet', text: 'You have 20 endpoints, the most this page keeps.' });
  const here = h('div');
  let rows = [];
  const form = endpointForm((id) => {
    update();
    const row = rows.find((r) => r.id === (id || opened));
    (row ? row.change : add).focus();
  });
  let opened = '';
  async function open(where, id) {
    if (form.changed() && (form.editing() === id || !await ask('Discard what you typed?', 'The form has changes that are not saved.', 'Discard'))) {
      return;
    }
    opened = id;
    form.open(where, id);
  }
  const add = h('button', { type: 'button', text: 'Add an endpoint', onclick: () => open(here, '') });
  const el = h('section', { 'aria-labelledby': 'h-ep' },
    h('div', { class: 'head' }, h('h2', { id: 'h-ep', text: 'Your endpoints' }), add),
    h('p', { class: 'quiet', text: 'Other servers whose models you can choose above. When Claude uses one of them, its prompts, files and tool output go to that address. Endpoints are saved at once.' }),
    list, empty, full, here);

  function update() {
    rows = app.endpoints.map((e) => rows.find((r) => r.id === e.id) || endpointRow(e.id, (id) => open(rows.find((r) => r.id === id).slot, id), () => add.focus()));
    const els = rows.map((r) => r.el);
    if (els.length !== list.children.length || els.some((x, i) => x !== list.children[i])) {
      list.replaceChildren(...els);
    }
    rows.forEach((r) => r.update());
    empty.hidden = rows.length > 0;
    list.hidden = rows.length === 0;
    full.hidden = rows.length < 20;
    off(add, rows.length >= 20);
    form.update();
  }
  app.formOpen = form.changed;
  return { el, update };
}

function modelsNotice() {
  const p = h('p');
  const again = h('button', { type: 'button', text: 'Load again', onclick: loadModels });
  const el = h('div', { class: 'notice', role: 'status' }, p, again);
  return { el, update() {
    const m = app.models;
    let text = '';
    if (m.status === 'error') {
      text = `AIMLAPI models could not be loaded: ${m.error} Codex models still work.`.replace(/([^.!?:]) Codex models/, '$1. Codex models');
    } else if (m.status === 'ok' && !m.list.length) {
      text = 'AIMLAPI lists no models now. Codex models still work.';
    } else if (m.status === 'ok' && m.hidden) {
      text = `${m.hidden} AIMLAPI ${m.hidden === 1 ? 'model is' : 'models are'} left out: their ids are not usable here or repeat.`;
    }
    el.hidden = !text;
    again.hidden = m.status === 'ok' && m.list.length > 0;
    el.className = again.hidden ? 'quiet small' : 'notice';
    if (p.textContent !== text) {
      p.textContent = text;
    }
  } };
}

function desktopNotice() {
  const p = h('p', { tabIndex: -1 });
  let busy = false;
  const go = h('button', { type: 'button', text: 'Update Claude Desktop', onclick: async () => {
    if (busy || !await ask('Restart Claude Desktop?', 'Claude will restart and open sessions in it will stop.', 'Restart and update')) {
      return;
    }
    busy = true;
    app.desktopResult = 'Updating Claude Desktop…';
    draw();
    try {
      await call('POST', 'desktop', { confirm: true });
      app.desktopResult = 'Claude Desktop is updated.';
      app.offerDesktop = false;
    } catch (e) {
      app.desktopResult = `Claude Desktop is not updated: ${e.message}`;
    }
    busy = false;
    draw();
    if (go.hidden) {
      p.focus();
    }
  } });
  const el = h('div', { role: 'status' }, p, go);
  return { el, update() {
    el.hidden = dirty() || (!app.offerDesktop && !app.desktopResult);
    go.hidden = !app.offerDesktop;
    off(go, busy);
    const text = app.desktopResult || 'Claude Desktop does not show this yet. Updating it restarts Claude and stops its open sessions.';
    if (p.textContent !== text) {
      p.textContent = text;
    }
  } };
}

function saveBar() {
  const p = h('p');
  const back = h('button', { type: 'button', class: 'plain', text: 'Undo changes', onclick: () => {
    app.draft = copy(app.saved);
    app.saveError = '';
    say('Changes undone.');
    draw();
    document.querySelector('.slot input').focus({ preventScroll: true });
  } });
  const ok = h('button', { type: 'button', class: 'main', text: 'Save', onclick: save });
  const el = h('div', {}, p, h('div', { class: 'actions' }, back, ok));
  return { el, update() {
    const d = dirty();
    const n = Math.max(1, changes());
    p.className = app.saveError && d ? 'alert' : d ? '' : 'quiet';
    p.textContent = app.saving ? 'Saving…' : app.saveError && d ? app.saveError : d ? `${n} unsaved ${n === 1 ? 'change' : 'changes'}` : 'All changes saved';
    back.hidden = !d || app.saving;
    off(ok, !d || app.saving || app.signedOut);
  } };
}

function build() {
  const main = $('main');
  const rows = app.slots.map(slotRow);
  const reset = h('button', { type: 'button', class: 'plain', text: 'Reset to defaults', onclick: () => {
    const gone = app.draft.extra.length;
    app.draft = copy({ slots: app.defaults.slots, extra: [] });
    draw();
    say(`Back to the defaults${gone ? `, ${gone} extra removed` : ''}.${dirty() ? ' Not saved yet.' : ''}`);
  } });
  const parts = [modelsNotice(), extrasSection(), keySection(), endpointsSection()];
  const desk = desktopNotice();
  const bar = saveBar();
  const [notice, extras, key, endpoints] = parts;
  main.replaceChildren($('alerts'), notice.el,
    h('section', { class: 'slots', 'aria-labelledby': 'h-slots' },
      h('div', { class: 'head' }, h('h2', { id: 'h-slots', text: 'Claude models' }), reset),
      h('ul', { class: 'rows' }, ...rows.map((r) => r.el))),
    extras.el, key.el, endpoints.el);
  $('bar').replaceChildren(desk.el, bar.el);
  $('bar').hidden = false;
  redraw = [...rows, ...parts, desk, bar].map((x) => x.update);
  draw();
}

addEventListener('beforeunload', (e) => {
  if (app.started && (dirty() || app.formOpen())) {
    e.preventDefault();
  }
});
addEventListener('hashchange', async () => {
  await signIn();
  if (!app.started) {
    start();
  } else if (!app.signedOut) {
    draw();
    loadModels();
    loadKey();
    loadLists(() => true);
    loadEndpointKeys();
  }
});
// A new link opened in another tab of this browser signs this one in too.
addEventListener('focus', () => { if (app.signedOut) { ping(); } });
signIn().then(start);
