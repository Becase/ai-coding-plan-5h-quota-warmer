// Run the real embedded script against a small DOM adapter and fake HTTP API.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const html = fs.readFileSync(path.join(__dirname, '../src/web/index.html'), 'utf8');
const script = html.match(/<script>([\s\S]*?)<\/script>/)[1];

class Element {
  constructor(tag = 'div') {
    this.tag = tag; this.children = []; this.listeners = {}; this.style = {};
    this.value = ''; this.hidden = false;
  }
  append(...nodes) { this.children.push(...nodes); }
  replaceChildren(...nodes) { this.children = nodes; }
  addEventListener(name, callback) { this.listeners[name] = callback; }
  querySelector() { return new Element(); }
  get options() { return this.children; }
  async trigger(name) { if (this.listeners[name]) await this.listeners[name]({}); }
}

const defaults = [
  { id: 'morning-0530', at: '05:30' }, { id: 'noon-1031', at: '10:31' },
  { id: 'afternoon-1532', at: '15:32' }, { id: 'evening-2033', at: '20:33' }
];

async function createPage(pluginMode, options = {}) {
  const elements = new Map([...html.matchAll(/id="([^"]+)"/g)].map(match => [match[1], new Element()]));
  const get = id => { assert(elements.has(id), 'Missing HTML element: ' + id); return elements.get(id); };
  const makeStorage = () => {
    const values = new Map();
    return { getItem: key => values.get(key) || null, setItem: (key, value) => values.set(key, value), removeItem: key => values.delete(key) };
  };
  const requests = [];
  let config = { timezone: 'Asia/Shanghai', max_log_entries: 200, schedules: [{ id: 'custom', at: '09:17' }], accounts: [] };
  const models = {
    'selected/account+one@example.com': [{ id: 'codex-model', display_name: 'Codex model' }],
    'other-account': [{ id: 'antigravity-model' }]
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
    else if (endpoint === '/api/status') payload = { running: false, schedules: [], last_results: [] };
    else if (endpoint === '/api/logs') payload = { logs: [] };
    else if (endpoint === '/api/accounts') payload = { accounts: [
      { id: 'selected/account+one@example.com', provider: 'codex', name: 'CPA account' },
      { id: 'other-account', provider: 'antigravity', name: 'Other account' }
    ] };
    else throw new Error('Unexpected API route: ' + url);
    return reply(payload);
  };
  vm.runInNewContext(script, {
    document: {
      querySelector: selector => get(selector.slice(1)), querySelectorAll: () => [],
      createElement: tag => new Element(tag), createTextNode: text => ({ textContent: text })
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
  const addButton = get('tabs').children.find(element => element.textContent === '+ 添加 Provider');
  await addButton.trigger('click');
  assert.equal(get('source-field').hidden, !pluginMode);
  return { get, requests, options, get config() { return config; } };
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
  if (pluginMode) {
    await selectCPA(page);
    assert.equal(get('cpa-account-field').hidden, false);
    assert.equal(get('cpa-model-field').hidden, false);
    for (const id of ['key-field', 'url-field', 'format-field', 'manual-model-field']) assert.equal(get(id).hidden, true);
    assert.equal(get('acct-url').required, false);
    assert.equal(get('acct-format').disabled, true);
    assert.equal(get('acct-cpa-model').options[1].value, 'codex-model');
    get('acct-cpa-model').value = 'not-this-accounts-model';
    await get('save-account').trigger('click');
    assert(!requests.some(request => request.options.method === 'PUT'), 'An unlisted model was saved');
    get('acct-cpa-model').value = 'codex-model';
  } else {
    assert.equal(get('cpa-account-field').hidden, true);
    assert.equal(get('cpa-model-field').hidden, true);
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
  assert.equal(saved.model, pluginMode ? 'codex-model' : 'test-model');
  if (pluginMode) {
    assert.equal(saved.source, 'cpa'); assert.equal(saved.auth_id, 'selected/account+one@example.com'); assert.equal(saved.cpa_provider, 'codex');
    for (const key of ['api_url', 'api_format', 'api_key']) assert(!(key in saved), 'Manual field sent for CPA: ' + key);
    assert(requests.every(request => request.url.startsWith('/proxy/')), 'Proxy mount path was dropped');
    assert(requests.some(request => request.url.endsWith('?name=selected%2Faccount%2Bone%40example.com')));
    get('acct-source').value = 'api_key';
    await get('acct-source').trigger('change');
    assert.equal(get('cpa-account-field').hidden, true);
    assert.equal(get('cpa-model-field').hidden, true);
    assert.equal(get('url-field').hidden, false);
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
  assert.equal(legacy.get('acct-cpa-model').options[1].value, 'codex-model');
  assert(legacy.requests.some(request => request.url.startsWith('/proxy/v0/management/auth-files/models?')));

  const failed = await createPage(true, { modelError: 403 });
  await selectCPA(failed);
  assert.equal(failed.get('acct-cpa-model').disabled, true);
  assert(failed.get('cpa-model-hint').textContent.includes('无法读取'));
  assert(!failed.requests.some(request => request.url.startsWith('/proxy/v0/management/auth-files/models?')), 'Auth failure must not retry another API');
  failed.options.modelError = 0;
  await failed.get('reload-cpa-models').trigger('click');
  assert.equal(failed.get('acct-cpa-model').options[1].value, 'codex-model');

  const empty = await createPage(true, { emptyModels: true });
  await selectCPA(empty);
  assert.equal(empty.get('acct-cpa-model').disabled, true);
  assert(empty.get('cpa-model-hint').textContent.includes('未返回'));

  const stale = await createPage(true, { delayedAccount: 'selected/account+one@example.com' });
  stale.get('acct-source').value = 'cpa';
  await stale.get('acct-source').trigger('change');
  stale.get('acct-cpa-account').value = JSON.stringify(['selected/account+one@example.com', 'codex']);
  const pending = stale.get('acct-cpa-account').trigger('change');
  await new Promise(resolve => setImmediate(resolve));
  stale.get('acct-cpa-account').value = JSON.stringify(['other-account', 'antigravity']);
  await stale.get('acct-cpa-account').trigger('change');
  stale.get('acct-cpa-model').value = 'antigravity-model';
  stale.options.releaseDelayed();
  await pending;
  assert.equal(stale.get('acct-cpa-model').value, 'antigravity-model');
  assert(!stale.get('acct-cpa-model').options.some(option => option.value === 'codex-model'), 'Previous account response replaced current models');
}

(async () => {
  await scenario(false);
  await scenario(true);
  await modelFailuresAndCompatibility();
  console.log('Standalone and CPA frontend smoke passed');
})().catch(error => { console.error(error); process.exitCode = 1; });
