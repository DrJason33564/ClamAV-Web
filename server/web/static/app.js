const browsers = {
  manual: {
    selected: new Map(),
    currentPath: "",
    parentPath: "",
    rootSelect: "rootSelect",
    upBtn: "upBtn",
    pathLabel: "currentPath",
    browser: "browser",
    selectedLabel: "selected",
    multi: true
  },
  cron: {
    selected: "",
    currentPath: "",
    parentPath: "",
    rootSelect: "cronRootSelect",
    upBtn: "cronUpBtn",
    pathLabel: "cronCurrentPath",
    browser: "cronBrowser",
    selectedLabel: "cronSelected",
    targetInput: "cronTarget",
    multi: false
  },
  whitelist: {
    selected: "",
    currentPath: "",
    parentPath: "",
    rootSelect: "whitelistRootSelect",
    upBtn: "whitelistUpBtn",
    pathLabel: "whitelistCurrentPath",
    browser: "whitelistBrowser",
    selectedLabel: "whitelistSelected",
    addBtn: "addWhitelist",
    multi: false
  }
};
let editingCronRule = "";
let historyPollTimer = 0;
let quarantinePollTimer = 0;
let queueItems = [];
let reorderScanID = "";
let statusPollTimer = 0;
let historyPage = 1;
let historyTotal = 0;
const historyPageSize = 10;

const el = id => document.getElementById(id);

function optionalEl(id) {
  return document.getElementById(id);
}

async function api(url, options) {
  const res = await fetch(url, options);
  if (!res.ok) {
    const body = await res.json().catch(() => ({error: res.statusText}));
    throw new Error(body.error || body.message || res.statusText);
  }
  return res.json();
}

function classForStatus(status) {
  if (status === "ready" || status === "finished") return "ok";
  if (status === "running" || status === "queued") return "warn";
  return "bad";
}

function objectOrEmpty(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? value : {};
}

function formatTimestamp(value) {
  if (!value) return "未知";
  const text = String(value);
  const normalized = text.replace(/([+-]\d{2})(\d{2})$/, "$1:$2");
  const date = new Date(normalized);
  if (Number.isNaN(date.getTime())) return text.replace("T", " ").replace(/([+-]\d{2}:?\d{2}|Z)$/, "");
  const pad = number => String(number).padStart(2, "0");
  return date.getFullYear() + "-" +
    pad(date.getMonth() + 1) + "-" +
    pad(date.getDate()) + " " +
    pad(date.getHours()) + ":" +
    pad(date.getMinutes()) + ":" +
    pad(date.getSeconds());
}

function formatCompactDate(value) {
  const text = String(value || "");
  if (/^\d{14}$/.test(text)) {
    return text.slice(0, 4) + "-" + text.slice(4, 6) + "-" + text.slice(6, 8) + " " +
      text.slice(8, 10) + ":" + text.slice(10, 12) + ":" + text.slice(12, 14);
  }
  return formatTimestamp(text);
}

function historyTypeLabel(value) {
  if (value === "manual") return "手动扫描";
  if (value === "cron") return "定时任务";
  return value || "未知";
}

function historyResultView(value) {
  if (value === "clean") return {text: "完成", className: "ok"};
  if (value === "found") return {text: "发现威胁", className: "bad"};
  if (value === "error") return {text: "错误", className: "bad"};
  return {text: "未知 / 运行中", className: "muted"};
}

function actionLabel(value) {
  if (value === "move") return "移动到隔离区";
  if (value === "remove") return "直接删除";
  return "仅告警";
}

function scanIDChip(value) {
  const id = String(value || "");
  const body = id.startsWith("web-") ? id.slice(4) : id;
  return body.slice(0, 5) || "未知";
}

function dashboardState(data) {
  const source = objectOrEmpty(data.source);
  const scan = objectOrEmpty(source.scan);
  if (data.ping !== "ready") {
    return {
      label: "错误",
      detail: data.ping + " - " + data.ping_message,
      mode: "error",
      icon: "/static/icons/error.png"
    };
  }
  if (scan.active_job_id) {
    return {
      label: "正在扫描",
      detail: "任务：" + scan.active_job_id,
      mode: "running",
      icon: "/static/icons/running.png"
    };
  }
  return {
    label: "预备",
    detail: "准备就绪，未发现正在运行的扫描任务",
    mode: "ready",
    icon: "/static/icons/ready.png"
  };
}

function scanResultView(scan) {
  const status = scan.last_job_status;
  const result = scan.last_job_result;
  if (status === "running") return {text: "扫描进行中...", className: "warn"};
  if (status === "finished" && result === "clean") return {text: "未发现威胁", className: "ok"};
  if (status === "finished" && result === "found") return {text: "发现威胁", className: "bad"};
  if (status === "failed") return {text: "扫描失败", className: ""};
  return {text: "未知", className: ""};
}

