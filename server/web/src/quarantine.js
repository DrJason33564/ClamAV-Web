import {api, el, escapeHtml, showToast} from "./shared.js";

let quarantinePollTimer = 0;

function renderQuarantineList(subjects) {
  const items = Array.isArray(subjects) ? subjects : [];
  el("quarantineList").innerHTML = items.map(item => (
    "<div class=\"quarantine-row\">" +
      "<img class=\"row-icon\" src=\"/static/icons/file.png\" alt=\"\">" +
      "<div class=\"quarantine-file\"><strong>" + escapeHtml(item.name || "") + "</strong><small>" + escapeHtml(item.source_file || "") + "</small></div>" +
      "<button class=\"secondary\" data-quarantine-recover=\"" + escapeHtml(item.name || "") + "\" type=\"button\">恢复</button>" +
      "<button class=\"danger\" data-quarantine-delete=\"" + escapeHtml(item.name || "") + "\" type=\"button\">删除</button>" +
    "</div>"
  )).join("") || "<div class=\"history-empty muted\">隔离区为空</div>";
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
  quarantinePollTimer = setTimeout(() => {
    pollQuarantineLookup(lookupID).catch(handleLoadError);
  }, 1000);
}

async function recoverQuarantineFile(filename) {
  if (!confirm("是否恢复\"" + filename + "\"？")) return;
  try {
    const data = await api(
      "/api/quarantine/recover/" + encodeURIComponent(filename),
      {method: "POST"}
    );
    if (data && data.status && data.status !== "success") {
      throw new Error(data.error || "recover failed");
    }
  } catch (err) {
    showToast("文件恢复失败：" + err.message, "bad");
    return;
  }
  showToast("文件恢复成功", "ok");
  startQuarantineLookup().catch(handleLoadError);
}

async function deleteQuarantineFile(filename) {
  if (!confirm("是否永久删除\"" + filename + "\"？")) return;
  try {
    const data = await api(
      "/api/quarantine/delete/" + encodeURIComponent(filename),
      {method: "POST"}
    );
    if (data && data.status && data.status !== "success") {
      throw new Error(data.error || "delete failed");
    }
  } catch (err) {
    showToast("文件删除失败：" + err.message, "bad");
    return;
  }
  showToast("文件删除成功", "ok");
  startQuarantineLookup().catch(handleLoadError);
}

async function cleanQuarantine() {
  if (!confirm("是否清理隔离区所有文件？")) return;
  try {
    const data = await api("/api/quarantine/clean?clean_all=Y", {method: "POST"});
    if (data && data.status && data.status !== "success") {
      throw new Error(data.error || "clean failed");
    }
  } catch (err) {
    showToast("隔离区清理失败：" + err.message, "bad");
    return;
  }
  showToast("隔离区清理成功", "ok");
  startQuarantineLookup().catch(handleLoadError);
}

function handleLoadError(err) {
  el("quarantineStatus").textContent = "加载失败";
  showToast("隔离区加载失败：" + err.message, "bad");
}

export function openQuarantine() {
  startQuarantineLookup().catch(handleLoadError);
}

export function closeQuarantine() {
  clearTimeout(quarantinePollTimer);
}

export function initQuarantine() {
  document.addEventListener("click", event => {
    const recover = event.target.closest && event.target.closest("[data-quarantine-recover]");
    if (recover) recoverQuarantineFile(recover.getAttribute("data-quarantine-recover"));

    const remove = event.target.closest && event.target.closest("[data-quarantine-delete]");
    if (remove) deleteQuarantineFile(remove.getAttribute("data-quarantine-delete"));
  });

  el("refreshQuarantine").addEventListener("click", () => {
    startQuarantineLookup().catch(handleLoadError);
  });
  el("cleanQuarantine").addEventListener("click", cleanQuarantine);
}
