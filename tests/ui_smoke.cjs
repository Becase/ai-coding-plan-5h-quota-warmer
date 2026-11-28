// Run the real embedded script against a small DOM adapter and fake HTTP API.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const html = fs.readFileSync(path.join(__dirname, '../src/web/index.html'), 'utf8');
const script = html.match(/<script>([\s\S]*?)<\/script>/)[1];

class Element {
  constructor(tag = 'div') {
    this.tag = tag; this.children = []; this.listeners = {}; this.style = {}; this.attributes = {};
    this.value = ''; this.hidden = false;
  }
  append(...nodes) { this.children.push(...nodes); }
  replaceChildren(...nodes) { this.children = nodes; if (this.tag === 'select') this.value = nodes[0]?.value || ''; }
  addEventListener(name, callback) { this.listeners[name] = callback; }
  querySelector() { return new Element(); }
  querySelectorAll(tag) { return this.children.filter(node => node.tag === tag); }
  setAttribute(name, value) { this.attributes[name] = value; }
  contains(node) { return node === this || this.children.some(child => child.contains?.(node)); }
  focus() { this.focused = true; }
  get options() { return this.children; }
  async trigger(name, event = {}) { if (this.listeners[name]) await this.listeners[name]({ target: this, preventDefault() {}, ...event }); }
}

const defaults = [
  { id: 'morning-0530', at: '05:30' }, { id: 'noon-1031', at: '10:31' },
  { id: 'afternoon-1532', at: '15:32' }, { id: 'evening-2033', at: '20:33' }
];

