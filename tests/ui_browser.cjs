// Optional real Chromium check: BROWSER_PATH must name a Chromium/Chrome/Edge executable.
// Uses a fake local CPA API and a fresh profile, without accessing real accounts.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
const { spawn } = require('node:child_process');
const root = path.join(__dirname, '..');
const browser = process.env.BROWSER_PATH;
assert(browser && fs.existsSync(browser), 'Set BROWSER_PATH to a Chromium/Chrome/Edge executable');

const defaults = [
  { id: 'morning-0530', at: '05:30' }, { id: 'noon-1031', at: '10:31' },
  { id: 'afternoon-1532', at: '15:32' }, { id: 'evening-2033', at: '20:33' }
];
let config = { timezone: 'Asia/Shanghai', max_log_entries: 200, schedules: [], accounts: [] };
let latestResults = [];
const currentStatus = () => ({ running: false, account_count: config.accounts.length, timezone: config.timezone,
  schedules: config.accounts.map(account => ({ provider: account.provider, runs: [{ id: 'morning', at: '05:30', time: '2026-11-28T21:30:00Z' }] })),
  last_results: latestResults });

async function browserChecks() {
  const get = id => document.getElementById(id);
  const check = (condition, message) => { if (!condition) throw Error(message); };
  const visible = id => getComputedStyle(get(id)).display !== 'none';
  const waitFor = async predicate => {
    for (let attempt = 0; attempt < 200; attempt++) {
      if (predicate()) return;
      await new Promise(resolve => setTimeout(resolve, 20));
    }
    throw Error('Timed out waiting for UI');
  };
  try {
    check(/^v\d+\.\d+\.\d+$/.test(get('app-version').textContent), 'Version missing before authentication');
    check(document.querySelector('.github-link').href === 'https://github.com/Becase/ai-coding-plan-5h-quota-warmer', 'GitHub link points to wrong repository');
    check(!document.getElementById('management-key-title') && document.querySelectorAll('label[for="management-key"]').length === 1, 'Duplicate management key heading remains');
    get('management-key').value = 'browser-test-management-key';
    get('management-key').dispatchEvent(new Event('input'));
    get('reload').click();
    await waitFor(() => get('tabs').children.length > 0);
    [...get('tabs').children].find(node => node.textContent === '+ 添加 Provider').click();
    check(get('acct-source').value === 'cpa', 'New plugin account must default to CPA');
    check(get('management-key-label').textContent === 'CPA 管理密钥', 'CPA management key label is ambiguous');
    check(document.querySelector('label[for="acct-cpa-account"]').textContent === 'CPA 账号', 'CPA account label still restricts providers');
    check(document.querySelector('label[for="acct-model"]').textContent === '模型', 'Model label was not simplified');
    check(!document.getElementById('reload-cpa-models'), 'Refresh model button still exists');
    check(!get('acct-url').hasAttribute('placeholder'), 'API URL still has a placeholder');
    check(document.querySelector('.presets .hint').textContent.includes('端点快速选择'), 'Preset label was not updated');
    check(visible('cpa-account-field') && visible('toggle-cpa-models') && visible('model-field'), 'CPA fields are hidden');
    check(!get('acct-model').disabled && get('acct-model').required, 'Model text input is not editable and required');
    for (const id of ['url-field', 'format-field', 'key-field']) check(!visible(id), 'Manual field visible in CPA mode: ' + id);
    check(get('toggle-cpa-models').disabled, 'Picker must wait for account selection');
    get('acct-cpa-account').value = JSON.stringify(['browser-account', 'claude']);
    get('acct-cpa-account').dispatchEvent(new Event('change'));
    get('acct-model').value = 'browser-manual-alias';
    get('toggle-cpa-models').click();
    await waitFor(() => get('cpa-model-list').querySelector('button'));
    check(get('acct-model').value === 'browser-manual-alias', 'Opening picker overwrote input');
    get('toggle-cpa-models').dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
    check(document.activeElement === get('cpa-model-list').querySelector('button'), 'Arrow key did not focus a model');
    document.activeElement.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    check(!visible('cpa-model-list') && document.activeElement === get('toggle-cpa-models'), 'Escape did not close picker and restore focus');
    get('toggle-cpa-models').click();
    await waitFor(() => get('cpa-model-list').querySelector('button'));
    get('management-key-label').click();
    check(!visible('cpa-model-list'), 'Outside click did not close picker');
    get('toggle-cpa-models').click();
    await waitFor(() => get('cpa-model-list').querySelector('button'));
    get('cpa-model-list').querySelector('button').click();
    check(get('acct-model').value === 'browser-model' && !visible('cpa-model-list'), 'Selecting model did not fill input and close picker');
    get('save-account').click();
    await waitFor(() => get('notice').textContent.includes('设置已保存'));
    get('acct-model').value = 'browser-manual-alias';
    get('notice').textContent = '';
    get('save-account').click();
    await waitFor(() => get('notice').textContent.includes('设置已保存') && get('acct-model').value === 'browser-manual-alias');
    get('acct-source').value = 'api_key';
    get('acct-source').dispatchEvent(new Event('change'));
    check(visible('url-field') && visible('format-field') && visible('key-field') && !visible('cpa-account-field') && !visible('toggle-cpa-models'), 'Switching back to manual mode failed');
    check(get('acct-model').value === 'browser-manual-alias' && visible('model-field'), 'Manual mode lost model text input');
    [...get('tabs').children].find(node => node.textContent === '设置').click();
    get('restore-schedules').click();
    await waitFor(() => document.querySelectorAll('#schedules input[type="time"]').length === 4);
    const times = [...document.querySelectorAll('#schedules input[type="time"]')].map(node => node.value);
    check(times.join(',') === '05:30,10:31,15:32,20:33', 'Default schedule times differ from request');
    get('save-settings').click();
    await waitFor(() => get('notice').textContent.includes('设置已保存'));
    await fetch('/test/prepare-status', { method: 'POST', headers: { Authorization: 'Bearer browser-test-management-key' } });
    get('reload').click();
    await waitFor(() => document.querySelectorAll('.provider-run').length === 2);
    [...get('tabs').children].find(node => node.textContent === '状态').click();
    const buttons = () => [...document.querySelectorAll('.provider-run')];
    check(buttons().every(button => button.textContent === '预热' && button.title === '预热'), 'Provider button text differs from request');
    const titleSize = parseFloat(getComputedStyle(get('next-runs').previousElementSibling).fontSize);
    const providerSize = parseFloat(getComputedStyle(document.querySelector('.provider-heading')).fontSize);
    const scheduleSize = parseFloat(getComputedStyle(get('next-runs')).fontSize);
    check(titleSize > providerSize && providerSize > scheduleSize, 'Provider font hierarchy is incorrect');
    buttons()[0].click();
    check(get('run-now').disabled && buttons().every(button => button.disabled), 'Warmup controls remain clickable during a request');
    await waitFor(() => get('notice').textContent === '预热已完成。' && !get('run-now').disabled);
    const rows = [...document.querySelectorAll('#last-results tbody tr')];
    check(rows.length === 2 && rows[0].cells[0].textContent === 'CPA alias', 'Latest results lost provider or alias');
    check(rows[0].cells[1].textContent === '2026-11-28 10:31:15' && rows[1].cells[1].textContent === '2026-11-28 05:30:00', 'Completion dates or configured timezone are incorrect');
    get('run-now').click();
    await waitFor(() => get('notice').textContent === '预热已完成。' && !get('run-now').disabled);
    document.body.dataset.uiTestResult = 'passed';
  } catch (error) {
    document.body.dataset.uiTestResult = 'failed';
    const result = document.createElement('pre');
    result.id = 'browser-test-error'; result.textContent = error.stack;
    document.body.append(result);
  }
}

