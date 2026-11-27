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
    get('management-key').value = 'browser-test-management-key';
    get('management-key').dispatchEvent(new Event('input'));
    get('reload').click();
    await waitFor(() => get('tabs').children.length > 0);
    [...get('tabs').children].find(node => node.textContent === '+ 添加 Provider').click();
    check(visible('url-field') && visible('format-field'), 'Manual request fields are hidden');
    check(!visible('cpa-account-field') && !visible('cpa-model-field'), 'Hidden CPA fields override display styles');
    check(!get('acct-url').hasAttribute('placeholder'), 'API URL still has a placeholder');
    check(document.querySelector('.presets .hint').textContent.includes('端点快速选择'), 'Preset label was not updated');
    get('acct-source').value = 'cpa';
    get('acct-source').dispatchEvent(new Event('change'));
    check(visible('cpa-account-field') && visible('cpa-model-field'), 'CPA selectors are hidden');
    for (const id of ['url-field', 'format-field', 'key-field', 'manual-model-field']) check(!visible(id), 'Manual field visible in CPA mode: ' + id);
    get('acct-cpa-account').value = JSON.stringify(['browser-account', 'codex']);
    get('acct-cpa-account').dispatchEvent(new Event('change'));
    await waitFor(() => [...get('acct-cpa-model').options].some(option => option.value === 'browser-model'));
    get('acct-cpa-model').value = 'browser-model';
    get('save-account').click();
    await waitFor(() => get('notice').textContent.includes('设置已保存'));
    get('acct-source').value = 'api_key';
    get('acct-source').dispatchEvent(new Event('change'));
    check(visible('url-field') && !visible('cpa-account-field'), 'Switching back to manual mode failed');
    [...get('tabs').children].find(node => node.textContent === '设置').click();
    get('restore-schedules').click();
    await waitFor(() => document.querySelectorAll('#schedules input[type="time"]').length === 4);
    const times = [...document.querySelectorAll('#schedules input[type="time"]')].map(node => node.value);
    check(times.join(',') === '05:30,10:31,15:32,20:33', 'Default schedule times differ from request');
    get('save-settings').click();
    await waitFor(() => get('notice').textContent.includes('设置已保存'));
    document.body.dataset.uiTestResult = 'passed';
  } catch (error) {
    document.body.dataset.uiTestResult = 'failed';
    const result = document.createElement('pre');
    result.id = 'browser-test-error'; result.textContent = error.stack;
    document.body.append(result);
  }
}

const html = fs.readFileSync(path.join(root, 'src/web/index.html'), 'utf8')
  .replace('</body>', '<script>(' + browserChecks.toString() + ')();</script></body>');
const server = http.createServer(async (request, response) => {
  try {
    const url = new URL(request.url, 'http://localhost');
    if (url.pathname === '/proxy/v0/resource/plugins/coding-plan-5h-quota-warmer/status') {
      response.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' }); response.end(html); return;
    }
    assert.equal(request.headers.authorization, 'Bearer browser-test-management-key');
    let payload;
    if (url.pathname.endsWith('/credentials/models')) {
      assert.equal(url.searchParams.get('name'), 'browser-account');
      payload = { models: [{ id: 'browser-model' }] };
    } else if (url.pathname.endsWith('/api/accounts')) payload = { accounts: [{ id: 'browser-account', provider: 'codex', name: 'Browser account' }] };
    else if (url.pathname.endsWith('/api/status')) payload = { running: false, schedules: [], last_results: [] };
    else if (url.pathname.endsWith('/api/logs')) payload = { logs: [] };
    else if (url.pathname.endsWith('/api/meta')) payload = { default_schedules: defaults };
    else if (url.pathname.endsWith('/api/config')) {
      if (request.method === 'PUT') {
        const chunks = []; for await (const chunk of request) chunks.push(chunk);
        config = JSON.parse(Buffer.concat(chunks));
        assert.equal(config.accounts[0].model, 'browser-model');
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
    if (code === 0 && output.includes('data-ui-test-result="passed"')) console.log('Real browser field visibility, CPA models and default schedule checks passed');
    else {
      console.error(output.match(/<pre id="browser-test-error">([\s\S]*?)<\/pre>/)?.[1] || errors.slice(-2000) || 'Browser validation did not complete');
      process.exitCode = 1;
    }
  });
});