async function loadStatus() {
  const data = await api("/api/status");
  const source = objectOrEmpty(data.source);
  const scan = objectOrEmpty(source.scan);
  const state = dashboardState(data);
  const ring = document.querySelector(".ring");
  const resultView = scanResultView(scan);
  el("scanState").textContent = state.label;
  el("scanState").className = "state " + state.mode;
  el("statusIcon").src = state.icon;
  el("summaryStatusIcon").src = state.icon;
  if (ring) ring.className = "ring " + (state.mode === "ready" ? "" : state.mode);
  el("scanResult").textContent = resultView.text;
  el("scanResult").className = resultView.className;
  el("updatedAt").textContent = formatTimestamp(source.updated_at || data.checked_at);
  el("statusRaw").textContent = data.source === undefined ? "" : JSON.stringify(data.source, null, 2);
}

async function loadBrowse(scope, path) {
  const state = browsers[scope];
  if (!state) return;
  const data = await api("/api/browse?path=" + encodeURIComponent(path || ""));
  const roots = Array.isArray(data.roots) ? data.roots : [];
  const entries = Array.isArray(data.entries) ? data.entries : [];
  state.currentPath = data.path;
  state.parentPath = data.parent || "";
  el(state.pathLabel).textContent = state.currentPath;
  el(state.upBtn).disabled = !state.parentPath;
  const rootSelect = el(state.rootSelect);
  rootSelect.innerHTML = roots.map(r => "<option value=\"" + escapeHtml(r) + "\">" + escapeHtml(r) + "</option>").join("");
  rootSelect.value = roots.find(r => state.currentPath === r || state.currentPath.startsWith(r + "/")) || roots[0] || "";

  el(state.browser).innerHTML = entries.map(entry => {
    const checked = state.multi ? state.selected.has(entry.path) : state.selected === entry.path;
    const inputType = state.multi ? "checkbox" : "radio";
    const inputName = state.multi ? "" : " name=\"" + escapeHtml(scope) + "-target\"";
    const icon = entry.is_dir ? "<img class=\"row-icon\" src=\"/static/icons/folder.png\" alt=\"目录\">" : "<img class=\"row-icon\" src=\"/static/icons/file.png\" alt=\"文件\">";
    const iconCell = entry.is_dir ? "<button class=\"entry-type\" data-browse-open=\"" + escapeHtml(scope) + "\" data-path=\"" + escapeHtml(entry.path) + "\" aria-label=\"进入目录\">" + icon + "</button>" : "<span class=\"entry-type\">" + icon + "</span>";
    const label = entry.is_dir ? "<button class=\"entry-name\" data-browse-open=\"" + escapeHtml(scope) + "\" data-path=\"" + escapeHtml(entry.path) + "\">" + escapeHtml(entry.name) + "</button>" : "<span class=\"entry-name\">" + escapeHtml(entry.name) + "</span>";
    return "<div class=\"entry\">" +
      "<input type=\"" + inputType + "\"" + inputName + " data-browse-select=\"" + escapeHtml(scope) + "\" data-path=\"" + escapeHtml(entry.path) + "\" " + (checked ? "checked" : "") + ">" +
      iconCell +
      "<div>" + label + "<div class=\"path\">" + escapeHtml(entry.path) + "</div></div>" +
    "</div>";
  }).join("") || "<div class=\"entry\"><span></span><span></span><span class=\"muted\">目录为空</span></div>";
}

function renderSelected(scope) {
  const state = browsers[scope];
  if (!state) return;
  if (state.multi) {
    el(state.selectedLabel).innerHTML = [...state.selected.values()].map(path => "<span class=\"chip\">" + escapeHtml(path) + "</span>").join("");
    el("scanBtn").disabled = state.selected.size === 0;
    return;
  }
  const targetInput = state.targetInput ? el(state.targetInput) : null;
  const target = state.selected || (targetInput ? targetInput.value.trim() : "");
  el(state.selectedLabel).innerHTML = target ? "<span class=\"chip\">" + escapeHtml(target) + "</span>" : "";
  if (state.addBtn) el(state.addBtn).disabled = !target;
}

function cronPayload() {
  return {
    enabled: el("cronEnabled").checked,
    minute: el("cronMinute").value.trim(),
    hour: el("cronHour").value.trim(),
    day: el("cronDay").value.trim(),
    month: el("cronMonth").value.trim(),
    weekday: el("cronWeekday").value.trim(),
    target: el("cronTarget").value.trim(),
    action: el("cronAction").value
  };
}

