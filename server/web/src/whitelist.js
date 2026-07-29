import {browsers, renderSelected} from "./file-browser.js";
import {api, el, escapeHtml} from "./shared.js";

function renderWhitelist(entries) {
  const items = Array.isArray(entries) ? entries : [];
  el("whitelistEntries").innerHTML = items.map(entry => (
    "<div class=\"whitelist-entry\">" +
      "<span class=\"path\">" + escapeHtml(entry.path) + "</span>" +
      "<button class=\"danger\" data-whitelist-delete=\"" + escapeHtml(entry.path) + "\" type=\"button\">删除</button>" +
    "</div>"
  )).join("") || "<div class=\"whitelist-entry empty\"><span class=\"muted\">暂无信任区条目</span><span></span></div>";
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

export function openWhitelist() {
  loadWhitelist().catch(err => alert(err.message));
}

export function initWhitelist() {
  document.addEventListener("click", event => {
    const remove = event.target.closest && event.target.closest("[data-whitelist-delete]");
    if (remove && confirm("删除这条信任区？")) {
      deleteWhitelist(remove.getAttribute("data-whitelist-delete"))
        .catch(err => alert(err.message));
    }
  });

  el("addWhitelist").addEventListener("click", () => {
    addWhitelist().catch(err => alert(err.message));
  });

  loadWhitelist().catch(console.error);
}
