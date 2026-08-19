const state = { modules: [], selectedModule: null, jobs: [], selectedJobId: null, poll: null, authenticated: false };
const authStorageKey = "suosi-control.user";
const routePrefix = window.location.pathname.startsWith("/Suosi/") ? "/Suosi" : "";
const route = path => `${routePrefix}${path}`;

const moduleFields = {
  "thoughts-export": [
    { name: "url", label: "知识库地址", type: "url", full: true, required: true, placeholder: "https://thoughts.teambition.com/workspaces/.../overview" },
    { name: "format", label: "正文格式", type: "select", full: true, options: [["docx", "DOCX"], ["html", "HTML"]] },
    { name: "overwrite", label: "覆盖已存在文件", type: "checkbox" },
    { name: "include_templates", label: "同时导出知识库模板", type: "checkbox" },
    { name: "dry_run", label: "仅生成目录预览", type: "checkbox" },
    { name: "retry_failed", label: "重试历史失败项", type: "checkbox" }
  ],
  "tb-files": [
    { name: "project_url", label: "项目文件库地址", type: "url", full: true, required: true, placeholder: "https://www.teambition.com/project/.../works/..." },
    { name: "source", label: "数据来源", type: "select", options: [["browser", "浏览器登录态"], ["sdk", "OpenAPI SDK"]] },
    { name: "concurrency", label: "下载并发", type: "number", value: 2, min: 1, max: 8 },
    { name: "page_size", label: "单页数量", type: "number", value: 100, min: 10, max: 200 },
    { name: "max_file_size", label: "单文件上限（字节）", hint: "0 表示不限", type: "number", value: 0, min: 0 },
    { name: "download_assets", label: "发现后下载文件本体", type: "checkbox", checked: true },
    { name: "resume", label: "复用已有断点", type: "checkbox", checked: true },
    { name: "include_raw", label: "保存脱敏原始响应", type: "checkbox" },
    { name: "retry_failed", label: "重试失败下载", type: "checkbox" }
  ],
  "tb-tasks": [
    { name: "project", label: "项目 ID 或任务面板地址", type: "text", full: true, required: true, placeholder: "项目 ID 或 https://www.teambition.com/project/.../tasks/view/..." },
    { name: "concurrency", label: "采集并发", type: "number", value: 2, min: 1, max: 8 },
    { name: "since", label: "更新时间下限", hint: "可选", type: "datetime-local" },
    { name: "resume", label: "复用已有断点", type: "checkbox", checked: true },
    { name: "include_raw", label: "保存脱敏原始响应", type: "checkbox" },
    { name: "download_assets", label: "下载任务关联文件", type: "checkbox" }
  ]
};

const statusNames = { queued: "排队中", running: "运行中", succeeded: "已完成", partial: "部分完成", failed: "失败", cancelling: "取消中", cancelled: "已取消" };

async function api(path, options = {}) {
  const response = await fetch(route(path), { headers: { "Content-Type": "application/json", ...(options.headers || {}) }, ...options });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(payload.error || `请求失败（${response.status}）`);
  return payload;
}

async function initialize() {
  bindAuthEvents();
  window.addEventListener("hashchange", handleRoute);
  try {
    const session = await api("/api/auth/session");
    activateConsole(session.user);
  } catch {
    handleRoute();
  }
}

async function initializeConsole() {
  bindEvents();
  try {
    const [health, modulePayload] = await Promise.all([api("/api/health"), api("/api/modules")]);
    if (health.status === "ok") setHealth(true);
    state.modules = modulePayload.modules || [];
    renderModules();
    applyConsoleRoute();
    await refreshJobs();
    state.poll = window.setInterval(refreshJobs, 2500);
  } catch (error) {
    setHealth(false);
    showToast(error.message);
  }
}

function bindAuthEvents() {
  document.querySelectorAll("[data-auth-view]").forEach(button => button.addEventListener("click", () => navigate(`/${button.dataset.authView}`)));
  document.querySelector("#loginForm").addEventListener("submit", event => submitAuth(event, "login"));
  document.querySelector("#registerForm").addEventListener("submit", event => submitAuth(event, "register"));
  document.querySelector("#recoverForm").addEventListener("submit", event => submitAuth(event, "recover"));
  document.querySelector("#logoutButton").addEventListener("click", logout);
}

