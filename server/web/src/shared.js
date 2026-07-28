export const el = id => document.getElementById(id);

export async function api(url, options) {
  const res = await fetch(url, options);
  if (!res.ok) {
    const body = await res.json().catch(() => ({error: res.statusText}));
    throw new Error(body.error || body.message || res.statusText);
  }
  return res.json();
}

export function objectOrEmpty(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? value : {};
}

export function formatTimestamp(value) {
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

export function formatCompactDate(value) {
  const text = String(value || "");
  if (/^\d{14}$/.test(text)) {
    return text.slice(0, 4) + "-" + text.slice(4, 6) + "-" + text.slice(6, 8) + " " +
      text.slice(8, 10) + ":" + text.slice(10, 12) + ":" + text.slice(12, 14);
  }
  return formatTimestamp(text);
}

export function actionLabel(value) {
  if (value === "move") return "移动到隔离区";
  if (value === "remove") return "直接删除";
  return "仅告警";
}

export function scanIDChip(value) {
  const id = String(value || "");
  const body = id.startsWith("web-") ? id.slice(4) : id;
  return body.slice(0, 5) || "未知";
}

export function escapeHtml(value) {
  return String(value).replace(/[&<>"']/g, ch => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    "\"": "&quot;",
    "'": "&#39;"
  })[ch]);
}

export function showToast(message, type) {
  const toast = el("toast");
  toast.textContent = message;
  toast.className = "toast show " + (type || "");
  clearTimeout(showToast.timer);
  showToast.timer = setTimeout(() => {
    toast.className = "toast";
  }, 2400);
}

export function sleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms));
}
