import {api, el, escapeHtml} from "./shared.js";

export const browsers = {
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

export async function loadBrowse(scope, path) {
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
  rootSelect.innerHTML = roots.map(root => (
    "<option value=\"" + escapeHtml(root) + "\">" + escapeHtml(root) + "</option>"
  )).join("");
  rootSelect.value = roots.find(root => (
    state.currentPath === root || state.currentPath.startsWith(root + "/")
  )) || roots[0] || "";

  el(state.browser).innerHTML = entries.map(entry => {
    const checked = state.multi ? state.selected.has(entry.path) : state.selected === entry.path;
    const inputType = state.multi ? "checkbox" : "radio";
    const inputName = state.multi ? "" : " name=\"" + escapeHtml(scope) + "-target\"";
    const icon = entry.is_dir
      ? "<img class=\"row-icon\" src=\"/static/icons/folder.png\" alt=\"目录\">"
      : "<img class=\"row-icon\" src=\"/static/icons/file.png\" alt=\"文件\">";
    const iconCell = entry.is_dir
      ? "<button class=\"entry-type\" data-browse-open=\"" + escapeHtml(scope) + "\" data-path=\"" + escapeHtml(entry.path) + "\" aria-label=\"进入目录\">" + icon + "</button>"
      : "<span class=\"entry-type\">" + icon + "</span>";
    const label = entry.is_dir
      ? "<button class=\"entry-name\" data-browse-open=\"" + escapeHtml(scope) + "\" data-path=\"" + escapeHtml(entry.path) + "\">" + escapeHtml(entry.name) + "</button>"
      : "<span class=\"entry-name\">" + escapeHtml(entry.name) + "</span>";
    return "<div class=\"entry\">" +
      "<input type=\"" + inputType + "\"" + inputName + " data-browse-select=\"" + escapeHtml(scope) + "\" data-path=\"" + escapeHtml(entry.path) + "\" " + (checked ? "checked" : "") + ">" +
      iconCell +
      "<div>" + label + "<div class=\"path\">" + escapeHtml(entry.path) + "</div></div>" +
    "</div>";
  }).join("") || "<div class=\"entry\"><span></span><span></span><span class=\"muted\">目录为空</span></div>";
}

export function renderSelected(scope) {
  const state = browsers[scope];
  if (!state) return;
  if (state.multi) {
    el(state.selectedLabel).innerHTML = [...state.selected.values()].map(path => (
      "<span class=\"chip\">" + escapeHtml(path) + "</span>"
    )).join("");
    el("scanBtn").disabled = state.selected.size === 0;
    return;
  }

  const targetInput = state.targetInput ? el(state.targetInput) : null;
  const target = state.selected || (targetInput ? targetInput.value.trim() : "");
  el(state.selectedLabel).innerHTML = target
    ? "<span class=\"chip\">" + escapeHtml(target) + "</span>"
    : "";
  if (state.addBtn) el(state.addBtn).disabled = !target;
}

export function initFileBrowser() {
  document.addEventListener("click", event => {
    const open = event.target.closest && event.target.closest("[data-browse-open]");
    if (open) {
      loadBrowse(open.getAttribute("data-browse-open"), open.getAttribute("data-path"))
        .catch(err => alert(err.message));
    }
  });

  document.addEventListener("change", event => {
    const scope = event.target.getAttribute && event.target.getAttribute("data-browse-select");
    const path = event.target.getAttribute && event.target.getAttribute("data-path");
    if (!scope || !path) return;
    const state = browsers[scope];
    if (state && state.multi) {
      if (event.target.checked) state.selected.set(path, path);
      else state.selected.delete(path);
      renderSelected(scope);
    } else if (state && event.target.checked) {
      state.selected = path;
      if (state.targetInput) el(state.targetInput).value = path;
      renderSelected(scope);
    }
  });

  document.querySelectorAll("[data-browse-root]").forEach(control => {
    control.addEventListener("change", event => {
      const scope = event.target.getAttribute("data-browse-root");
      loadBrowse(scope, event.target.value).catch(err => alert(err.message));
    });
  });

  document.querySelectorAll("[data-browse-up]").forEach(control => {
    control.addEventListener("click", event => {
      const scope = event.currentTarget.getAttribute("data-browse-up");
      const state = browsers[scope];
      if (state && state.parentPath) {
        loadBrowse(scope, state.parentPath).catch(err => alert(err.message));
      }
    });
  });

  Object.keys(browsers).forEach(scope => {
    renderSelected(scope);
    loadBrowse(scope, "").catch(console.error);
  });
}