function fillCronForm(rule) {
  editingCronRule = rule ? rule.id : "";
  el("cronMinute").value = rule ? rule.minute : "30";
  el("cronHour").value = rule ? rule.hour : "3";
  el("cronDay").value = rule ? rule.day : "*";
  el("cronMonth").value = rule ? rule.month : "*";
  el("cronWeekday").value = rule ? rule.weekday : "*";
  el("cronTarget").value = rule ? rule.target : "";
  el("cronAction").value = rule ? rule.action : "warn";
  el("cronEnabled").checked = rule ? rule.enabled : true;
  el("saveCronRule").textContent = editingCronRule ? "更新规则" : "保存规则";
  browsers.cron.selected = rule ? rule.target : "";
  renderSelected("cron");
}

async function loadCronRules() {
  const data = await api("/api/cron/rules");
  const rules = data.rules || [];
  el("cronRules").innerHTML = rules.map(rule => {
    const expr = [rule.minute, rule.hour, rule.day, rule.month, rule.weekday].join(" ");
    return "<div class=\"rule\">" +
      "<input type=\"checkbox\" data-cron-enable=\"" + escapeHtml(rule.id) + "\" " + (rule.enabled ? "checked" : "") + ">" +
      "<div><strong>" + escapeHtml(expr) + "</strong><div class=\"path\">" + escapeHtml(rule.target) + " [" + escapeHtml(rule.action) + "]</div></div>" +
      "<small class=\"" + (rule.enabled ? "ok" : "muted") + "\">" + (rule.enabled ? "启用" : "禁用") + "</small>" +
      "<div class=\"rule-actions\"><button data-cron-edit=\"" + escapeHtml(rule.id) + "\">编辑</button><button class=\"danger\" data-cron-delete=\"" + escapeHtml(rule.id) + "\">删除</button></div>" +
    "</div>";
  }).join("") || "<div class=\"rule\"><span></span><span class=\"muted\">暂无定时规则</span><span></span><span></span></div>";
}

function sleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms));
}

async function reloadCronRulesSoon() {
  await sleep(50);
  await loadCronRules();
}

function renderWhitelist(entries) {
  const items = Array.isArray(entries) ? entries : [];
  el("whitelistEntries").innerHTML = items.map(entry => {
    return "<div class=\"whitelist-entry\">" +
      "<span class=\"path\">" + escapeHtml(entry.path) + "</span>" +
      "<button class=\"danger\" data-whitelist-delete=\"" + escapeHtml(entry.path) + "\" type=\"button\">删除</button>" +
    "</div>";
  }).join("") || "<div class=\"whitelist-entry empty\"><span class=\"muted\">暂无信任区条目</span><span></span></div>";
}

async function loadWhitelist() {
  const data = await api("/api/whitelist");
  renderWhitelist(data.entries || []);
}

async function addWhitelist() {
  const target = browsers.whitelist.selected;
  if (!target) return;
  await api("/api/whitelist", {
    method: "POST",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify({path: target})
  });
  browsers.whitelist.selected = "";
  renderSelected("whitelist");
  await loadWhitelist();
}

async function deleteWhitelist(path) {
  await api("/api/whitelist", {
    method: "DELETE",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify({path})
  });
  if (browsers.whitelist.selected === path) {
    browsers.whitelist.selected = "";
    renderSelected("whitelist");
  }
  await loadWhitelist();
}

function showQueueList() {
  el("queueListView").hidden = false;
  el("queueDetailView").hidden = true;
}

function renderQueueList(items) {
  queueItems = Array.isArray(items) ? items : [];
  el("queueList").innerHTML = queueItems.map((item, index) => {
    const status = item.status === "running" ? "running" : "queued";
    const queueNumber = Number.isFinite(Number(item.queue_number)) ? Number(item.queue_number) : index;
    const disabled = status === "running" ? " disabled" : "";
    return "<div class=\"task-card\" data-queue-detail=\"" + escapeHtml(item.id || "") + "\" role=\"button\" tabindex=\"0\">" +
      "<span class=\"queue-number\">" + escapeHtml(queueNumber) + "</span>" +
      "<span class=\"task-id-chip " + status + "\">" + escapeHtml(scanIDChip(item.id)) + "</span>" +
      "<span class=\"task-card-main\"><strong>" + escapeHtml(item.id || "") + "</strong><small>" + escapeHtml(status === "running" ? "正在扫描" : "等待中") + "</small></span>" +
      "<span class=\"task-card-actions\">" +
        "<button class=\"secondary reorder-job\" data-queue-reorder=\"" + escapeHtml(item.id || "") + "\" type=\"button\"" + disabled + ">排序</button>" +
        "<button class=\"cancel-job\" data-queue-cancel=\"" + escapeHtml(item.id || "") + "\" type=\"button\"" + disabled + "><img src=\"/static/icons/close_red.png\" alt=\"删除\"></button>" +
      "</span>" +
      "</div>";
  }).join("") || "<div class=\"history-empty muted\">队列内暂无任务</div>";
}

