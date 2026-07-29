import {
  actionLabel,
  api,
  el,
  escapeHtml,
  formatTimestamp,
  scanIDChip,
  showToast
} from "./shared.js";

let queueItems = [];
let reorderScanID = "";

function showQueueList() {
  el("queueListView").hidden = false;
  el("queueDetailView").hidden = true;
}

function renderQueueList(items) {
  queueItems = Array.isArray(items) ? items : [];
  el("queueList").innerHTML = queueItems.map((item, index) => {
    const status = item.status === "running" ? "running" : "queued";
    const queueNumber = Number.isFinite(Number(item.queue_number))
      ? Number(item.queue_number)
      : index;
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
    if (data && data.status && data.status !== "success") {
      throw new Error(data.error || data.message || "reorder failed");
    }
  } catch (err) {
    showToast("排序失败: " + err.message, "bad");
    return;
  }
  hideReorderModal();
  showToast("排序成功", "ok");
  loadQueue().catch(handleLoadError);
}

async function cancelQueueTask(scanID) {
  if (!confirm("删除此任务？")) return;
  try {
    const data = await api("/api/scans/cancel", {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({id: scanID, cancel: "Y"})
    });
    if (data && data.status && data.status !== "success") {
      throw new Error(data.error || data.message || "cancel failed");
    }
  } catch (err) {
    showToast("删除失败: " + err.message, "bad");
    return;
  }
  showToast("删除成功", "ok");
  loadQueue().catch(handleLoadError);
}

function renderQueueDetail(scanID) {
  const item = queueItems.find(entry => entry.id === scanID);
  if (!item) {
    showToast("任务不存在或已完成", "bad");
    return;
  }
  const targets = Array.isArray(item.targets) ? item.targets : [];
  el("queueDetailTitle").textContent = "任务ID: " + (item.id || "");
  el("queueDetailTargets").innerHTML = targets.map(target => (
    "<div class=\"target-line\">" + escapeHtml(target) + "</div>"
  )).join("") || "<div class=\"target-line muted\">暂无目标</div>";
  el("queueDetailAction").textContent = actionLabel(item.action);
  el("queueDetailStarted").textContent = item.status === "queued"
    ? "排队中..."
    : formatTimestamp(item.started_at);
  el("queueListView").hidden = true;
  el("queueDetailView").hidden = false;
}

function handleLoadError(err) {
  el("queueStatus").textContent = "加载失败";
  showToast("任务队列加载失败: " + err.message, "bad");
}

export function openQueue() {
  loadQueue().catch(handleLoadError);
}

export function closeQueue() {
  hideReorderModal();
}

export function isReorderModalOpen() {
  return !el("queueModalLayer").hidden;
}

export function initQueue() {
  document.addEventListener("click", event => {
    const reorder = event.target.closest && event.target.closest("[data-queue-reorder]");
    if (reorder) {
      showReorderModal(reorder.getAttribute("data-queue-reorder"));
      return;
    }

    const cancel = event.target.closest && event.target.closest("[data-queue-cancel]");
    if (cancel) {
      cancelQueueTask(cancel.getAttribute("data-queue-cancel"));
      return;
    }

    const detail = event.target.closest && event.target.closest("[data-queue-detail]");
    if (detail) renderQueueDetail(detail.getAttribute("data-queue-detail"));
  });

  el("refreshQueue").addEventListener("click", () => {
    loadQueue().catch(handleLoadError);
  });
  el("queueBack").addEventListener("click", showQueueList);
  el("queueModalClose").addEventListener("click", hideReorderModal);
  el("queueModalConfirm").addEventListener("click", confirmReorder);
  el("queueTargetInput").addEventListener("keydown", event => {
    if (event.key === "Enter") confirmReorder();
  });
}