async function createPage(pluginMode, options = {}) {
  const elements = new Map([...html.matchAll(/<([a-z0-9]+)\b([^>]*\bid="([^"]+)"[^>]*)>/g)].map(match => {
    const element = new Element(match[1]);
    element.hidden = /\bhidden\b/.test(match[2]); element.required = /\brequired\b/.test(match[2]);
    return [match[3], element];
  }));
  const documentEvents = new Element('document');
  const get = id => { assert(elements.has(id), 'Missing HTML element: ' + id); return elements.get(id); };
  const makeStorage = () => {
    const values = new Map();
    return { getItem: key => values.get(key) || null, setItem: (key, value) => values.set(key, value), removeItem: key => values.delete(key) };
  };
  const requests = [];
  let config = { timezone: 'Asia/Shanghai', max_log_entries: 200, schedules: [{ id: 'custom', at: '09:17' }], accounts: structuredClone(options.initialAccounts || []) };
  let status = options.status || { running: false, schedules: [], last_results: [] };
  const models = {
    'selected/account+one@example.com': [{ id: 'codex-model', display_name: 'Codex model' }],
    'other-account': [{ id: 'antigravity-model' }],
    'claude-account': [{ id: 'claude-model' }]
  };
  const mount = pluginMode ? '/proxy' : '';
  const base = pluginMode ? '/proxy/v0/management/plugins/coding-plan-5h-quota-warmer' : '';
  const reply = (payload, status = 200) => ({ ok: status >= 200 && status < 300, status, json: async () => structuredClone(payload) });
  const fetch = async (url, options = {}) => {
    requests.push({ url, options });
    return route(url, options);
  };
  const route = async (url, requestOptions) => {
    if (url.startsWith(mount + '/v8/management/credentials/models?') || url.startsWith(mount + '/v0/management/auth-files/models?')) {
      if (options.legacyModels && url.includes('/v8/')) return reply({ error: 'not found' }, 404);
      if (options.modelError) return reply({ error: 'model request failed' }, options.modelError);
      const id = new URL(url, 'http://localhost').searchParams.get('name');
      const payload = { models: options.emptyModels ? [] : models[id] || [] };
      if (options.delayedAccount === id) return new Promise(resolve => { options.releaseDelayed = () => resolve(reply(payload)); });
      return reply(payload);
    }
    const endpoint = url.slice(base.length);
    let payload = {};
    if (endpoint === '/api/meta') payload = { management_enabled: true, default_schedules: defaults };
    else if (endpoint === '/api/config' && requestOptions.method === 'PUT') {
      config = JSON.parse(requestOptions.body); payload = { config };
    } else if (endpoint === '/api/config') payload = config;
    else if (endpoint === '/api/status') payload = status;
    else if (endpoint === '/api/run') {
      if (options.delayedRun) await new Promise(resolve => { options.releaseRun = resolve; });
      if (options.runError) return reply({ message: 'test warmup failed' }, 409);
      status = options.runStatus || status;
      payload = { results: status.last_results?.slice(0, 1), status };
    }
    else if (endpoint === '/api/logs') payload = { logs: [] };
    else if (endpoint === '/api/accounts') payload = { accounts: [
      { id: 'selected/account+one@example.com', provider: 'codex', name: 'CPA account' },
      { id: 'other-account', provider: 'antigravity', name: 'Other account' },
      { id: 'claude-account', provider: 'claude', name: 'Claude account' }
    ] };
    else throw new Error('Unexpected API route: ' + url);
    return reply(payload);
  };
  vm.runInNewContext(script, {
    document: {
      querySelector: selector => get(selector.slice(1)), querySelectorAll: () => [],
      createElement: tag => new Element(tag), createTextNode: text => ({ textContent: text }),
      addEventListener: (name, callback) => documentEvents.addEventListener(name, callback)
    },
    location: { pathname: pluginMode ? '/proxy/v0/resource/plugins/coding-plan-5h-quota-warmer/status' : '/' },
    sessionStorage: makeStorage(), localStorage: makeStorage(), fetch, Headers,
    window: { setTimeout() {}, setInterval() {}, confirm: () => true },
    console, Date, JSON
  });
  await new Promise(resolve => setImmediate(resolve));
  get('management-key').value = 'test-management-key';
  await get('reload').trigger('click');
  assert.equal(get('notice').className, 'notice success', get('notice').textContent);
  if (options.addAccount !== false) {
    const addButton = get('tabs').children.find(element => element.textContent === '+ 添加 Provider');
    await addButton.trigger('click');
    assert.equal(get('source-field').hidden, !pluginMode);
    assert.equal(get('acct-source').value, pluginMode ? 'cpa' : 'api_key');
  }
  return { get, requests, options, documentEvents, get config() { return config; } };
}

async function selectCPA(page, id = 'selected/account+one@example.com', provider = 'codex') {
  page.get('acct-source').value = 'cpa';
  await page.get('acct-source').trigger('change');
  page.get('acct-cpa-account').value = JSON.stringify([id, provider]);
  await page.get('acct-cpa-account').trigger('change');
}

