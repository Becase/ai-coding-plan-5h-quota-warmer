package main

const statusPageHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Codex 五小时额度预热</title>
  <style>
    :root { color-scheme: light dark; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    body { margin: 0; background: #f7f8fa; color: #1f2937; }
    main { max-width: 1100px; margin: 0 auto; padding: 28px 18px 48px; }
    h1 { margin: 0; font-size: 28px; } h2 { margin: 0 0 14px; font-size: 18px; }
    .subtitle { color: #5f6b7a; margin: 8px 0 24px; }
    .card { background: #fff; border: 1px solid #e3e7ed; border-radius: 12px; padding: 18px; margin: 16px 0; box-shadow: 0 1px 2px rgba(0,0,0,.03); }
    .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(190px, 1fr)); gap: 12px; }
    .field { display: grid; gap: 6px; } label { font-weight: 600; font-size: 14px; } input, textarea, button { font: inherit; }
    input, textarea { box-sizing: border-box; width: 100%; padding: 9px 10px; border: 1px solid #c9d1dc; border-radius: 7px; background: #fff; color: #1f2937; }
    textarea { min-height: 76px; resize: vertical; } button { padding: 8px 12px; border: 1px solid #2867c8; border-radius: 7px; background: #2867c8; color: #fff; cursor: pointer; }
    button.secondary { background: #fff; color: #24579f; } button.danger { border-color: #b42318; background: #fff; color: #b42318; } button:disabled { opacity: .55; cursor: wait; }
    .row { display: flex; gap: 10px; align-items: end; flex-wrap: wrap; } .grow { flex: 1 1 220px; }
    .hint, .muted { color: #657181; font-size: 13px; } .notice { margin-top: 12px; min-height: 20px; } .notice.error { color: #b42318; } .notice.success { color: #027a48; }
    table { width: 100%; border-collapse: collapse; font-size: 14px; } th, td { padding: 10px 8px; border-bottom: 1px solid #e7ebf0; text-align: left; vertical-align: top; } th { color: #5f6b7a; font-weight: 600; }
    code { padding: 2px 4px; background: #f2f4f7; border-radius: 4px; } pre { margin: 0; white-space: pre-wrap; word-break: break-word; }
    .status { font-weight: 600; } .ok { color: #027a48; } .failed { color: #b42318; } .tag { display: inline-block; border-radius: 100px; padding: 2px 8px; background: #eff4ff; color: #175cd3; font-size: 12px; }
    .schedule-row input[type="time"] { width: 130px; } .schedule-row input[type="text"] { width: 180px; }
    @media (prefers-color-scheme: dark) { body { background: #111827; color: #e5e7eb; } .card { background: #182230; border-color: #344054; } input, textarea, button.secondary { background: #101828; color: #e5e7eb; border-color: #475467; } .subtitle, .muted, .hint, th { color: #a6b1c2; } td, th { border-color: #344054; } code { background: #293548; } }
  </style>
</head>
<body>
<main>
  <h1>Codex 五小时额度预热</h1>
  <p class="subtitle">按每日固定时刻对当前启用的 Codex 凭证发送预热请求。</p>

  <section class="card">
    <h2>管理密钥</h2>
    <div class="row">
      <div class="field grow"><label for="management-key">管理密钥</label><input id="management-key" type="password" autocomplete="current-password" placeholder="用于读取状态、保存日程和手动执行"></div>
      <label class="muted"><input id="remember-key" type="checkbox"> 在此浏览器保存管理密钥</label>
      <button id="reload" type="button" class="secondary">读取数据</button>
    </div>
    <p class="hint">页面资源不携带凭证数据；所有状态和操作均通过受管理密钥保护的接口完成。</p>
    <div id="notice" class="notice" role="status"></div>
  </section>

  <section class="card">
    <div class="row"><h2 class="grow">当前状态</h2><button id="run-now" type="button">立即预热</button></div>
    <div id="summary" class="grid muted">请先输入管理密钥并读取数据。</div>
    <div style="margin-top:16px"><h3>下一次预热</h3><div id="next-runs" class="muted">暂无数据</div></div>
    <div style="margin-top:16px"><h3>最近一轮结果</h3><div id="last-results" class="muted">暂无数据</div></div>
  </section>

  <section class="card">
    <h2>预热设置</h2>
    <div class="grid">
      <div class="field"><label for="model">模型</label><input id="model" type="text" required></div>
      <div class="field"><label for="timezone">时区</label><input id="timezone" type="text" required placeholder="Asia/Singapore"></div>
      <div class="field"><label for="max-logs">日志条数上限</label><input id="max-logs" type="number" min="1" max="2000"></div>
    </div>
    <div class="field" style="margin-top:12px"><label for="prompt">预热消息</label><textarea id="prompt" required></textarea></div>
    <div style="margin-top:20px"><h3>每日固定时刻</h3><p class="hint">每一项每天执行一次。日程保存后会立即重新计算下一次预热时间。</p><div id="schedules"></div>
      <div class="row" style="margin-top:12px"><button id="add-schedule" type="button" class="secondary">新增日程</button><button id="save-settings" type="button">保存设置</button></div>
    </div>
  </section>

  <section class="card">
    <div class="row"><h2 class="grow">运行日志</h2><button id="load-logs" type="button" class="secondary">刷新日志</button></div>
    <div id="logs" class="muted">暂无数据</div>
  </section>
</main>
<script>
(() => {
  const pluginID = "codex-5h-quota-warmer";
  const base = "/v0/management/plugins/" + pluginID;
  const elements = {
    key: document.querySelector("#management-key"), remember: document.querySelector("#remember-key"), notice: document.querySelector("#notice"),
    summary: document.querySelector("#summary"), nextRuns: document.querySelector("#next-runs"), results: document.querySelector("#last-results"),
    model: document.querySelector("#model"), timezone: document.querySelector("#timezone"), prompt: document.querySelector("#prompt"), maxLogs: document.querySelector("#max-logs"),
    schedules: document.querySelector("#schedules"), logs: document.querySelector("#logs"), runNow: document.querySelector("#run-now")
  };
  let configuration = null;
  const storedKey = sessionStorage.getItem(pluginID + ".management-key") || localStorage.getItem(pluginID + ".management-key") || "";
  elements.key.value = storedKey;
  elements.remember.checked = Boolean(localStorage.getItem(pluginID + ".management-key"));

  function managementKey() { return elements.key.value.trim(); }
  function showNotice(text, type) { elements.notice.textContent = text || ""; elements.notice.className = "notice " + (type || ""); }
  function escapeText(value) { return String(value == null ? "" : value); }
  function formatTime(value) { if (!value || String(value).startsWith("0001-01-01")) return "—"; const date = new Date(value); return Number.isNaN(date.getTime()) ? escapeText(value) : date.toLocaleString(); }
  function saveKey() { const key = managementKey(); sessionStorage.removeItem(pluginID + ".management-key"); localStorage.removeItem(pluginID + ".management-key"); if (!key) return; if (elements.remember.checked) localStorage.setItem(pluginID + ".management-key", key); else sessionStorage.setItem(pluginID + ".management-key", key); }
  async function request(path, options = {}) {
    const key = managementKey();
    if (!key) throw new Error("请输入管理密钥");
    saveKey();
    const headers = new Headers(options.headers || {});
    headers.set("Authorization", "Bearer " + key);
    if (options.body) headers.set("Content-Type", "application/json");
    const response = await fetch(path, { ...options, headers, credentials: "same-origin" });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(payload.message || payload.error || ("请求失败，HTTP " + response.status));
    return payload;
  }
  function cell(text) { const node = document.createElement("td"); node.textContent = escapeText(text); return node; }
  function button(text, className, onClick) { const node = document.createElement("button"); node.type = "button"; node.textContent = text; node.className = className || "secondary"; node.addEventListener("click", onClick); return node; }
  function enabledSchedule(schedule) { return schedule.enabled !== false; }
  function renderSchedules() {
    const list = Array.isArray(configuration && configuration.schedules) ? configuration.schedules : [];
    elements.schedules.replaceChildren();
    if (!list.length) { const empty = document.createElement("p"); empty.className = "muted"; empty.textContent = "尚未设置自动预热时间。"; elements.schedules.append(empty); return; }
    list.forEach((schedule, index) => {
      const row = document.createElement("div"); row.className = "row schedule-row"; row.style.marginTop = "8px";
      const id = document.createElement("input"); id.type = "text"; id.value = schedule.id || ""; id.placeholder = "日程名称";
      const at = document.createElement("input"); at.type = "time"; at.value = schedule.at || "";
      const enabled = document.createElement("input"); enabled.type = "checkbox"; enabled.checked = enabledSchedule(schedule);
      id.addEventListener("input", () => { configuration.schedules[index].id = id.value; });
      at.addEventListener("input", () => { configuration.schedules[index].at = at.value; });
      enabled.addEventListener("change", () => { configuration.schedules[index].enabled = enabled.checked; });
      const enabledLabel = document.createElement("label"); enabledLabel.className = "muted"; enabledLabel.append(enabled, document.createTextNode(" 启用"));
      row.append(id, at, enabledLabel, button("删除", "danger", () => { configuration.schedules.splice(index, 1); renderSchedules(); }));
      elements.schedules.append(row);
    });
  }
  function fillConfiguration(config) {
    configuration = { ...config, schedules: Array.isArray(config.schedules) ? config.schedules.map(item => ({ ...item })) : [] };
    elements.model.value = configuration.model || "gpt-5.6-luna";
    elements.timezone.value = configuration.timezone || "Asia/Singapore";
    elements.prompt.value = configuration.prompt || "Reply with exactly: quota window activated";
    elements.maxLogs.value = configuration.max_log_entries || 200;
    renderSchedules();
  }
  function renderStatus(status) {
    elements.summary.replaceChildren();
    const fields = [["运行状态", status.running ? "正在预热" : "空闲"], ["模型", status.model], ["时区", status.timezone], ["最近执行", formatTime(status.last_run)], ["执行来源", status.last_source || "—"], ["日志数量", status.log_count], ["状态保存", status.state_error || "正常"]];
    fields.forEach(([label, value]) => { const item = document.createElement("div"); item.innerHTML = "<strong></strong><br>"; item.querySelector("strong").textContent = label; item.append(document.createTextNode(escapeText(value))); elements.summary.append(item); });
    const runs = Array.isArray(status.schedules) ? status.schedules : [];
    elements.nextRuns.replaceChildren();
    if (!runs.length) elements.nextRuns.textContent = "未设置启用的自动日程。";
    else runs.forEach(run => { const item = document.createElement("div"); item.textContent = run.id + "（" + run.at + "）：" + formatTime(run.time); elements.nextRuns.append(item); });
    renderResults(status.last_results || []);
  }
  function renderResults(results) {
    elements.results.replaceChildren();
    if (!results.length) { elements.results.textContent = "暂无完成的预热记录。"; return; }
    const table = document.createElement("table"), head = document.createElement("thead"), header = document.createElement("tr"); ["凭证", "模型", "结果", "HTTP 状态", "错误"].forEach(value => { const node = document.createElement("th"); node.textContent = value; header.append(node); }); head.append(header); table.append(head);
    const body = document.createElement("tbody"); results.forEach(result => { const row = document.createElement("tr"); row.append(cell(result.label || result.auth_id), cell(result.model), cell(result.success ? "成功" : "失败"), cell(result.status_code || "—"), cell(result.error || "—")); body.append(row); }); table.append(body); elements.results.append(table);
  }
  function renderLogs(entries) {
    elements.logs.replaceChildren();
    if (!entries.length) { elements.logs.textContent = "暂无日志。"; return; }
    const table = document.createElement("table"), head = document.createElement("thead"), header = document.createElement("tr"); ["时间", "级别", "事件", "信息", "字段"].forEach(value => { const node = document.createElement("th"); node.textContent = value; header.append(node); }); head.append(header); table.append(head);
    const body = document.createElement("tbody"); entries.slice().reverse().forEach(entry => { const row = document.createElement("tr"); row.append(cell(formatTime(entry.time)), cell(entry.level), cell(entry.event), cell(entry.message), cell(JSON.stringify(entry.fields || {}))); body.append(row); }); table.append(body); elements.logs.append(table);
  }
  async function loadAll() {
    try {
      showNotice("正在读取数据…");
      const [status, config, logs] = await Promise.all([request(base + "/status"), request(base.replace("/plugins/", "/plugins/") + "/config"), request(base + "/logs")]);
      renderStatus(status); fillConfiguration(config); renderLogs(logs.logs || []); showNotice("数据已更新。", "success");
    } catch (error) { showNotice(error.message, "error"); }
  }
  async function saveSettings() {
    try {
      if (!configuration) configuration = {};
      configuration.model = elements.model.value.trim(); configuration.timezone = elements.timezone.value.trim(); configuration.prompt = elements.prompt.value.trim(); configuration.max_log_entries = Number(elements.maxLogs.value || 200);
      configuration.schedules = configuration.schedules || [];
      await request(base + "/config", { method: "PUT", body: JSON.stringify(configuration) });
      showNotice("设置已保存，主程序将重新加载插件配置。", "success");
      window.setTimeout(loadAll, 800);
    } catch (error) { showNotice(error.message, "error"); }
  }
  async function runNow() {
    try { elements.runNow.disabled = true; showNotice("正在执行预热…"); const payload = await request(base + "/run", { method: "POST", body: "{}" }); renderResults(payload.results || []); renderStatus(payload.status || {}); showNotice("预热已完成。", "success"); await loadLogs(); } catch (error) { showNotice(error.message, "error"); } finally { elements.runNow.disabled = false; }
  }
  async function loadLogs() { try { const payload = await request(base + "/logs"); renderLogs(payload.logs || []); } catch (error) { showNotice(error.message, "error"); } }
  document.querySelector("#reload").addEventListener("click", loadAll); document.querySelector("#run-now").addEventListener("click", runNow); document.querySelector("#save-settings").addEventListener("click", saveSettings); document.querySelector("#load-logs").addEventListener("click", loadLogs);
  document.querySelector("#add-schedule").addEventListener("click", () => { if (!configuration) configuration = { schedules: [] }; configuration.schedules = configuration.schedules || []; const now = new Date(); const at = String(now.getHours()).padStart(2, "0") + ":" + String(now.getMinutes()).padStart(2, "0"); configuration.schedules.push({ id: "daily-" + at.replace(":", ""), at, enabled: true }); renderSchedules(); });
  if (managementKey()) loadAll();
})();
</script>
</body>
</html>`

