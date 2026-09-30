const { api, shortID, setMessage, withBusy } = window.LockFlow;
const form = document.querySelector("#plan-form");
const message = document.querySelector("#plan-message");
const editorBody = document.querySelector("#convoy-editor tbody");

let vessels = [
  { name: "River Star", call_sign: "RS-204", length_m: 86 },
  { name: "River Moon", call_sign: "RM-117", length_m: 92 },
];

const STATE_LABEL = { standby: "待命", allowed: "允许", entered: "已进入", withdrawn: "撤回" };
const esc = (value) => String(value ?? "").replace(/[&<>"]/g, (ch) =>
  ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[ch]));

function readEditor() {
  vessels = [...editorBody.querySelectorAll("tr")].map((row) => ({
    name: row.querySelector("[data-field=name]").value.trim(),
    call_sign: row.querySelector("[data-field=call_sign]").value.trim(),
    length_m: Number(row.querySelector("[data-field=length]").value),
  })).filter((v) => v.name && v.call_sign && v.length_m > 0);
}

function renderEditor() {
  editorBody.innerHTML = vessels.map((v, index) => `<tr>
    <td>${index + 1}</td>
    <td><input data-field="name" value="${esc(v.name)}"></td>
    <td><input data-field="call_sign" value="${esc(v.call_sign)}"></td>
    <td><input data-field="length" type="number" min="1" value="${v.length_m}"></td>
    <td><button type="button" class="danger" data-remove="${index}">移除</button></td>
  </tr>`).join("");
}

editorBody.addEventListener("input", readEditor);
editorBody.addEventListener("click", (event) => {
  const index = event.target.dataset.remove;
  if (index === undefined) return;
  readEditor();
  vessels.splice(Number(index), 1);
  renderEditor();
});
document.querySelector("#add-vessel").addEventListener("click", () => {
  readEditor();
  vessels.push({ name: "", call_sign: "", length_m: 60 });
  renderEditor();
});
renderEditor();

function vesselRow(planID, entry, activeHeadID, index) {
  const state = entry.state;
  const isHead = entry.vessel.id === activeHeadID;
  const badge = `<span class="badge ${state}">${STATE_LABEL[state] || state}</span>`;
  let actions = "";
  if (state === "standby" && isHead) {
    actions = `<button data-action="allow" data-plan="${planID}" data-vessel="${entry.vessel.id}">允许进入</button>`;
  } else if (state === "allowed") {
    actions = `<button data-action="entered" data-plan="${planID}" data-vessel="${entry.vessel.id}">确认进入</button>`;
  } else if (state === "withdrawn" && isHead) {
    actions = `<button data-action="retry" data-plan="${planID}" data-vessel="${entry.vessel.id}">重新待命</button>`;
  } else if (state === "standby") {
    actions = `<span class="muted">等待前船</span>`;
  }
  if (state === "standby" || state === "allowed") {
    actions += ` <button class="danger" data-action="withdraw" data-plan="${planID}" data-vessel="${entry.vessel.id}">撤回</button>`;
  }
  const note = entry.reason ? `<div class="muted">${esc(entry.reason)}</div>` : "";
  return `<tr>
    <td>${index + 1}</td><td>${esc(entry.vessel.name)}</td><td>${esc(entry.vessel.call_sign)}</td>
    <td>${badge}</td><td class="actions">${actions}${note}</td>
  </tr>`;
}

function renderQueue(chambers, plansByChamber, pendingByChamber) {
  const host = document.querySelector("#plan-queue");
  const chambersWithWork = chambers.filter((c) => (plansByChamber[c.chamber_id] || []).length);
  if (chambersWithWork.length === 0) {
    host.innerHTML = '<div class="muted">No active plans</div>';
    return;
  }
  host.innerHTML = chambersWithWork.map((chamber) => {
    const items = plansByChamber[chamber.chamber_id] || [];
    const pending = pendingByChamber[chamber.chamber_id] || [];
    const direction = chamber.cycle?.direction || "";
    const stage = chamber.cycle?.stage || "idle";
    const gateControls = renderGateControls(chamber, pending);
    return items.map((item) => {
      const plan = item.plan;
      const progress = item.admission;
      const entries = progress.entries || [];
      const activeHead = entries.find((e) => e.state !== "entered");
      const activeHeadID = activeHead ? activeHead.vessel.id : null;
      const seal = item.ready_to_seal
        ? '<span class="badge entered">可封闸</span>'
        : `<span class="badge standby">封闸受阻: ${esc(item.seal_reason || "")}</span>`;
      return `<section class="plan-card">
        <div class="plan-head">
          <div><strong>${esc(plan.chamber_id)}</strong> · 计划 ${shortID(plan.id)} · ${esc(plan.direction)}
            <span class="muted">(${entries.length} 艘: ${progress.entered || 0} 已进入 / ${progress.allowed || 0} 允许 / ${progress.standby || 0} 待命 / ${progress.withdrawn || 0} 撤回)</span></div>
          <div>阶段 <strong>${stage}</strong> · 入口闸 ${esc(direction ? chamber.gates[opposite(direction)] : "-")} ${seal}</div>
        </div>
        ${gateControls}
        <table><thead><tr><th>#</th><th>船名</th><th>呼号</th><th>状态</th><th>操作</th></tr></thead>
        <tbody>${entries.map((entry, index) => vesselRow(plan.id, entry, activeHeadID, index)).join("")}</tbody></table>
      </section>`;
    }).join("");
  }).join("");
}

