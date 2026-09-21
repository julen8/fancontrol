const state = { config: null, disks: [], status: null, lastHardwareError: "" };
const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];
const noticeQueue = [];
let noticeActive = false;

async function api(path, options = {}) {
  const response = await fetch(path, {
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
    ...options,
  });
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(body.error || `请求失败 (${response.status})`);
  return body;
}

function showLogin(show) {
  $("#login-view").classList.toggle("hidden", !show);
  $("#app-view").classList.toggle("hidden", show);
}

async function boot() {
  const session = await api("/api/auth/session");
  if (!session.authenticated) return showLogin(true);
  showLogin(false);
  await Promise.all([loadConfig(), loadDisks(), refresh()]);
  setInterval(refresh, 3000);
}

$("#login-form").addEventListener("submit", async event => {
  event.preventDefault();
  try {
    await api("/api/auth/login", { method: "POST", body: JSON.stringify({ username: $("#login-user").value, password: $("#login-password").value }) });
    await boot();
  } catch (error) { notify(error.message, true); }
});

$("#logout").addEventListener("click", async () => { await api("/api/auth/logout", { method: "POST", body: "{}" }); location.reload(); });

async function loadConfig() {
  state.config = await api("/api/config");
  renderController("cpu", state.config.cpu);
  renderController("hdd", state.config.hdd);
  $("#listen").value = state.config.server.listen;
  $("#session-timeout").value = state.config.server.session_timeout;
  $("#ipmi-command").value = state.config.ipmi.command;
  $("#ipmi-timeout").value = state.config.ipmi.timeout;
  $("#exit-speed").value = state.config.ipmi.exit_speed;
  $("#cpu-poll").value = state.config.cpu.poll_interval;
  $("#hdd-poll").value = state.config.hdd.poll_interval;
  $("#cpu-safe").value = state.config.cpu.safe_speed;
  $("#hdd-safe").value = state.config.hdd.safe_speed;
  $("#cpu-critical").value = state.config.cpu.critical_temperature;
  $("#hdd-critical").value = state.config.hdd.critical_temperature;
  $("#avoid-wakeup").checked = state.config.hdd.avoid_wakeup;
}

function renderController(name, config) {
  const root = $(`[data-controller="${name}"]`);
  $(".enabled", root).checked = config.enabled;
  $(".fixed-speed", root).min = config.minimum_speed;
  $(".fixed-speed", root).value = config.fixed_speed;
  $(".fixed-output", root).textContent = `${config.fixed_speed}%`;
  setMode(root, config.mode);
  renderCurve(root, config.curve);
}

function setMode(root, mode) {
  root.dataset.mode = mode;
  $$(".segmented button", root).forEach(button => button.classList.toggle("active", button.dataset.mode === mode));
  $(".fixed-editor", root).classList.toggle("hidden", mode !== "fixed");
  $(".curve-editor", root).classList.toggle("hidden", mode !== "curve");
}

$$('.controller').forEach(root => {
  $$(".segmented button", root).forEach(button => button.addEventListener("click", () => setMode(root, button.dataset.mode)));
  $(".fixed-speed", root).addEventListener("input", event => $(".fixed-output", root).textContent = `${event.target.value}%`);
  $(".add-point", root).addEventListener("click", () => {
    const points = readCurve(root);
    const last = points.at(-1) || { temperature: 30, speed: 30 };
    points.push({ temperature: Math.min(120, last.temperature + 5), speed: Math.min(100, last.speed + 5) });
    renderCurve(root, points);
  });
  $(".curve-table", root).addEventListener("input", () => drawCurve(root));
  $(".curve-table", root).addEventListener("click", event => {
    if (!event.target.matches("button")) return;
    const rows = $$(".curve-row", root);
    if (rows.length <= 2) return notify("曲线至少需要两个控制点", true);
    event.target.closest(".curve-row").remove();
    drawCurve(root);
  });
  $(".test-fan", root).addEventListener("click", async () => {
    const speed = Number($(".fixed-speed", root).value);
    const zone = root.dataset.controller === "cpu" ? state.config.cpu.zone : state.config.hdd.zone;
    try { await api("/api/fans/test", { method: "POST", body: JSON.stringify({ zone, speed, duration: 5 }) }); notify(`正在以 ${speed}% 测试 5 秒`); }
    catch (error) { notify(error.message, true); }
  });
});