async function scenario(pluginMode) {
  const page = await createPage(pluginMode), { get, requests } = page;
  get('acct-model').value = 'test-model';
  assert.equal(get('acct-model').required, true);
  assert(!get('acct-model').disabled);
  assert.equal(get('model-field').hidden, false);
  if (pluginMode) {
    assert.equal(get('management-key-label').textContent, 'CPA 管理密钥');
    assert.equal(get('toggle-cpa-models').disabled, true);
    await get('save-account').trigger('click');
    assert(!requests.some(request => request.options.method === 'PUT'), 'Missing CPA account was saved');
    await selectCPA(page);
    assert.equal(get('cpa-account-field').hidden, false);
    assert.equal(get('toggle-cpa-models').hidden, false);
    for (const id of ['key-field', 'url-field', 'format-field']) assert.equal(get(id).hidden, true);
    assert.equal(get('acct-url').required, false);
    assert.equal(get('acct-format').disabled, true);
    assert(!requests.some(request => request.url.includes('/models?')), 'Model list loaded before opening picker');
    await get('toggle-cpa-models').trigger('click');
    assert.equal(get('acct-model').value, 'test-model', 'Opening list overwrote input');
    assert.equal(get('cpa-model-list').children[0].value, 'codex-model');
    await get('cpa-model-list').children[0].trigger('click');
    assert.equal(get('acct-model').value, 'codex-model');
    assert.equal(get('cpa-model-list').hidden, true);
    get('acct-model').value = ' manual-alias-not-in-catalog ';
  } else {
    assert.equal(get('cpa-account-field').hidden, true);
    assert.equal(get('toggle-cpa-models').hidden, true);
    assert.equal(get('url-field').hidden, false);
    get('acct-url').value = 'https://example.com/v1';
    get('acct-key').value = 'test-api-key';
    get('acct-provider').value = 'my-provider';
    await get('acct-provider').trigger('input');
    assert(get('api-key-env-hint').textContent.includes('QUOTA_WARMER_API_KEY_MY_PROVIDER'));
  }
  await get('save-account').trigger('click');
  assert.equal(get('notice').className, 'notice success', get('notice').textContent);
  const saved = page.config.accounts[0];
  assert.equal(saved.model, pluginMode ? 'manual-alias-not-in-catalog' : 'test-model');
  if (pluginMode) {
    assert.equal(saved.source, 'cpa'); assert.equal(saved.auth_id, 'selected/account+one@example.com'); assert.equal(saved.cpa_provider, 'codex');
    for (const key of ['api_url', 'api_format', 'api_key']) assert(!(key in saved), 'Manual field sent for CPA: ' + key);
    assert(requests.every(request => request.url.startsWith('/proxy/')), 'Proxy mount path was dropped');
    assert(requests.some(request => request.url.endsWith('?name=selected%2Faccount%2Bone%40example.com')));
    get('acct-source').value = 'api_key';
    await get('acct-source').trigger('change');
    assert.equal(get('cpa-account-field').hidden, true);
    assert.equal(get('toggle-cpa-models').hidden, true);
    assert.equal(get('url-field').hidden, false);
    assert.equal(get('acct-model').value, 'manual-alias-not-in-catalog', 'Switching source lost model');
    get('acct-model').value = 'manual-model'; get('acct-url').value = 'https://manual.example.com/v1'; get('acct-key').value = 'manual-key';
    await get('save-account').trigger('click');
    assert.equal(page.config.accounts[0].source, 'api_key');
    assert(!('auth_id' in page.config.accounts[0]) && !('cpa_provider' in page.config.accounts[0]));
  } else {
    assert.equal(saved.api_key, 'test-api-key'); assert.equal(saved.api_url, 'https://example.com/v1');
    assert(!requests.some(request => request.url === '/api/accounts'));
  }
  assert(requests.filter(request => request.url !== '/api/meta').every(request => request.options.headers.get('Authorization') === 'Bearer test-management-key'));
  assert.deepEqual(page.config.schedules, [{ id: 'custom', at: '09:17' }], 'Loading overwrote existing schedules');
  await get('restore-schedules').trigger('click');
  await get('save-settings').trigger('click');
  assert.deepEqual(page.config.schedules, defaults);
}