const html = fs.readFileSync(path.join(root, 'src/web/index.html'), 'utf8')
  .replaceAll('{{APP_VERSION}}', fs.readFileSync(path.join(root, 'src/warmer.go'), 'utf8').match(/serviceVersion\s*=\s*"([^"]+)"/)[1])
  .replace('</body>', '<script>(' + browserChecks.toString() + ')();</script></body>');
const server = http.createServer(async (request, response) => {
  try {
    const url = new URL(request.url, 'http://localhost');
    if (url.pathname === '/proxy/v0/resource/plugins/coding-plan-5h-quota-warmer/status') {
      response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' }); response.end(html); return;
    }
    assert.equal(request.headers.authorization, 'Bearer browser-test-management-key');
    let payload;
    if (url.pathname === '/test/prepare-status') {
      config.accounts = [{ provider: 'cpa-one', alias: 'CPA alias', source: 'cpa', auth_id: 'browser-account', cpa_provider: 'claude', model: 'browser-model' },
        { provider: 'manual-two', model: 'manual-model' }];
      latestResults = [{ provider: 'manual-two', model: 'manual-model', success: true, finished_at: '2026-11-27T21:30:00Z' }];
      payload = {};
    } else if (url.pathname.endsWith('/api/run')) {
      const chunks = []; for await (const chunk of request) chunks.push(chunk);
      const selection = JSON.parse(Buffer.concat(chunks));
      assert(selection.provider === 'cpa-one' || Object.keys(selection).length === 0, 'Wrong provider selector');
      latestResults = [{ provider: 'cpa-one', model: 'browser-model', success: true, finished_at: '2026-11-28T02:31:15Z' }, latestResults.at(-1)];
      payload = { results: latestResults.slice(0, selection.provider ? 1 : 2), status: currentStatus() };
    } else if (url.pathname.endsWith('/credentials/models')) {
      assert.equal(url.searchParams.get('name'), 'browser-account');
      payload = { models: [{ id: 'browser-model' }] };
    } else if (url.pathname.endsWith('/api/accounts')) payload = { accounts: [{ id: 'browser-account', provider: 'claude', name: 'Browser account' }] };
    else if (url.pathname.endsWith('/api/status')) payload = currentStatus();
    else if (url.pathname.endsWith('/api/logs')) payload = { logs: [] };
    else if (url.pathname.endsWith('/api/meta')) payload = { default_schedules: defaults };
    else if (url.pathname.endsWith('/api/config')) {
      if (request.method === 'PUT') {
        const chunks = []; for await (const chunk of request) chunks.push(chunk);
        config = JSON.parse(Buffer.concat(chunks));
        assert(['browser-model', 'browser-manual-alias'].includes(config.accounts[0].model));
        assert.equal(config.accounts[0].auth_id, 'browser-account');
        assert.equal(config.accounts[0].cpa_provider, 'claude');
        for (const key of ['api_key', 'api_url', 'api_format']) assert(!(key in config.accounts[0]), 'Manual field sent for CPA');
        payload = { config };
      } else payload = config;
    } else { response.writeHead(404); response.end('{}'); return; }
    response.writeHead(200, { 'Content-Type': 'application/json' }); response.end(JSON.stringify(payload));
  } catch (error) {
    response.writeHead(500, { 'Content-Type': 'application/json' }); response.end(JSON.stringify({ error: error.message }));
  }
});