function renderCurve(root, points) {
  $(".curve-table", root).innerHTML = points.map((point, index) => `
    <div class="curve-row">
      <label>温度 <input type="number" class="point-temp" min="0" max="120" step="0.5" value="${point.temperature}"> °C</label>
      <label>风速 <input type="number" class="point-speed" min="1" max="100" value="${point.speed}"> %</label>
      <button type="button" aria-label="删除控制点" title="删除控制点">×</button>
    </div>`).join("");
  drawCurve(root);
}

function readCurve(root) {
  return $$(".curve-row", root).map(row => ({ temperature: Number($(".point-temp", row).value), speed: Number($(".point-speed", row).value) }));
}

function drawCurve(root) {
  const svg = $("svg", root);
  const points = readCurve(root);
  const x = temp => 24 + (Math.max(0, Math.min(120, temp)) / 120) * 432;
  const y = speed => 160 - (Math.max(0, Math.min(100, speed)) / 100) * 140;
  $(".grid", svg).innerHTML = [20, 55, 90, 125, 160].map(value => `<line x1="24" y1="${value}" x2="456" y2="${value}"></line>`).join("");
  $("polyline", svg).setAttribute("points", points.map(point => `${x(point.temperature)},${y(point.speed)}`).join(" "));
  $(".dots", svg).innerHTML = points.map(point => `<circle cx="${x(point.temperature)}" cy="${y(point.speed)}" r="5"></circle>`).join("");
}

async function loadDisks(rescan = false) {
  try {
    state.disks = await api(rescan ? "/api/disks/rescan" : "/api/disks", rescan ? { method: "POST", body: "{}" } : {});
    renderDisks();
  } catch (error) { $("#disk-list").innerHTML = `<p class="empty">${escapeHTML(error.message)}</p>`; }
}

function renderDisks() {
  const selected = new Set(state.config?.hdd.devices || []);
  $("#disk-list").innerHTML = state.disks.length ? state.disks.map(disk => `
    <label class="disk">
      <input type="checkbox" value="${escapeHTML(disk.id)}" ${selected.has(disk.id) ? "checked" : ""}>
      <span class="disk-name"><strong>${escapeHTML(disk.model || "未知型号")}</strong><span>${formatSize(disk.size)} · ${escapeHTML(disk.serial || "无序列号")}</span></span>
      <span class="disk-path" title="${escapeHTML(disk.id)}">${escapeHTML(disk.id)}</span>
      <span class="disk-temp">${disk.temperature == null ? "--" : `${disk.temperature.toFixed(1)}°C`}</span>
      <span class="disk-state">${escapeHTML(disk.state)}</span>
    </label>`).join("") : '<p class="empty">未发现可监控硬盘</p>';
}

$("#rescan").addEventListener("click", () => loadDisks(true));

$("#save").addEventListener("click", async () => {
  try {
    readForm();
    state.config = await api("/api/config", { method: "PUT", body: JSON.stringify(state.config) });
    notify("配置已保存并应用");
    await refresh();
  } catch (error) { notify(error.message, true); }
});

function readForm() {
  for (const name of ["cpu", "hdd"]) {
    const root = $(`[data-controller="${name}"]`);
    const target = state.config[name];
    target.enabled = $(".enabled", root).checked;
    target.mode = root.dataset.mode;
    target.fixed_speed = Number($(".fixed-speed", root).value);
    target.curve = readCurve(root);
  }
  state.config.hdd.devices = $$("#disk-list input:checked").map(input => input.value);
  state.config.server.listen = $("#listen").value;
  state.config.server.session_timeout = $("#session-timeout").value;
  state.config.ipmi.command = $("#ipmi-command").value;
  state.config.ipmi.timeout = $("#ipmi-timeout").value;
  state.config.ipmi.exit_speed = Number($("#exit-speed").value);
  state.config.cpu.poll_interval = $("#cpu-poll").value;
  state.config.hdd.poll_interval = $("#hdd-poll").value;
  state.config.cpu.safe_speed = Number($("#cpu-safe").value);
  state.config.hdd.safe_speed = Number($("#hdd-safe").value);
  state.config.cpu.critical_temperature = Number($("#cpu-critical").value);
  state.config.hdd.critical_temperature = Number($("#hdd-critical").value);
  state.config.hdd.avoid_wakeup = $("#avoid-wakeup").checked;
}

