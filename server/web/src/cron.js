import {browsers, renderSelected} from "./file-browser.js";
import {api, el, escapeHtml, sleep} from "./shared.js";

let editingCronRule = "";

function cronPayload() {
  return {
    enabled: el("cronEnabled").checked,
    minute: el("cronMinute").value.trim(),
    hour: el("cronHour").value.trim(),
    day: el("cronDay").value.trim(),
    month: el("cronMonth").value.trim(),
    weekday: el("cronWeekday").value.trim(),
    target: el("cronTarget").value.trim(),
    action: el("cronAction").value
  };
}

function fillCronForm(rule) {
  editingCronRule = rule ? rule.id : "";
  el("cronMinute").value = rule ? rule.minute : "30";
  el("cronHour").value = rule ? rule.hour : "3";
  el("cronDay").value = rule ? rule.day : "*";
  el("cronMonth").value = rule ? rule.month : "*";
  el("cronWeekday").value = rule ? rule.weekday : "*";
  el("cronTarget").value = rule ? rule.target : "";
  el("cronAction").value = rule ? rule.action : "warn";
  el("cronEnabled").checked = rule ? rule.enabled : true;
  el("saveCronRule").textContent = editingCronRule ? "更新规则" : "保存规则";
  browsers.cron.selected = rule ? rule.target : "";
  renderSelected("cron");
}

async function loadCronRules() {
  const data = await api("/api/cron/rules");
  const rules = data.rules || [];
  el("cronRules").innerHTML = rules.map(rule => {
    const expr = [rule.minute, rule.hour, rule.day, rule.month, rule.weekday].join(" ");
    return "<div class=\"rule\">" +
      "<input type=\"checkbox\" data-cron-enable=\"" + escapeHtml(rule.id) + "\" " + (rule.enabled ? "checked" : "") + ">" +
      "<div><strong>" + escapeHtml(expr) + "</strong><div class=\"path\">" + escapeHtml(rule.target) + " [" + escapeHtml(rule.action) + "]</div></div>" +
      "<small class=\"" + (rule.enabled ? "ok" : "muted") + "\">" + (rule.enabled ? "启用" : "禁用") + "</small>" +
      "<div class=\"rule-actions\"><button data-cron-edit=\"" + escapeHtml(rule.id) + "\">编辑</button><button class=\"danger\" data-cron-delete=\"" + escapeHtml(rule.id) + "\">删除</button></div>" +
    "</div>";
  }).join("") || "<div class=\"rule\"><span></span><span class=\"muted\">暂无定时规则</span><span></span><span></span></div>";
}

async function reloadCronRulesSoon() {
  await sleep(50);
  await loadCronRules();
}

async function saveCronRule() {
  const payload = cronPayload();
  const url = editingCronRule
    ? "/api/cron/rules/" + encodeURIComponent(editingCronRule)
    : "/api/cron/rules";
  const method = editingCronRule ? "PUT" : "POST";
  await api(url, {
    method,
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify(payload)
  });
  fillCronForm(null);
  await reloadCronRulesSoon();
}

async function setCronEnabled(id, enabled) {
  await api("/api/cron/rules/" + encodeURIComponent(id) + "/enabled", {
    method: "PATCH",
    headers: {"Content-Type": "application/json"},
    body: JSON.stringify({enabled})
  });
  await reloadCronRulesSoon();
}

async function deleteCronRule(id) {
  await api("/api/cron/rules/" + encodeURIComponent(id), {method: "DELETE"});
  if (editingCronRule === id) fillCronForm(null);
  await reloadCronRulesSoon();
}

async function editCronRule(id) {
  const data = await api("/api/cron/rules");
  const rule = (data.rules || []).find(item => item.id === id);
  if (rule) fillCronForm(rule);
}

export function openCron() {
  loadCronRules().catch(err => alert(err.message));
}

export function initCron() {
  document.addEventListener("click", event => {
    const edit = event.target.closest && event.target.closest("[data-cron-edit]");
    if (edit) {
      editCronRule(edit.getAttribute("data-cron-edit")).catch(err => alert(err.message));
    }

    const remove = event.target.closest && event.target.closest("[data-cron-delete]");
    if (remove && confirm("删除这条定时规则？")) {
      deleteCronRule(remove.getAttribute("data-cron-delete")).catch(err => alert(err.message));
    }
  });

  document.addEventListener("change", event => {
    const id = event.target.getAttribute && event.target.getAttribute("data-cron-enable");
    if (id) setCronEnabled(id, event.target.checked).catch(err => alert(err.message));
  });

  el("cronTarget").addEventListener("input", event => {
    browsers.cron.selected = event.target.value.trim();
    renderSelected("cron");
  });
  el("saveCronRule").addEventListener("click", () => {
    saveCronRule().catch(err => alert(err.message));
  });
  el("newCronRule").addEventListener("click", () => fillCronForm(null));
  el("reloadCron").addEventListener("click", () => {
    api("/api/cron/reload", {method: "POST"})
      .then(loadCronRules)
      .catch(err => alert(err.message));
  });

  fillCronForm(null);
  loadCronRules().catch(console.error);
}