async function loadQueue() {
  showQueueList();
  el("queueStatus").textContent = "正在加载...";
  const data = await api("/api/scans");
  renderQueueList(data);
  el("queueStatus").textContent = "加载完成";
}

function showReorderModal(scanID) {
  reorderScanID = scanID;
  el("queueTargetInput").value = "";
  el("queueModalLayer").hidden = false;
  el("queueTargetInput").focus();
}

function hideReorderModal() {
  reorderScanID = "";
  el("queueModalLayer").hidden = true;
}

async function confirmReorder() {
  const queueNumber = Number(el("queueTargetInput").value);
  if (!reorderScanID || !Number.isFinite(queueNumber)) return;
  try {
    const data = await api("/api/scans/reorder", {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({id: reorderScanID, queue_number: queueNumber})
    });
    if (data && data.status && data.status !== "success") throw new Error(data.error || data.message || "reorder failed");
  } catch (err) {
    showToast("排序失败: " + err.message, "bad");
    return;
  }
  hideReorderModal();
  showToast("排序成功", "ok");
  loadQueue().catch(err => {
    el("queueStatus").textContent = "加载失败";
    showToast("任务队列加载失败: " + err.message, "bad");
  });
}

async function cancelQueueTask(scanID) {
  if (!confirm("删除此任务？")) return;
  try {
    const data = await api("/api/scans/cancel", {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({id: scanID, cancel: "Y"})
    });
    if (data && data.status && data.status !== "success") throw new Error(data.error || data.message || "cancel failed");
  } catch (err) {
    showToast("删除失败: " + err.message, "bad");
    return;
  }
  showToast("删除成功", "ok");
  loadQueue().catch(err => {
    el("queueStatus").textContent = "加载失败";
    showToast("任务队列加载失败: " + err.message, "bad");
  });
}

function renderQueueDetail(scanID) {
  const item = queueItems.find(entry => entry.id === scanID);
  if (!item) {
    showToast("任务不存在或已完成", "bad");
    return;
  }
  const targets = Array.isArray(item.targets) ? item.targets : [];
  el("queueDetailTitle").textContent = "任务ID: " + (item.id || "");
  el("queueDetailTargets").innerHTML = targets.map(target => "<div class=\"target-line\">" + escapeHtml(target) + "</div>").join("") || "<div class=\"target-line muted\">暂无目标</div>";
  el("queueDetailAction").textContent = actionLabel(item.action);
  el("queueDetailStarted").textContent = item.status === "queued" ? "排队中..." : formatTimestamp(item.started_at);
  el("queueListView").hidden = true;
  el("queueDetailView").hidden = false;
}

function showHistoryList() {
  el("historyListView").hidden = false;
  el("historyDetailView").hidden = true;
}

function renderHistoryList(results) {
  const items = Array.isArray(results) ? results : [];
  el("historyList").innerHTML = items.map(item => {
    const result = historyResultView(item.result);
    return "<button class=\"history-card\" data-history-job=\"" + escapeHtml(item.id) + "\" type=\"button\">" +
      "<span><strong>" + escapeHtml(formatCompactDate(item.date)) + "</strong><small>任务日期</small></span>" +
      "<span><strong>" + escapeHtml(historyTypeLabel(item.type)) + "</strong><small>任务类型</small></span>" +
      "<span><strong class=\"history-status-line\">任务状态: <em class=\"" + result.className + "\">" + escapeHtml(result.text) + "</em></strong><small>任务状态</small></span>" +
      "</button>";
  }).join("") || "<div class=\"history-empty muted\">暂无历史任务</div>";
}

function historyTotalPages() {
  return Math.ceil(historyTotal / historyPageSize);
}

function historyScope(page) {
  const start = (page - 1) * historyPageSize + 1;
  const end = page * historyPageSize;
  return start + "-" + end;
}

function historyPageNumbers(totalPages) {
  if (totalPages <= 5) return Array.from({length: totalPages}, (_, index) => index + 1);
  if (historyPage > totalPages - 5) {
    return Array.from({length: 5}, (_, index) => totalPages - 4 + index);
  }
  return [historyPage, historyPage + 1, historyPage + 2, "...", totalPages];
}