async function refresh() {
  try {
    const [status, events] = await Promise.all([api("/api/status"), api("/api/events")]);
    state.status = status;
    updateMetric("#cpu-temp", status.cpu.temperature, "°C");
    updateMetric("#hdd-temp", status.hdd.temperature, "°C");
    updateMetric("#cpu-speed", status.cpu.applied_speed, "%");
    updateMetric("#hdd-speed", status.hdd.applied_speed, "%");
    const healthy = status.full_mode && !status.ipmi_error;
    $("#system-state").textContent = healthy ? "运行正常 · FULL" : "硬件连接异常";
    $("#system-state").classList.toggle("error", !healthy);
    $("[data-controller=cpu] .controller-status").textContent = status.cpu.error || status.cpu.status;
    $("[data-controller=hdd] .controller-status").textContent = status.hdd.error || status.hdd.status;
    renderFans("cpu", status.fans || [], status.fan_error);
    renderFans("hdd", status.fans || [], status.fan_error);
    const hardwareError = [status.ipmi_error, status.fan_error].filter(Boolean).join("\n");
    if (hardwareError && hardwareError !== state.lastHardwareError) notify(hardwareError, true);
    state.lastHardwareError = hardwareError;
    if (status.disks?.length) { state.disks = mergeDisks(state.disks, status.disks); renderDisks(); }
    renderEvents(events);
  } catch (error) {
    if (error.message === "authentication required") return location.reload();
    $("#system-state").textContent = "连接中断";
    $("#system-state").classList.add("error");
  }
}

function mergeDisks(discovered, readings) {
  const map = new Map(readings.map(disk => [disk.id, disk]));
  return discovered.map(disk => ({ ...disk, ...(map.get(disk.id) || {}) }));
}
function renderFans(controller, fans, error) {
  const group = controller === "cpu" ? "cpu" : "system";
  const readings = fans.filter(fan => fan.group === group || (group === "system" && fan.group === "other"));
  const root = $(`[data-controller="${controller}"] .fan-readings`);
  root.innerHTML = readings.length ? readings.map(fan => `
    <span class="fan-reading"><span>${escapeHTML(fan.name)}</span><strong>${Number(fan.rpm)} RPM</strong></span>`).join("")
    : `<span class="empty">${error ? "转速读取失败" : "未发现转速传感器"}</span>`;
}
function updateMetric(selector, value, suffix) { $(selector).textContent = value == null || value === 0 ? "--" : `${Number(value).toFixed(suffix === "%" ? 0 : 1)}${suffix}`; }
function renderEvents(events) {
  $("#events").innerHTML = events.length ? events.slice(0, 20).map(event => `<div class="event"><time>${new Date(event.time).toLocaleString()}</time><strong class="${event.level}">${escapeHTML(event.level)}</strong><span>${escapeHTML(event.message)}</span></div>`).join("") : '<p class="empty">暂无事件</p>';
}
function notify(message, error = false) {
  return new Promise(resolve => {
    noticeQueue.push({ message, error, resolve });
    showNextNotice();
  });
}
function showNextNotice() {
  if (noticeActive || noticeQueue.length === 0) return;
  noticeActive = true;
  const notice = noticeQueue.shift();
  const dialog = $("#notice-dialog");
  $("#notice-title").textContent = notice.error ? "操作失败" : "操作提示";
  $("#notice-message").textContent = notice.message;
  $("#notice-mark").textContent = notice.error ? "!" : "i";
  dialog.classList.toggle("error", notice.error);
  dialog.addEventListener("close", () => {
    noticeActive = false;
    notice.resolve();
    showNextNotice();
  }, { once: true });
  dialog.showModal();
}
function formatSize(bytes) { if (!bytes) return "--"; const units = ["B", "KB", "MB", "GB", "TB"]; const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1000)), 4); return `${(bytes / 1000 ** index).toFixed(index > 2 ? 1 : 0)} ${units[index]}`; }
function escapeHTML(value) { return String(value ?? "").replace(/[&<>"']/g, character => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[character]); }

$("#password-form").addEventListener("submit", async event => {
  event.preventDefault();
  try {
    await api("/api/auth/password", { method: "PUT", body: JSON.stringify({ current: $("#current-password").value, next: $("#new-password").value }) });
    await notify("密码已修改，请重新登录"); location.reload();
  } catch (error) { notify(error.message, true); }
});

boot().catch(error => { showLogin(true); notify(error.message, true); });
