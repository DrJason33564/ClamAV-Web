import {el} from "./shared.js";

export function pollIntervalSeconds() {
  const value = Number(localStorage.getItem("statusPollInterval") || "5");
  return Number.isInteger(value) && value > 0 ? value : 5;
}

function isDebugging() {
  return localStorage.getItem("debugging") === "true";
}

function applyDebugMode() {
  const enabled = isDebugging();
  el("debugMode").checked = enabled;
  el("rawStatusCard").hidden = !enabled;
}

function applyPollIntervalSetting() {
  el("pollInterval").value = String(pollIntervalSeconds());
  el("pollIntervalError").hidden = true;
}

export function initSettings(onPollIntervalChange) {
  el("debugMode").addEventListener("change", event => {
    localStorage.setItem("debugging", event.target.checked ? "true" : "false");
    applyDebugMode();
  });

  el("pollInterval").addEventListener("input", event => {
    const input = event.target;
    const value = input.value.replace(/\D/g, "");
    if (input.value !== value) input.value = value;
    const valid = /^[1-9]\d*$/.test(value);
    el("pollIntervalError").hidden = valid;
    if (!valid) return;
    localStorage.setItem("statusPollInterval", value);
    onPollIntervalChange();
  });

  applyDebugMode();
  applyPollIntervalSetting();
}