server.listen(0, '127.0.0.1', () => {
  const profile = path.join(root, '.cache', 'browser-ui-' + process.pid);
  const url = 'http://127.0.0.1:' + server.address().port + '/proxy/v0/resource/plugins/coding-plan-5h-quota-warmer/status';
  const child = spawn(browser, [
    '--headless=new', '--disable-gpu', '--disable-extensions', '--disable-background-networking',
    '--no-first-run', '--no-default-browser-check', '--user-data-dir=' + profile,
    '--virtual-time-budget=10000', '--dump-dom', url
  ], { windowsHide: true });
  let output = '', errors = '';
  child.stdout.on('data', chunk => { output += chunk; });
  child.stderr.on('data', chunk => { errors += chunk; });
  const timeout = setTimeout(() => child.kill(), 30000);
  child.on('error', error => { errors += error.message; });
  child.on('close', code => {
    clearTimeout(timeout); server.close();
    fs.mkdirSync(path.join(root, '.cache'), { recursive: true });
    fs.writeFileSync(path.join(root, '.cache/ui-browser-result.html'), output);
    if (code === 0 && output.includes('data-ui-test-result="passed"')) console.log('Real browser CPA settings, header/version, Provider warmup controls and dated results passed');
    else {
      console.error(output.match(/<pre id="browser-test-error">([\s\S]*?)<\/pre>/)?.[1] || errors.slice(-2000) || 'Browser validation did not complete');
      process.exitCode = 1;
    }
  });
});