function routePath() { return window.location.hash.replace(/^#/, ""); }

function navigate(path, replace = false) {
  history[replace ? "replaceState" : "pushState"](null, "", `${window.location.pathname}${window.location.search}#${path}`);
  handleRoute();
}

function handleRoute() {
  const path = routePath();
  if (!state.authenticated) {
    if (path === "/register") showAuthView("register");
    else if (path === "/recover") showAuthView("recover");
    else showAuthView("login");
    return;
  }
  if (!path.startsWith("/console/")) {
    navigate(`/console/${state.modules[0]?.id || "thoughts-export"}`, true);
    return;
  }
  applyConsoleRoute();
}

function applyConsoleRoute() {
  const moduleID = routePath().replace(/^\/console\//, "");
  const module = state.modules.find(item => item.id === moduleID) || state.modules[0];
  if (!module) return;
  if (module.id !== moduleID) {
    navigate(`/console/${module.id}`, true);
    return;
  }
  selectModule(module.id);
}

function showAuthView(view) {
  document.querySelector("#authScreen").hidden = false;
  document.querySelector("#appShell").hidden = true;
  ["login", "register", "recover"].forEach(name => {
    const form = document.querySelector(`#${name}Form`);
    form.hidden = name !== view;
    const error = form.querySelector(".auth-error");
    error.hidden = true;
    error.textContent = "";
  });
}

async function submitAuth(event, action) {
  event.preventDefault();
  const form = event.currentTarget;
  if (!form.reportValidity()) return;
  const error = form.querySelector(".auth-error");
  error.hidden = true;
  const button = form.querySelector("[type=submit]");
  button.disabled = true;
  try {
    const values = Object.fromEntries(new FormData(form).entries());
    const result = await api(`/api/auth/${action}`, { method: "POST", body: JSON.stringify(values) });
    if (action === "recover") {
      navigate("/login", true);
      document.querySelector("#loginForm [name=name]").value = values.name;
      showToast("密码已修改，请使用新密码登录");
      return;
    }
    activateConsole(result.user);
  } catch (requestError) {
    error.textContent = requestError.message;
    error.hidden = false;
  } finally { button.disabled = false; }
}

function activateConsole(user) {
  localStorage.setItem(authStorageKey, JSON.stringify({ id: user.id, name: user.name, type: user.type }));
  state.authenticated = true;
  document.querySelector("#authScreen").hidden = true;
  document.querySelector("#appShell").hidden = false;
  if (!state.poll) initializeConsole();
  handleRoute();
}

async function logout() {
  try { await api("/api/auth/logout", { method: "POST", body: "{}" }); } catch { /* local state is still cleared */ }
  localStorage.removeItem(authStorageKey);
  state.authenticated = false;
  if (state.poll) window.clearInterval(state.poll);
  state.poll = null;
  navigate("/login", true);
}

function bindEvents() {
  document.querySelector("#jobForm").addEventListener("submit", submitJob);
  document.querySelector("#preflightButton").addEventListener("click", runPreflight);
  document.querySelector("#refreshButton").addEventListener("click", refreshJobs);
  document.querySelector("#moduleList").addEventListener("click", event => {
    const link = event.target.closest("[data-module-id]");
    if (link) {
      event.preventDefault();
      navigate(`/console/${link.dataset.moduleId}`);
    }
  });
  document.querySelector("#jobRows").addEventListener("click", event => {
    const button = event.target.closest("[data-job-id]");
    if (button) selectJob(button.dataset.jobId);
  });
  document.querySelector("#jobDetail").addEventListener("click", async event => {
    const button = event.target.closest("[data-cancel-id]");
    if (!button) return;
    button.disabled = true;
    try {
      await api(`/api/jobs/${button.dataset.cancelId}/cancel`, { method: "POST", body: "{}" });
      showToast("已发送取消请求");
      await refreshJobs();
    } catch (error) { showToast(error.message); }
  });
}

function renderModules() {
  document.querySelector("#moduleCount").textContent = String(state.modules.length);
  document.querySelector("#moduleList").innerHTML = state.modules.map((module, index) => `
    <a class="module-button" href="#/console/${encodeURIComponent(module.id)}" data-module-id="${escapeHTML(module.id)}">
      <span class="module-index">0${index + 1}</span>
      <span class="module-copy"><strong>${escapeHTML(module.name)}</strong><span>${escapeHTML(module.description)}</span></span>
    </a>`).join("");
}

function selectModule(id) {
  state.selectedModule = state.modules.find(module => module.id === id);
  document.querySelectorAll(".module-button").forEach(button => button.classList.toggle("active", button.dataset.moduleId === id));
  if (!state.selectedModule) return;
  document.querySelector("#formTitle").textContent = state.selectedModule.name;
  document.querySelector("#formDescription").textContent = state.selectedModule.description;
  document.querySelector("#credentialBadge").textContent = state.selectedModule.credential;
  document.querySelector("#dynamicFields").innerHTML = (moduleFields[id] || []).map(renderField).join("");
  document.querySelector("#preflightPanel").hidden = true;
  hideFormError();
}

function renderField(field) {
  const id = `field-${field.name}`;
  const layoutClass = field.layout ? ` ${field.layout}` : "";
  if (field.type === "checkbox") return `<div class="field toggle-field ${field.full ? "full" : ""}${layoutClass}"><input id="${id}" name="${field.name}" type="checkbox" ${field.checked ? "checked" : ""}><label for="${id}">${escapeHTML(field.label)}</label></div>`;
  const hint = field.hint ? `<small>${escapeHTML(field.hint)}</small>` : "";
  let control = "";
  if (field.type === "select") {
    control = `<select id="${id}" name="${field.name}">${field.options.map(option => `<option value="${option[0]}">${option[1]}</option>`).join("")}</select>`;
  } else {
    control = `<input id="${id}" name="${field.name}" type="${field.type}" value="${field.value ?? ""}" placeholder="${escapeHTML(field.placeholder || "")}" ${field.required ? "required" : ""} ${field.min !== undefined ? `min="${field.min}"` : ""} ${field.max !== undefined ? `max="${field.max}"` : ""}>`;
  }
  return `<div class="field ${field.full ? "full" : ""}${layoutClass}"><label for="${id}"><span>${escapeHTML(field.label)}</span>${hint}</label>${control}</div>`;
}

function collectInput() {
  const form = document.querySelector("#jobForm");
  if (!form.reportValidity()) return null;
  const input = {};
  for (const field of moduleFields[state.selectedModule.id] || []) {
    const element = form.elements[field.name];
    if (field.type === "checkbox") input[field.name] = element.checked;
    else if (field.type === "number") input[field.name] = Number(element.value || 0);
    else if (field.type === "datetime-local") input[field.name] = element.value ? new Date(element.value).toISOString() : "";
    else input[field.name] = element.value.trim();
  }
  return input;
}

async function runPreflight() {
  if (!state.selectedModule) return null;
  const input = collectInput();
  if (!input) return null;
  setBusy(true, "preflight");
  hideFormError();
  try {
    const result = await api("/api/preflight", { method: "POST", body: JSON.stringify({ module_id: state.selectedModule.id, input }) });
    renderPreflight(result);
    return result;
  } catch (error) {
    showFormError(error.message);
    return null;
  } finally { setBusy(false); }
}

function renderPreflight(result) {
  const panel = document.querySelector("#preflightPanel");
  panel.hidden = false;
  panel.innerHTML = `<div class="preflight-title"><span>预检结果</span><span>${result.ok ? "可以启动" : "需要处理"}</span></div>
    <ul class="check-list">${result.checks.map(check => `<li><span class="check-symbol ${check.ok ? "" : "failed"}">${check.ok ? "OK" : "!"}</span><span>${escapeHTML(check.message)}</span></li>`).join("")}</ul>
    ${result.warnings.length ? `<p class="warning-copy">${result.warnings.map(escapeHTML).join(" ")}</p>` : ""}`;
}

async function submitJob(event) {
  event.preventDefault();
  const input = collectInput();
  if (!input || !state.selectedModule) return;
  setBusy(true, "submit");
  hideFormError();
  try {
    const check = await api("/api/preflight", { method: "POST", body: JSON.stringify({ module_id: state.selectedModule.id, input }) });
    renderPreflight(check);
    if (!check.ok) throw new Error("预检未通过，请处理失败项后重试");
    const job = await api("/api/jobs", { method: "POST", body: JSON.stringify({ module_id: state.selectedModule.id, input }) });
    state.selectedJobId = job.id;
    showToast("任务已创建，完成后可下载 ZIP 归档");
    await refreshJobs();
  } catch (error) { showFormError(error.message); }
  finally { setBusy(false); }
}

async function refreshJobs() {
  try {
    const payload = await api("/api/jobs?limit=50");
    state.jobs = payload.jobs || [];
    renderJobRows();
    if (state.selectedJobId) {
      const selected = state.jobs.find(job => job.id === state.selectedJobId);
      if (selected) renderJobDetail(selected);
    }
    setHealth(true);
  } catch (error) {
    setHealth(false);
    if (!state.jobs.length) document.querySelector("#jobRows").innerHTML = `<tr><td colspan="6"><div class="table-empty">无法读取任务记录</div></td></tr>`;
  }
}

function renderJobRows() {
  const rows = document.querySelector("#jobRows");
  document.querySelector("#runningCount").textContent = String(state.jobs.filter(job => ["queued", "running", "cancelling"].includes(job.status)).length);
  if (!state.jobs.length) {
    rows.innerHTML = `<tr><td colspan="6"><div class="table-empty">还没有采集任务</div></td></tr>`;
    return;
  }
  rows.innerHTML = state.jobs.map(job => `<tr>
    <td><button class="job-link" type="button" data-job-id="${job.id}">${escapeHTML(job.id)}</button></td>
    <td>${escapeHTML(job.module_name)}</td>
    <td><span class="status-badge ${job.status}">${statusNames[job.status] || job.status}</span></td>
    <td>${escapeHTML(job.message)}</td>
    <td>${formatTime(job.created_at)}</td>
    <td><button class="row-action" type="button" data-job-id="${job.id}">打开</button></td>
  </tr>`).join("");
}

function selectJob(id) {
  state.selectedJobId = id;
  const job = state.jobs.find(item => item.id === id);
  if (job) renderJobDetail(job);
  document.querySelector("#jobDetail").scrollIntoView({ behavior: "smooth", block: "nearest" });
}

function renderJobDetail(job) {
  const active = ["queued", "running", "cancelling"].includes(job.status);
  document.querySelector("#jobDetail").className = "job-detail";
  document.querySelector("#jobDetail").innerHTML = `
    <div class="status-line"><span class="status-badge ${job.status}">${statusNames[job.status] || job.status}</span><span class="job-id">${escapeHTML(job.id)}</span></div>
    <h3 class="job-title">${escapeHTML(job.module_name)}</h3>
    <p class="job-message">${escapeHTML(job.message)}</p>
    <div class="activity-track ${active ? "" : "stopped"}" aria-hidden="true"></div>
    <dl class="job-facts">
      <div><dt>当前阶段</dt><dd>${escapeHTML(job.stage)}</dd></div>
      <div><dt>创建时间</dt><dd>${formatTime(job.created_at)}</dd></div>
      <div><dt>完成时间</dt><dd>${job.finished_at ? formatTime(job.finished_at) : "尚未完成"}</dd></div>
      <div><dt>归档位置</dt><dd>服务器归档（按模块、用户和任务隔离）</dd></div>
    </dl>
    ${job.error ? `<div class="job-error">${escapeHTML(job.error)}</div>` : ""}
    ${["succeeded", "partial"].includes(job.status) ? `<a class="button secondary download-archive" href="${route(`/api/jobs/${encodeURIComponent(job.id)}/download`)}">下载 ZIP</a>` : ""}
    ${active && job.status !== "cancelling" ? `<button class="button danger" type="button" data-cancel-id="${job.id}">取消任务</button>` : ""}`;
}

function setBusy(busy, action) {
  const preflight = document.querySelector("#preflightButton");
  const submit = document.querySelector("#submitButton");
  preflight.disabled = busy;
  submit.disabled = busy;
  preflight.textContent = busy && action === "preflight" ? "正在预检" : "执行预检";
  submit.textContent = busy && action === "submit" ? "正在创建" : "启动采集";
}

function setHealth(online) {
  document.querySelector("#healthDot").classList.toggle("online", online);
  document.querySelector("#healthText").textContent = online ? "服务运行正常" : "服务连接中断";
}

function showFormError(message) { const element = document.querySelector("#formError"); element.textContent = message; element.hidden = false; }
function hideFormError() { const element = document.querySelector("#formError"); element.hidden = true; element.textContent = ""; }
function showToast(message) { const toast = document.querySelector("#toast"); toast.textContent = message; toast.hidden = false; window.setTimeout(() => { toast.hidden = true; }, 2800); }
function formatTime(value) { return value ? new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }).format(new Date(value)) : ""; }
function escapeHTML(value) { return String(value ?? "").replace(/[&<>'"]/g, char => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;" })[char]); }

initialize();
