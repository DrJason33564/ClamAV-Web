import {api, el, formatTimestamp, objectOrEmpty, showToast} from "./shared.js";
import {pollIntervalSeconds} from "./settings.js";

let statusPollTimer = 0;

function dashboardState(data) {
  const source = objectOrEmpty(data.source);
  const scan = objectOrEmpty(source.scan);
  if (data.ping !== "ready") {
    return {
      label: "错误",
      mode: "error",
      icon: "/static/icons/error.png"
    };
  }
  if (scan.active_job_id) {
    return {
      label: "正在扫描",
      mode: "running",
      icon: "/static/icons/running.png"
    };
  }
  return {
    label: "预备",
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

export async function loadStatus() {
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

export function startStatusPolling() {
  clearInterval(statusPollTimer);
  statusPollTimer = setInterval(() => {
    loadStatus().catch(console.error);
  }, pollIntervalSeconds() * 1000);
}

export function initStatusPage() {
  el("refreshStatus").addEventListener("click", () => {
    loadStatus()
      .then(() => showToast("页面已刷新", "ok"))
      .catch(err => {
        showToast("页面刷新失败", "bad");
        console.error(err);
      });
  });

  loadStatus().catch(console.error);
  startStatusPolling();
}