async function modelFailuresAndCompatibility() {
  const legacy = await createPage(true, { legacyModels: true });
  await selectCPA(legacy);
  await legacy.get('toggle-cpa-models').trigger('click');
  assert.equal(legacy.get('cpa-model-list').children[0].value, 'codex-model');
  assert(legacy.requests.some(request => request.url.startsWith('/proxy/v0/management/auth-files/models?')));

  const failed = await createPage(true, { modelError: 403 });
  await selectCPA(failed);
  await failed.get('toggle-cpa-models').trigger('click');
  assert(!failed.get('acct-model').disabled);
  assert(failed.get('cpa-model-list').children[0].textContent.includes('无法读取'));
  assert(!failed.requests.some(request => request.url.startsWith('/proxy/v0/management/auth-files/models?')), 'Auth failure must not retry another API');
  failed.options.modelError = 0;
  await failed.get('toggle-cpa-models').trigger('click');
  await failed.get('toggle-cpa-models').trigger('click');
  assert.equal(failed.get('cpa-model-list').children[0].value, 'codex-model');

  const empty = await createPage(true, { emptyModels: true });
  await selectCPA(empty);
  await empty.get('toggle-cpa-models').trigger('click');
  assert(empty.get('cpa-model-list').children[0].textContent.includes('未返回'));
  for (const page of [failed, empty]) {
    page.get('acct-model').value = '';
    await page.get('save-account').trigger('click');
    assert(!page.requests.some(request => request.options.method === 'PUT'), 'Empty model was saved');
    page.get('acct-model').value = 'custom-model-alias';
    await page.get('save-account').trigger('click');
    assert.equal(page.config.accounts[0].model, 'custom-model-alias');
  }
  const unavailable = await createPage(true, { modelError: 503 });
  await selectCPA(unavailable, 'claude-account', 'claude');
  await unavailable.get('toggle-cpa-models').trigger('click');
  unavailable.get('acct-model').value = 'claude-custom';
  await unavailable.get('save-account').trigger('click');
  assert.equal(unavailable.config.accounts[0].cpa_provider, 'claude');
  assert.equal(unavailable.config.accounts[0].model, 'claude-custom');
}

async function pickerLifecycle() {
  const stale = await createPage(true, { delayedAccount: 'selected/account+one@example.com' });
  await selectCPA(stale);
  const pending = stale.get('toggle-cpa-models').trigger('click');
  await new Promise(resolve => setImmediate(resolve));
  await selectCPA(stale, 'other-account', 'antigravity');
  await stale.get('toggle-cpa-models').trigger('click');
  stale.get('acct-model').value = 'typed-during-load';
  stale.options.releaseDelayed();
  await pending;
  assert.equal(stale.get('acct-model').value, 'typed-during-load');
  assert.equal(stale.get('cpa-model-list').children[0].value, 'antigravity-model', 'Previous account response replaced current models');
  await stale.documentEvents.trigger('keydown', { key: 'Escape' });
  assert.equal(stale.get('cpa-model-list').hidden, true);
  assert.equal(stale.get('toggle-cpa-models').focused, true);
  await stale.get('toggle-cpa-models').trigger('click');
  await stale.documentEvents.trigger('click', { target: stale.get('notice') });
  assert.equal(stale.get('cpa-model-list').hidden, true);

  for (const action of ['close', 'source', 'tab']) {
    const page = await createPage(true, { delayedAccount: 'selected/account+one@example.com' });
    await selectCPA(page);
    const pending = page.get('toggle-cpa-models').trigger('click');
    await new Promise(resolve => setImmediate(resolve));
    if (action === 'close') await page.get('toggle-cpa-models').trigger('click');
    else if (action === 'source') {
      page.get('acct-source').value = 'api_key';
      await page.get('acct-source').trigger('change');
    } else await page.get('tabs').children.find(button => button.textContent === '设置').trigger('click');
    page.options.releaseDelayed(); await pending;
    assert.equal(page.get('cpa-model-list').hidden, true, action + ' allowed stale list to reopen');
    assert.equal(page.get('cpa-model-list').children.length, 0);
  }
}

async function existingSourcesRemainUnchanged() {
  for (const source of [undefined, 'api_key', 'cpa']) {
    const account = { provider: 'existing', source, model: 'existing-alias', api_url: 'https://example.com/v1', api_format: 'openai' };
    if (source === 'cpa') Object.assign(account, { auth_id: 'claude-account', cpa_provider: 'claude' });
    const page = await createPage(true, { initialAccounts: [account], addAccount: false });
    await page.get('tabs').children.find(button => button.textContent === 'existing').trigger('click');
    assert.equal(page.get('acct-source').value, source || 'api_key');
    assert.equal(page.get('acct-model').value, 'existing-alias');
    assert.equal(page.get('toggle-cpa-models').hidden, source !== 'cpa');
    assert(!page.requests.some(request => request.url.includes('/models?')));
    await page.get('save-account').trigger('click');
    assert.equal(page.config.accounts[0].source, source || 'api_key');
  }
}