function opposite(direction) { return direction === "upstream" ? "downstream" : "upstream"; }

function renderGateControls(chamber, pending) {
  const cycle = chamber.cycle;
  if (!cycle) return "";
  const entryDirection = opposite(cycle.direction);
  const entryPosition = chamber.gates[entryDirection];
  const buttons = [];
  if (pending.length > 0) {
    const command = pending[0];
    buttons.push(`<button data-action="ack" data-chamber="${chamber.chamber_id}" data-command="${command.id}">确认闸 ${esc(command.device)} ${esc(command.action)}</button>`);
  } else if (cycle.stage === "planned" && entryPosition === "closed") {
    buttons.push(`<button data-action="open-gate" data-chamber="${chamber.chamber_id}" data-direction="${entryDirection}">开启入口闸</button>`);
  } else if (cycle.stage === "admitting" && entryPosition === "open") {
    buttons.push(`<button data-action="close-gate" data-chamber="${chamber.chamber_id}" data-direction="${entryDirection}">关闭入口闸(封闸)</button>`);
  }
  return buttons.length ? `<div class="gate-controls">${buttons.join("")}</div>` : "";
}

async function act(event) {
  const button = event.target.closest("button[data-action]");
  if (!button) return;
  const { action, plan, vessel, chamber, direction, command } = button.dataset;
  await withBusy(button, async () => {
    try {
      if (action === "allow") await api(`/api/plans/${plan}/vessels/${vessel}/allow`, { method: "POST", body: "{}" });
      else if (action === "entered") await api(`/api/plans/${plan}/vessels/${vessel}/entered`, { method: "POST", body: "{}" });
      else if (action === "retry") await api(`/api/plans/${plan}/vessels/${vessel}/retry`, { method: "POST", body: "{}" });
      else if (action === "withdraw") {
        const reason = prompt("撤回原因(故障/失联)?", "vessel lost contact") || "";
        await api(`/api/plans/${plan}/vessels/${vessel}/withdraw`, { method: "POST", body: JSON.stringify({ reason }) });
      } else if (action === "open-gate") await api(`/api/chambers/${chamber}/gates/${direction}/open`, { method: "POST", body: "{}" });
      else if (action === "close-gate") await api(`/api/chambers/${chamber}/gates/${direction}/close`, { method: "POST", body: "{}" });
      else if (action === "ack") await api(`/api/chambers/${chamber}/commands/${command}/ack`, { method: "POST", body: "{}" });
      await refresh();
    } catch (error) { setMessage(message, error.message); }
  });
}
document.querySelector("#plan-queue").addEventListener("click", act);

async function refresh() {
  const [plans, chambers, interlocks, health] = await Promise.all([
    api("/api/plans"), api("/api/chambers"), api("/api/interlocks"), api("/healthz"),
  ]);
  document.querySelector("#health").textContent = health.status === "ok" ? "Devices online" : "Attention required";
  const pendingByChamber = {};
  for (const [chamberID, item] of Object.entries(interlocks)) {
    if (chamberID === "signal_handoffs" || !item.pending_commands) continue;
    pendingByChamber[chamberID] = item.pending_commands;
  }
  renderQueue(chambers, plans, pendingByChamber);
  const current = chambers.find((item) => item.cycle);
  document.querySelector("#metric-chamber").textContent = current?.chamber_id || "Idle";
  document.querySelector("#metric-stage").textContent = current?.cycle?.stage || "-";
  document.querySelector("#metric-cycle").textContent = shortID(current?.cycle?.id);
}

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  readEditor();
  if (vessels.length === 0) { setMessage(message, "Add at least one vessel."); return; }
  const button = form.querySelector("button[type=submit]");
  await withBusy(button, async () => {
    try {
      const result = await api("/api/plans", { method: "POST", body: JSON.stringify({
        chamber_id: document.querySelector("#chamber").value,
        direction: document.querySelector("#direction").value,
        vessels: vessels,
      }) });
      setMessage(message, `计划 ${shortID(result.plan.id)} 已排队 ${result.plan.vessels.length} 艘船于 ${result.plan.chamber_id},等待逐船放行。`);
      vessels = [{ name: "River Star", call_sign: "RS-204", length_m: 86 }];
      renderEditor();
      await refresh();
    } catch (error) { setMessage(message, error.message); }
  });
});

refresh().catch((error) => setMessage(message, error.message));
setInterval(() => refresh().catch(() => {}), 5000);
