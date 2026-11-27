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

async function scenario(pluginMode) {
  const elements = new Map([...html.matchAll(/id="([^"]+)"/g)].map(match => [match[1], new Element()]));
  const get = id => { assert(elements.has(id), 'Missing HTML element: ' + id); return elements.get(id); };
  const makeStorage = () => {
    const values = new Map();
    return { getItem: key => values.get(key) || null, setItem: (key, value) => values.set(key, value), removeItem: key => values.delete(key) };
  };
  const requests = [];
  let config = { timezone: 'Asia/Shanghai', max_log_entries: 200, schedules: [], accounts: [] };
  const base = pluginMode ? '/proxy/v0/management/plugins/coding-plan-5h-quota-warmer' : '';
  const fetch = async (url, options = {}) => {
    requests.push({ url, options });
    const endpoint = url.slice(base.length);
    let payload = {};
    if (endpoint === '/api/meta') payload = { management_enabled: true };
    else if (endpoint === '/api/config' && options.method === 'PUT') {
      config = JSON.parse(options.body); payload = { config };
    } else if (endpoint === '/api/config') payload = config;
    else if (endpoint === '/api/status') payload = { running: false, schedules: [], last_results: [] };
    else if (endpoint === '/api/logs') payload = { logs: [] };
    else if (endpoint === '/api/accounts') payload = { accounts: [{ id: 'selected-account', provider: 'codex', name: 'CPA account' }] };
    else throw new Error('Unexpected API route: ' + url);
    return { ok: true, json: async () => structuredClone(payload) };
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
  get('acct-model').value = 'test-model';
  if (pluginMode) {
    get('acct-source').value = 'cpa';
    await get('acct-source').trigger('change');
    assert.equal(get('cpa-account-field').hidden, false);
    assert.equal(get('key-field').hidden, true);
    get('acct-cpa-account').value = JSON.stringify(['selected-account', 'codex']);
  } else {
    get('acct-url').value = 'https://example.com/v1';
    get('acct-key').value = 'test-api-key';
  }
  await get('save-account').trigger('click');
  assert.equal(get('notice').className, 'notice success', get('notice').textContent);
  const saved = config.accounts[0];
  assert.equal(saved.model, 'test-model');
  if (pluginMode) {
    assert.equal(saved.source, 'cpa'); assert.equal(saved.auth_id, 'selected-account'); assert.equal(saved.cpa_provider, 'codex');
    assert(requests.every(request => request.url.startsWith(base + '/api/')), 'Proxy mount path was dropped');
  } else {
    assert.equal(saved.api_key, 'test-api-key'); assert.equal(saved.api_url, 'https://example.com/v1');
    assert(!requests.some(request => request.url === '/api/accounts'));
  }
  assert(requests.filter(request => request.url !== '/api/meta').every(request => request.options.headers.get('Authorization') === 'Bearer test-management-key'));
}

(async () => {
  await scenario(false);
  await scenario(true);
  console.log('Standalone and CPA frontend smoke passed');
})().catch(error => { console.error(error); process.exitCode = 1; });
