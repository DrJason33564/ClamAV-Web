import {
  api,
  el,
  escapeHtml,
  formatCompactDate,
  objectOrEmpty,
  showToast
} from "./shared.js";

let historyPollTimer = 0;
let historyPage = 1;
let historyTotal = 0;
const historyPageSize = 10;

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
  return start + "-" + (page * historyPageSize);
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
  const data = await api(
    "/api/results/lookups?scope=" + encodeURIComponent(historyScope(historyPage)),
    {method: "POST"}
  );
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
  historyPollTimer = setTimeout(() => {
    pollHistoryLookup(lookupID).catch(handleLoadError);
  }, 1000);
}

function renderDetectionDetail(data) {
  const result = objectOrEmpty(data);
  const detections = Array.isArray(result.detections) ? result.detections : [];
  el("historyDetailTitle").textContent = "任务ID: " + (result.job_id || "");
  el("historyDetections").innerHTML = detections.map(item => (
    "<div class=\"detection-card\">" +
      "<p>威胁文件: <span>" + escapeHtml(item.source_file || "") + "</span></p>" +
      "<p>检出原因: <span>" + escapeHtml(item.detection_reason || "") + "</span></p>" +
    "</div>"
  )).join("");
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

function handleLoadError(err) {
  el("historyStatus").textContent = "加载失败";
  alert(err.message);
}

export function openHistory() {
  startHistoryLookup(1).catch(handleLoadError);
}

export function closeHistory() {
  clearTimeout(historyPollTimer);
}

export function initHistory() {
  document.addEventListener("click", event => {
    const job = event.target.closest && event.target.closest("[data-history-job]");
    if (job) {
      loadDetectionDetail(job.getAttribute("data-history-job")).catch(err => alert(err.message));
    }

    const page = event.target.closest && event.target.closest("[data-history-page]");
    if (page) {
      startHistoryLookup(Number(page.getAttribute("data-history-page"))).catch(handleLoadError);
    }
  });

  el("refreshHistory").addEventListener("click", () => {
    startHistoryLookup(historyPage).catch(handleLoadError);
  });
  el("historyBack").addEventListener("click", showHistoryList);
  el("cleanHistory").addEventListener("click", () => {
    cleanHistory().catch(err => alert(err.message));
  });
  el("historyPrevPage").addEventListener("click", () => {
    if (historyPage > 1) startHistoryLookup(historyPage - 1).catch(handleLoadError);
  });
  el("historyNextPage").addEventListener("click", () => {
    if (historyPage < historyTotalPages()) {
      startHistoryLookup(historyPage + 1).catch(handleLoadError);
    }
  });
}