function renderHistoryPagination(total) {
  historyTotal = Number(total) || 0;
  const pagination = el("historyPagination");
  if (historyTotal <= 0) {
    pagination.hidden = true;
    el("historyPages").innerHTML = "";
    return;
  }
  const totalPages = historyTotalPages();
  if (historyPage > totalPages) historyPage = totalPages;
  pagination.hidden = false;
  el("historyPrevPage").disabled = historyPage <= 1;
  el("historyNextPage").disabled = historyPage >= totalPages;
  el("historyPages").innerHTML = historyPageNumbers(totalPages).map(page => {
    if (page === "...") return "<span class=\"page-ellipsis\">...</span>";
    const active = page === historyPage ? " active" : "";
    return "<button class=\"page-number" + active + "\" data-history-page=\"" + page + "\" type=\"button\">" + page + "</button>";
  }).join("");
}

async function startHistoryLookup(page) {
  clearTimeout(historyPollTimer);
  historyPage = Number.isInteger(page) && page > 0 ? page : historyPage;
  showHistoryList();
  el("historyStatus").textContent = "正在加载...";
  el("historyList").innerHTML = "";
  el("historyPagination").hidden = true;
  const data = await api("/api/results/lookups?scope=" + encodeURIComponent(historyScope(historyPage)), {method: "POST"});
  if (!data.lookup_id) throw new Error("missing lookup_id");
  await pollHistoryLookup(data.lookup_id);
}

async function pollHistoryLookup(lookupID) {
  const data = await api("/api/results/lookups/" + encodeURIComponent(lookupID));
  if (data.status === "success") {
    el("historyStatus").textContent = "加载完成";
    renderHistoryPagination(data.total);
    renderHistoryList(data.results || []);
    return;
  }
  if (data.status === "failed") {
    throw new Error(data.error || "历史任务加载失败");
  }
  el("historyStatus").textContent = "正在加载...";
  historyPollTimer = setTimeout(() => pollHistoryLookup(lookupID).catch(err => {
    el("historyStatus").textContent = "加载失败";
    alert(err.message);
  }), 1000);
}

function renderDetectionDetail(data) {
  const result = objectOrEmpty(data);
  const detections = Array.isArray(result.detections) ? result.detections : [];
  el("historyDetailTitle").textContent = "任务ID: " + (result.job_id || "");
  el("historyDetections").innerHTML = detections.map(item => {
    return "<div class=\"detection-card\">" +
      "<p>威胁文件: <span>" + escapeHtml(item.source_file || "") + "</span></p>" +
      "<p>检出原因: <span>" + escapeHtml(item.detection_reason || "") + "</span></p>" +
    "</div>";
  }).join("");
  el("historyDetectionLogSection").hidden = detections.length === 0;
  el("historyDetectionLog").textContent = result.original || "";
  el("historyTaskLog").textContent = result.log || "";
  el("historyListView").hidden = true;
  el("historyDetailView").hidden = false;
}

async function loadDetectionDetail(jobID) {
  const data = await api("/api/results/detection", {
    method: "POST",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify({job_id: jobID})
  });
  renderDetectionDetail(data);
}

async function cleanHistory() {
  if (!confirm("是否清理所有历史任务及其文件？")) return;
  const data = await api("/api/results/clean?clean_all=Y", {method: "POST"});
  showToast("成功清理" + (Number(data.deleted) || 0) + "个历史任务文件", "ok");
  await startHistoryLookup(1);
}

function renderQuarantineList(subjects) {
  const items = Array.isArray(subjects) ? subjects : [];
  el("quarantineList").innerHTML = items.map(item => {
    return "<div class=\"quarantine-row\">" +
      "<img class=\"row-icon\" src=\"/static/icons/file.png\" alt=\"\">" +
      "<div class=\"quarantine-file\"><strong>" + escapeHtml(item.name || "") + "</strong><small>" + escapeHtml(item.source_file || "") + "</small></div>" +
      "<button class=\"secondary\" data-quarantine-recover=\"" + escapeHtml(item.name || "") + "\" type=\"button\">恢复</button>" +
      "<button class=\"danger\" data-quarantine-delete=\"" + escapeHtml(item.name || "") + "\" type=\"button\">删除</button>" +
    "</div>";
  }).join("") || "<div class=\"history-empty muted\">隔离区为空</div>";
}

async function startQuarantineLookup() {
  clearTimeout(quarantinePollTimer);
  el("quarantineStatus").textContent = "正在加载...";
  el("quarantineList").innerHTML = "";
  const data = await api("/api/quarantine/lookups", {method: "POST"});
  if (!data.lookup_id) throw new Error("missing lookup_id");
  await pollQuarantineLookup(data.lookup_id);
}

