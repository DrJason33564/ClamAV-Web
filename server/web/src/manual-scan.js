import {browsers, loadBrowse, renderSelected} from "./file-browser.js";
import {api, el, sleep} from "./shared.js";
import {loadStatus} from "./status.js";

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

export function initManualScan() {
  el("scanBtn").addEventListener("click", () => {
    startScan().catch(err => alert(err.message));
  });
}