async function providerWarmupControls() {
  const first = { provider: 'first', alias: 'First alias', model: 'model-one' };
  const second = { provider: 'second', model: 'model-two' };
  const oldResult = { provider: 'second', model: 'model-two', success: true, finished_at: '2026-11-27T21:30:00Z' };
  const status = { account_count: 2, timezone: 'Asia/Shanghai', running: false,
    schedules: [first, second].map(account => ({ provider: account.provider, runs: [] })), last_results: [oldResult] };
  const runStatus = { ...status, last_results: [
    { provider: 'first', model: 'model-one', success: true, finished_at: '2026-11-28T02:31:15Z' }, oldResult
  ] };
  for (const pluginMode of [false, true]) {
    const page = await createPage(pluginMode, { initialAccounts: [first, second], addAccount: false, status, runStatus, delayedRun: true });
    const buttons = () => page.get('next-runs').children.filter(node => node.className === 'provider-heading').map(node => node.children[1]);
    assert.equal(buttons().length, 2);
    assert.equal(buttons()[0].textContent, '预热');
    assert.equal(buttons()[0].title, '预热');
    const pending = buttons()[0].trigger('click');
    await new Promise(resolve => setImmediate(resolve));
    assert(buttons().every(button => button.disabled));
    assert(page.get('run-now').disabled);
    await buttons()[1].trigger('click');
    await page.get('run-now').trigger('click');
    const runs = () => page.requests.filter(request => request.url.endsWith('/api/run'));
    assert.equal(runs().length, 1, 'Concurrent click sent a second warmup');
    assert.deepEqual(JSON.parse(runs()[0].options.body), { provider: 'first' }, 'Alias used as provider ID');
    page.options.releaseRun(); await pending;
    assert(buttons().every(button => !button.disabled));
    assert(!page.get('run-now').disabled);
    const rows = page.get('last-results').children[0].children[1].children;
    assert.equal(rows.length, 2, 'Single warmup dropped another provider result');
    assert.equal(rows[0].children[0].textContent, 'First alias');
    assert.equal(rows[0].children[1].textContent, '2026-11-28 10:31:15');
    assert.equal(rows[1].children[1].textContent, '2026-11-28 05:30:00');
    assert(page.requests.at(-1).url.endsWith('/api/logs'), 'Completion did not refresh logs');
    page.options.delayedRun = false;
    await page.get('run-now').trigger('click');
    assert.deepEqual(JSON.parse(runs().at(-1).options.body), {}, 'Global warmup no longer targets all providers');
    page.options.runError = true;
    await buttons()[0].trigger('click');
    assert.equal(page.get('notice').textContent, 'test warmup failed');
    assert(!page.get('run-now').disabled, 'Failed request left controls disabled');
  }
  const single = await createPage(false, { initialAccounts: [first], addAccount: false, status: { ...status, account_count: 1, schedules: status.schedules.slice(0, 1) } });
  assert.equal(single.get('next-runs').children[0].children.length, 1, 'Single-provider page shows a redundant button');
  const busy = await createPage(true, { initialAccounts: [first, second], addAccount: false, status: { ...status, running: true } });
  assert(busy.get('run-now').disabled, 'Server-running state did not disable global warmup');
  assert(busy.get('next-runs').children.filter(node => node.className === 'provider-heading').every(node => node.children[1].disabled));
}

(async () => {
  await scenario(false);
  await scenario(true);
  await modelFailuresAndCompatibility();
  await pickerLifecycle();
  await existingSourcesRemainUnchanged();
  await providerWarmupControls();
  console.log('Standalone and CPA frontend smoke passed');
})().catch(error => { console.error(error); process.exitCode = 1; });