async function pollQuarantineLookup(lookupID) {
  const data = await api("/api/quarantine/lookups/" + encodeURIComponent(lookupID));
  if (data.status === "success") {
    el("quarantineStatus").textContent = "加载完成";
    renderQuarantineList(data.subjects || []);
    return;
  }
  if (data.status === "failed") {
    throw new Error(data.error || "隔离区加载失败");
  }
  el("quarantineStatus").textContent = "正在加载...";
  quarantinePollTimer = setTimeout(() => pollQuarantineLookup(lookupID).catch(err => {
    el("quarantineStatus").textContent = "加载失败";
    showToast("隔离区加载失败：" + err.message, "bad");
  }), 1000);
}

async function recoverQuarantineFile(filename) {
  if (!confirm("是否恢复\"" + filename + "\"？")) return;
  try {
    const data = await api("/api/quarantine/recover/" + encodeURIComponent(filename), {method: "POST"});
    if (data && data.status && data.status !== "success") throw new Error(data.error || "recover failed");
  } catch (err) {
    showToast("文件恢复失败：" + err.message, "bad");
    return;
  }
  showToast("文件恢复成功", "ok");
  startQuarantineLookup().catch(err => {
    el("quarantineStatus").textContent = "加载失败";
    showToast("隔离区加载失败：" + err.message, "bad");
  });
}

async function deleteQuarantineFile(filename) {
  if (!confirm("是否永久删除\"" + filename + "\"？")) return;
  try {
    const data = await api("/api/quarantine/delete/" + encodeURIComponent(filename), {method: "POST"});
    if (data && data.status && data.status !== "success") throw new Error(data.error || "delete failed");
  } catch (err) {
    showToast("文件删除失败：" + err.message, "bad");
    return;
  }
  showToast("文件删除成功", "ok");
  startQuarantineLookup().catch(err => {
    el("quarantineStatus").textContent = "加载失败";
    showToast("隔离区加载失败：" + err.message, "bad");
  });
}

async function cleanQuarantine() {
  if (!confirm("是否清理隔离区所有文件？")) return;
  try {
    const data = await api("/api/quarantine/clean?clean_all=Y", {method: "POST"});
    if (data && data.status && data.status !== "success") throw new Error(data.error || "clean failed");
  } catch (err) {
    showToast("隔离区清理失败：" + err.message, "bad");
    return;
  }
  showToast("隔离区清理成功", "ok");
  startQuarantineLookup().catch(err => {
    el("quarantineStatus").textContent = "加载失败";
    showToast("隔离区加载失败：" + err.message, "bad");
  });
}

async function saveCronRule() {
  const payload = cronPayload();
  const url = editingCronRule ? "/api/cron/rules/" + encodeURIComponent(editingCronRule) : "/api/cron/rules";
  const method = editingCronRule ? "PUT" : "POST";
  await api(url, {method, headers: {"Content-Type": "application/json"}, body: JSON.stringify(payload)});
  fillCronForm(null);
  await reloadCronRulesSoon();
}

async function setCronEnabled(id, enabled) {
  await api("/api/cron/rules/" + encodeURIComponent(id) + "/enabled", {
    method: "PATCH",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify({enabled})
  });
  await reloadCronRulesSoon();
}

async function deleteCronRule(id) {
  await api("/api/cron/rules/" + encodeURIComponent(id), {method: "DELETE"});
  if (editingCronRule === id) fillCronForm(null);
  await reloadCronRulesSoon();
}

async function startScan() {
  const targets = [...browsers.manual.selected.values()];
  if (!targets.length) return;
  await api("/api/scans", {
    method: "POST",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify({targets, action: el("action").value})
  });
  browsers.manual.selected.clear();
  renderSelected("manual");
  await loadBrowse("manual", browsers.manual.currentPath);
  await sleep(500);
  await loadStatus();
}

