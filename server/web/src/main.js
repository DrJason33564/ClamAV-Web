import "./style.css";

import {el} from "./shared.js";
import {initFileBrowser} from "./file-browser.js";
import {initManualScan} from "./manual-scan.js";
import {initCron, openCron} from "./cron.js";
import {closeQueue, initQueue, isReorderModalOpen, openQueue} from "./queue.js";
import {initWhitelist, openWhitelist} from "./whitelist.js";
import {closeHistory, initHistory, openHistory} from "./history.js";
import {closeQuarantine, initQuarantine, openQuarantine} from "./quarantine.js";
import {initSettings} from "./settings.js";
import {initStatusPage, startStatusPolling} from "./status.js";

function openDrawer(name) {
  document.querySelectorAll("[data-drawer-panel]").forEach(panel => {
    panel.hidden = panel.getAttribute("data-drawer-panel") !== name;
  });

  if (name === "schedule") openCron();
  if (name === "whitelist") openWhitelist();
  if (name === "history") openHistory();
  if (name === "queue") openQueue();
  if (name === "quarantine") openQuarantine();

  el("drawerLayer").classList.toggle("settings-open", name === "settings");
  el("drawerLayer").classList.add("open");
  el("drawerLayer").setAttribute("aria-hidden", "false");
  document.body.classList.add("drawer-open");
}

function closeDrawer() {
  closeHistory();
  closeQuarantine();
  closeQueue();
  el("drawerLayer").classList.remove("open");
  el("drawerLayer").classList.remove("settings-open");
  el("drawerLayer").setAttribute("aria-hidden", "true");
  document.body.classList.remove("drawer-open");
}

function initDrawerNavigation() {
  document.addEventListener("click", event => {
    const drawerOpen = event.target.closest && event.target.closest("[data-drawer-open]");
    if (drawerOpen) openDrawer(drawerOpen.getAttribute("data-drawer-open"));

    const drawerClose = event.target.closest && event.target.closest("[data-drawer-close]");
    if (drawerClose) closeDrawer();
  });

  document.addEventListener("keydown", event => {
    if (event.key === "Escape" && isReorderModalOpen()) {
      closeQueue();
      return;
    }
    if (event.key === "Escape") closeDrawer();
  });
}

initFileBrowser();
initManualScan();
initCron();
initQueue();
initWhitelist();
initHistory();
initQuarantine();
initSettings(startStatusPolling);
initStatusPage();
initDrawerNavigation();