function escapeHtml(value) {
  return String(value).replace(/[&<>"']/g, ch => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[ch]));
}

function showToast(message, type) {
  const toast = el("toast");
  toast.textContent = message;
  toast.className = "toast show " + (type || "");
  clearTimeout(showToast.timer);
  showToast.timer = setTimeout(() => {
    toast.className = "toast";
  }, 2400);
}

function isDebugging() {
  return localStorage.getItem("debugging") === "true";
}

function applyDebugMode() {
  const enabled = isDebugging();
  el("debugMode").checked = enabled;
  el("rawStatusCard").hidden = !enabled;
}

function pollIntervalSeconds() {
  const value = Number(localStorage.getItem("statusPollInterval") || "5");
  return Number.isInteger(value) && value > 0 ? value : 5;
}

function validatePollInterval(value) {
  return /^[1-9]\d*$/.test(value);
}

function applyPollIntervalSetting() {
  const value = String(pollIntervalSeconds());
  el("pollInterval").value = value;
  el("pollIntervalError").hidden = true;
}

function startStatusPolling() {
  clearInterval(statusPollTimer);
  statusPollTimer = setInterval(() => loadStatus().catch(console.error), pollIntervalSeconds() * 1000);
}

function handlePollIntervalInput(event) {
  const input = event.target;
  const value = input.value.replace(/\D/g, "");
  if (input.value !== value) input.value = value;
  const valid = validatePollInterval(value);
  el("pollIntervalError").hidden = valid;
  if (!valid) return;
  localStorage.setItem("statusPollInterval", value);
  startStatusPolling();
}

function openDrawer(name) {
  document.querySelectorAll("[data-drawer-panel]").forEach(panel => {
    panel.hidden = panel.getAttribute("data-drawer-panel") !== name;
  });
  if (name === "whitelist") loadWhitelist().catch(err => alert(err.message));
  if (name === "history") startHistoryLookup(1).catch(err => {
    el("historyStatus").textContent = "加载失败";
    alert(err.message);
  });
  if (name === "queue") loadQueue().catch(err => {
    el("queueStatus").textContent = "加载失败";
    showToast("任务队列加载失败: " + err.message, "bad");
  });
  if (name === "quarantine") startQuarantineLookup().catch(err => {
    el("quarantineStatus").textContent = "加载失败";
    showToast("隔离区加载失败：" + err.message, "bad");
  });
  el("drawerLayer").classList.toggle("settings-open", name === "settings");
  el("drawerLayer").classList.add("open");
  el("drawerLayer").setAttribute("aria-hidden", "false");
  document.body.classList.add("drawer-open");
}

function closeDrawer() {
  clearTimeout(historyPollTimer);
  clearTimeout(quarantinePollTimer);
  hideReorderModal();
  el("drawerLayer").classList.remove("open");
  el("drawerLayer").classList.remove("settings-open");
  el("drawerLayer").setAttribute("aria-hidden", "true");
  document.body.classList.remove("drawer-open");
}

document.addEventListener("click", event => {
  const drawerOpen = event.target.closest && event.target.closest("[data-drawer-open]");
  if (drawerOpen) openDrawer(drawerOpen.getAttribute("data-drawer-open"));
  const drawerClose = event.target.closest && event.target.closest("[data-drawer-close]");
  if (drawerClose) closeDrawer();
  const open = event.target.closest && event.target.closest("[data-browse-open]");
  if (open) loadBrowse(open.getAttribute("data-browse-open"), open.getAttribute("data-path")).catch(err => alert(err.message));
  const edit = event.target.closest && event.target.closest("[data-cron-edit]");
  if (edit) api("/api/cron/rules").then(data => {
    const rule = (data.rules || []).find(item => item.id === edit.getAttribute("data-cron-edit"));
    if (rule) fillCronForm(rule);
  }).catch(err => alert(err.message));
  const del = event.target.closest && event.target.closest("[data-cron-delete]");
  if (del && confirm("删除这条定时规则？")) deleteCronRule(del.getAttribute("data-cron-delete")).catch(err => alert(err.message));
  const whitelistDelete = event.target.closest && event.target.closest("[data-whitelist-delete]");
  if (whitelistDelete && confirm("删除这条信任区？")) deleteWhitelist(whitelistDelete.getAttribute("data-whitelist-delete")).catch(err => alert(err.message));
  const queueReorder = event.target.closest && event.target.closest("[data-queue-reorder]");
  if (queueReorder) {
    showReorderModal(queueReorder.getAttribute("data-queue-reorder"));
    return;
  }
  const queueCancel = event.target.closest && event.target.closest("[data-queue-cancel]");
  if (queueCancel) {
    cancelQueueTask(queueCancel.getAttribute("data-queue-cancel"));
    return;
  }
  const queueDetail = event.target.closest && event.target.closest("[data-queue-detail]");
  if (queueDetail) renderQueueDetail(queueDetail.getAttribute("data-queue-detail"));
  const historyJob = event.target.closest && event.target.closest("[data-history-job]");
  if (historyJob) loadDetectionDetail(historyJob.getAttribute("data-history-job")).catch(err => alert(err.message));
  const historyPageButton = event.target.closest && event.target.closest("[data-history-page]");
  if (historyPageButton) startHistoryLookup(Number(historyPageButton.getAttribute("data-history-page"))).catch(err => {
    el("historyStatus").textContent = "加载失败";
    alert(err.message);
  });
  const quarantineRecover = event.target.closest && event.target.closest("[data-quarantine-recover]");
  if (quarantineRecover) recoverQuarantineFile(quarantineRecover.getAttribute("data-quarantine-recover"));
  const quarantineDelete = event.target.closest && event.target.closest("[data-quarantine-delete]");
  if (quarantineDelete) deleteQuarantineFile(quarantineDelete.getAttribute("data-quarantine-delete"));
});

document.addEventListener("change", event => {
  const browseSelect = event.target.getAttribute && event.target.getAttribute("data-browse-select");
  const path = event.target.getAttribute && event.target.getAttribute("data-path");
  if (browseSelect && path) {
    const state = browsers[browseSelect];
    if (state && state.multi) {
      if (event.target.checked) state.selected.set(path, path);
      else state.selected.delete(path);
      renderSelected(browseSelect);
    } else if (state && event.target.checked) {
      state.selected = path;
      if (state.targetInput) el(state.targetInput).value = path;
      renderSelected(browseSelect);
    }
  }
  const cronEnable = event.target.getAttribute && event.target.getAttribute("data-cron-enable");
  if (cronEnable) setCronEnabled(cronEnable, event.target.checked).catch(err => alert(err.message));
});

document.querySelectorAll("[data-browse-root]").forEach(control => {
  control.addEventListener("change", e => loadBrowse(e.target.getAttribute("data-browse-root"), e.target.value).catch(err => alert(err.message)));
});
document.querySelectorAll("[data-browse-up]").forEach(control => {
  control.addEventListener("click", e => {
    const scope = e.currentTarget.getAttribute("data-browse-up");
    const state = browsers[scope];
    if (state && state.parentPath) loadBrowse(scope, state.parentPath).catch(err => alert(err.message));
  });
});
el("scanBtn").addEventListener("click", () => startScan().catch(err => alert(err.message)));
el("cronTarget").addEventListener("input", e => {
  browsers.cron.selected = e.target.value.trim();
  renderSelected("cron");
});
el("addWhitelist").addEventListener("click", () => addWhitelist().catch(err => alert(err.message)));
el("refreshHistory").addEventListener("click", () => startHistoryLookup(historyPage).catch(err => {
  el("historyStatus").textContent = "加载失败";
  alert(err.message);
}));
el("historyBack").addEventListener("click", showHistoryList);
el("cleanHistory").addEventListener("click", () => cleanHistory().catch(err => alert(err.message)));
el("historyPrevPage").addEventListener("click", () => {
  if (historyPage <= 1) return;
  startHistoryLookup(historyPage - 1).catch(err => {
    el("historyStatus").textContent = "加载失败";
    alert(err.message);
  });
});
el("historyNextPage").addEventListener("click", () => {
  if (historyPage >= historyTotalPages()) return;
  startHistoryLookup(historyPage + 1).catch(err => {
    el("historyStatus").textContent = "加载失败";
    alert(err.message);
  });
});
el("refreshQueue").addEventListener("click", () => loadQueue().catch(err => {
  el("queueStatus").textContent = "加载失败";
  showToast("任务队列加载失败: " + err.message, "bad");
}));
el("queueBack").addEventListener("click", showQueueList);
el("queueModalClose").addEventListener("click", hideReorderModal);
el("queueModalConfirm").addEventListener("click", () => confirmReorder());
el("queueTargetInput").addEventListener("keydown", event => {
  if (event.key === "Enter") confirmReorder();
});
el("refreshQuarantine").addEventListener("click", () => startQuarantineLookup().catch(err => {
  el("quarantineStatus").textContent = "加载失败";
  showToast("隔离区加载失败：" + err.message, "bad");
}));
el("cleanQuarantine").addEventListener("click", cleanQuarantine);
el("saveCronRule").addEventListener("click", () => saveCronRule().catch(err => alert(err.message)));
el("newCronRule").addEventListener("click", () => fillCronForm(null));
el("reloadCron").addEventListener("click", () => api("/api/cron/reload", {method:"POST"}).then(loadCronRules).catch(err => alert(err.message)));
el("refreshStatus").addEventListener("click", () => {
  loadStatus()
    .then(() => showToast("页面已刷新", "ok"))
    .catch(err => {
      showToast("页面刷新失败", "bad");
      console.error(err);
    });
});
el("debugMode").addEventListener("change", e => {
  localStorage.setItem("debugging", e.target.checked ? "true" : "false");
  applyDebugMode();
});
el("pollInterval").addEventListener("input", handlePollIntervalInput);
document.addEventListener("keydown", event => {
  if (event.key === "Escape" && !el("queueModalLayer").hidden) {
    hideReorderModal();
    return;
  }
  if (event.key === "Escape") closeDrawer();
});

renderSelected("manual");
renderSelected("cron");
renderSelected("whitelist");
fillCronForm(null);
applyDebugMode();
applyPollIntervalSetting();
loadStatus().catch(console.error);
loadBrowse("manual", "").catch(console.error);
loadBrowse("cron", "").catch(console.error);
loadBrowse("whitelist", "").catch(console.error);
loadCronRules().catch(console.error);
loadWhitelist().catch(console.error);
startStatusPolling();
