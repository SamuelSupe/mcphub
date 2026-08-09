import { apiErrorMessage, getLocale, setLocale, t, translateDOM } from "./i18n.js";

const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];

const state = {
  backends: [],
  groups: [],
  events: [],
  groupEditing: null,
  toolEditing: null,
  importPreview: null,
  importDocument: "",
  editing: null,
  authMode: "headers",
  probeOK: false,
  probeAttempted: false,
  busy: false,
  opener: null,
};

const elements = {
  list: $("#backend-list"),
  empty: $("#empty-state"),
  events: $("#event-list"),
  inspector: $("#inspector"),
  scrim: $("#scrim"),
  form: $("#backend-form"),
  probeResult: $("#probe-result"),
  formError: $("#form-error"),
  save: $("#save-button"),
  probe: $("#probe-button"),
  remove: $("#delete-button"),
  copy: $("#copy-button"),
  live: $("#live-region"),
  groupList: $("#tool-group-list"),
  groupEmpty: $("#tool-group-empty"),
};

async function api(path, options = {}) {
  const response = await fetch(`/api/v1${path}`, {
    ...options,
    headers: {
      ...(options.body ? { "Content-Type": "application/json" } : {}),
      ...(options.headers || {}),
    },
  });
  if (response.status === 204) return null;
  const body = await response.json().catch(() => ({}));
  if (!response.ok) {
    const error = new Error(apiErrorMessage(body.error, "请求失败"));
    error.code = body.error?.code;
    error.field = body.error?.field;
    error.status = response.status;
    throw error;
  }
  return body;
}

async function refresh() {
  try {
    const [overview, backendData, groupData, eventData] = await Promise.all([
      api("/overview"), api("/backends"), api("/tool-groups"), api("/events?limit=12"),
    ]);
    state.backends = backendData.backends || [];
    state.groups = groupData.tool_groups || [];
    state.events = eventData.events || [];
    $("#metric-total").textContent = overview.total;
    $("#metric-ready").textContent = overview.ready;
    $("#metric-required").textContent = overview.required;
    $("#metric-optional").textContent = overview.optional;
    $("#metric-unavailable").textContent = overview.unavailable;
    renderBackends();
    renderToolGroups();
    renderEvents(state.events);
  } catch (error) {
    announce(`${t("刷新失败")}：${error.message}`);
  }
}

function renderBackends() {
  const query = $("#search").value.trim().toLowerCase();
  const filtered = state.backends.filter((item) => `${item.id} ${item.url}`.toLowerCase().includes(query));
  elements.list.replaceChildren();
  elements.empty.hidden = state.backends.length !== 0;
  for (const backend of filtered) {
    const row = document.createElement("article");
    row.className = "backend-row";
    row.tabIndex = 0;
    row.setAttribute("role", "button");
    row.setAttribute("aria-label", `${t("编辑")} ${backend.id}`);

    const identity = document.createElement("div");
    identity.className = "backend-identity";
    const icon = document.createElement("div");
    icon.className = "backend-icon";
    icon.textContent = backend.id.slice(0, 2).toUpperCase();
    const labels = document.createElement("div");
    const title = document.createElement("strong");
    title.textContent = backend.id;
    const url = document.createElement("small");
    url.textContent = backend.url;
    labels.append(title, url);
    identity.append(icon, labels);

    const capabilities = document.createElement("div");
    capabilities.className = "backend-meta";
    const capTitle = document.createElement("strong");
    capTitle.textContent = `${backend.runtime.tools} tools · ${backend.runtime.resources} resources`;
    const capLabel = document.createElement("small");
    capLabel.textContent = backend.oauth ? "OAuth client_credentials" : backend.headers?.length ? t("静态 Headers") : t("无需后端认证");
    capabilities.append(capTitle, capLabel);

    const policy = document.createElement("div");
    policy.className = "backend-meta";
    const policyTitle = document.createElement("strong");
    policyTitle.textContent = backend.required ? "Required" : "Optional";
    const policyLabel = document.createElement("small");
    policyLabel.textContent = backend.required_scopes?.length ? `${backend.required_scopes.length} ${getLocale() === "en" ? "scopes" : "个 Scope"}` : t("无 Scope 限制");
    policy.append(policyTitle, policyLabel);

    const pill = document.createElement("span");
    pill.className = "state-pill";
    const dot = document.createElement("i");
    dot.className = `status-dot ${backend.runtime.state}`;
    pill.append(dot, document.createTextNode(stateLabel(backend.runtime.state)));
    const chevron = document.createElement("span");
    chevron.className = "chevron";
    chevron.textContent = "›";
    const toggle = document.createElement("button");
    toggle.type = "button";
    toggle.className = "row-toggle pressable";
    toggle.textContent = backend.enabled ? t("停用") : t("启用");
    toggle.setAttribute("aria-label", `${backend.enabled ? t("停用") : t("启用")} ${backend.id}`);
    toggle.addEventListener("click", (event) => {
      event.stopPropagation();
      toggleBackend(backend, toggle);
    });
    const actions = document.createElement("div");
    actions.className = "row-actions";
    actions.append(pill, toggle, chevron);
    row.append(identity, capabilities, policy, actions);
    row.addEventListener("click", () => openInspector(backend));
    row.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        openInspector(backend);
      }
    });
    elements.list.append(row);
  }
  translateDOM(elements.list);
}

function renderEvents(events) {
  elements.events.replaceChildren();
  if (!events.length) {
    const item = document.createElement("li");
    item.textContent = t("还没有配置变更");
    elements.events.append(item);
    return;
  }
  for (const event of events) {
    const item = document.createElement("li");
    const dot = document.createElement("i");
    dot.className = `status-dot ${event.success ? "ready" : "unavailable"}`;
    const copy = document.createElement("span");
    copy.textContent = eventCopy(event);
    const time = document.createElement("time");
    time.dateTime = event.created_at;
    time.textContent = relativeTime(event.created_at);
    item.append(dot, copy, time);
    elements.events.append(item);
  }
  translateDOM(elements.events);
}

function stateLabel(value) {
  return t({ ready: "运行中", unavailable: "重连中", disabled: "已停用" }[value] || value);
}

function eventCopy(event) {
  const actions = { bootstrap: "已从 YAML 导入", create: "已注册", update: "已更新", delete: "已删除" };
  return `${event.source_id || event.backend_id || t("系统")} · ${t(actions[event.action] || event.action)}${event.success ? "" : t("失败")}`;
}

function relativeTime(value) {
  const seconds = Math.round((new Date(value).getTime() - Date.now()) / 1000);
  const formatter = new Intl.RelativeTimeFormat(getLocale(), { numeric: "auto" });
  if (Math.abs(seconds) < 60) return formatter.format(seconds, "second");
  const minutes = Math.round(seconds / 60);
  if (Math.abs(minutes) < 60) return formatter.format(minutes, "minute");
  const hours = Math.round(minutes / 60);
  if (Math.abs(hours) < 24) return formatter.format(hours, "hour");
  return formatter.format(Math.round(hours / 24), "day");
}

function openInspector(backend = null) {
  state.opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  state.editing = backend;
  state.probeOK = false;
  state.probeAttempted = false;
  clearFeedback();
  resetForm();
  if (backend) fillForm(backend);
  $("#field-id").disabled = Boolean(backend);
  $("#inspector-kicker").textContent = backend ? t("编辑配置") : t("新建配置");
  $("#inspector-title").textContent = backend ? backend.id : t("注册 MCP 后端");
  $("#revision-label").textContent = backend ? `Revision ${backend.revision}` : "";
  elements.remove.hidden = !backend;
  elements.copy.hidden = !backend;
  elements.save.textContent = backend ? t("保存并应用") : t("注册并启用");
  elements.inspector.hidden = false;
  elements.scrim.hidden = false;
  $(".app-shell").inert = true;
  document.body.style.overflow = "hidden";
  requestAnimationFrame(() => {
    setSheetPosition(sheetDimension());
    elements.scrim.style.opacity = "0";
    animateSheet(0, 0, () => $("#field-id").focus());
  });
}

function copyCurrent() {
  if (!state.editing) return;
  state.editing = null;
  state.probeOK = false;
  state.probeAttempted = false;
  $("#field-id").disabled = false;
  $("#field-id").value = "";
  $("#inspector-kicker").textContent = t("复制配置");
  $("#inspector-title").textContent = t("注册副本");
  $("#revision-label").textContent = t("Secret 需重新填写");
  elements.remove.hidden = true;
  elements.copy.hidden = true;
  elements.save.textContent = t("注册并启用");
  for (const row of $$(".header-row", $("#headers-list"))) {
    row.dataset.configured = "false";
    const value = $(".header-value", row);
    value.required = true;
    value.placeholder = t("请重新输入 Header 值");
  }
  if (state.authMode === "oauth" || state.authMode === "both") {
    $("#oauth-secret").required = true;
    $("#oauth-secret").placeholder = t("请重新输入 Client Secret");
  }
  clearFeedback();
  $("#field-id").focus();
}

function closeInspector() {
  animateSheet(sheetDimension(), motion.velocity, () => {
    elements.inspector.hidden = true;
    elements.scrim.hidden = true;
    $(".app-shell").inert = false;
    document.body.style.overflow = "";
    const target = state.opener?.isConnected ? state.opener : $("#register-button");
    target.focus();
    state.opener = null;
  });
}

function resetForm() {
  elements.form.reset();
  $("#oauth-secret").required = false;
  $("#field-timeout").value = "60s";
  $("#field-enabled").checked = true;
  $("#headers-list").replaceChildren();
  addHeaderRow("X-API-Key", "", false);
  $("#field-rules").value = "";
  setAuthMode("headers");
}

function fillForm(backend) {
  $("#field-id").value = backend.id;
  $("#field-url").value = backend.url;
  $("#field-timeout").value = backend.request_timeout || "60s";
  $("#field-enabled").checked = backend.enabled;
  $("#field-required").checked = backend.required;
  $("#field-insecure").checked = backend.allow_insecure_http;
  $("#field-scopes").value = (backend.required_scopes || []).join(", ");
  $("#field-rules").value = backend.tool_rules?.length ? JSON.stringify(backend.tool_rules, null, 2) : "";
  $("#headers-list").replaceChildren();
  for (const header of backend.headers || []) addHeaderRow(header.name, "", true);
	if (backend.oauth) {
		setAuthMode(backend.headers?.length ? "both" : "oauth");
    $("#oauth-issuer").value = backend.oauth.issuer;
    $("#oauth-client-id").value = backend.oauth.client_id;
		$("#oauth-scopes").value = (backend.oauth.scopes || []).join(", ");
    $("#oauth-secret").placeholder = backend.oauth.client_secret_configured ? t("已安全保存，留空保持不变") : t("请输入 Client Secret");
	} else if (backend.headers?.length) {
    setAuthMode("headers");
  } else {
    setAuthMode("none");
  }
}

function setAuthMode(mode) {
  state.authMode = mode;
  for (const button of $$(".segmented button")) button.classList.toggle("active", button.dataset.auth === mode);
  $("#headers-panel").hidden = mode !== "headers" && mode !== "both";
  $("#oauth-panel").hidden = mode !== "oauth" && mode !== "both";
}

function addHeaderRow(name = "", value = "", configured = false) {
  const row = document.createElement("div");
  row.className = "header-row";
  row.dataset.configured = configured ? "true" : "false";
  const nameInput = document.createElement("input");
  nameInput.className = "header-name";
  nameInput.value = name;
  nameInput.placeholder = t("Header 名称");
  nameInput.setAttribute("aria-label", t("Header 名称"));
  const valueInput = document.createElement("input");
  valueInput.className = "header-value";
  valueInput.type = "password";
  valueInput.value = value;
  valueInput.placeholder = configured ? t("已安全保存，留空保持") : t("Header 值");
  valueInput.autocomplete = "new-password";
  valueInput.setAttribute("aria-label", `${name || "Header"} ${getLocale() === "en" ? "value" : "值"}`);
  const remove = document.createElement("button");
  remove.type = "button";
  remove.className = "icon-button pressable";
  remove.setAttribute("aria-label", `${t("删除")} ${name || "Header"}`);
  remove.textContent = "−";
  remove.addEventListener("click", () => row.remove());
  row.append(nameInput, valueInput, remove);
  $("#headers-list").append(row);
}

function collectInput() {
  let rules = [];
  const rawRules = $("#field-rules").value.trim();
  if (rawRules) {
    rules = JSON.parse(rawRules);
    if (!Array.isArray(rules)) throw new Error(t("Tool Rules 必须是 JSON 数组"));
  }
  const input = {
    id: $("#field-id").value.trim(),
    url: $("#field-url").value.trim(),
    enabled: $("#field-enabled").checked,
    required: $("#field-required").checked,
    required_scopes: splitValues($("#field-scopes").value),
    tool_rules: rules,
    request_timeout: $("#field-timeout").value.trim(),
    allow_insecure_http: $("#field-insecure").checked,
    headers: [],
  };
  if (state.authMode === "headers" || state.authMode === "both") {
    input.headers = $$(".header-row", $("#headers-list")).filter((row) => $(".header-name", row).value.trim()).map((row) => {
      const value = $(".header-value", row).value;
      const header = { name: $(".header-name", row).value.trim() };
      if (value !== "") header.value = value;
      return header;
    });
  }
  if (state.authMode === "oauth" || state.authMode === "both") {
    input.oauth = {
      type: "client_credentials",
      issuer: $("#oauth-issuer").value.trim(),
      client_id: $("#oauth-client-id").value.trim(),
      scopes: splitValues($("#oauth-scopes").value),
    };
    const secret = $("#oauth-secret").value;
    if (secret !== "") input.oauth.client_secret = secret;
  }
  return input;
}

function inputFromBackend(backend, enabled) {
  const input = {
    id: backend.id,
    url: backend.url,
    enabled,
    required: backend.required,
    required_scopes: backend.required_scopes,
    tool_rules: backend.tool_rules,
    request_timeout: backend.request_timeout,
    allow_insecure_http: backend.allow_insecure_http,
    headers: backend.headers.map((header) => ({ name: header.name })),
  };
  if (backend.oauth) {
    input.oauth = {
      type: backend.oauth.type,
      issuer: backend.oauth.issuer,
      client_id: backend.oauth.client_id,
      scopes: backend.oauth.scopes,
    };
  }
  return input;
}

async function toggleBackend(backend, button) {
  button.disabled = true;
  const enabled = !backend.enabled;
  try {
    await api(`/backends/${encodeURIComponent(backend.id)}`, {
      method: "PUT",
      headers: { "If-Match": `"${backend.revision}"` },
      body: JSON.stringify(inputFromBackend(backend, enabled)),
    });
    announce(`${backend.id} ${enabled ? t("已启用") : t("已停用")}`);
    await refresh();
  } catch (error) {
    announce(`${backend.id} ${enabled ? t("启用") : t("停用")} ${t("失败")}：${error.message}`);
    button.disabled = false;
  }
}

function splitValues(raw) {
  return [...new Set(raw.split(/[\s,]+/).map((value) => value.trim()).filter(Boolean))];
}

async function probeForm() {
  clearFeedback();
  if (!elements.form.reportValidity()) return;
  let input;
  try { input = collectInput(); } catch (error) { showError(error.message); return; }
  setBusy(true, "probe");
  state.probeAttempted = true;
  elements.probeResult.hidden = false;
  elements.probeResult.classList.remove("error");
  elements.probeResult.textContent = t("正在连接并发现 MCP 能力…");
  try {
    const response = await api("/backends/probe", { method: "POST", body: JSON.stringify(input) });
    state.probeOK = true;
    const result = response.result;
    elements.probeResult.textContent = `${t("连接成功")} · ${result.latency_ms} ms · ${result.server.title || result.server.name || "MCP Server"} · ${result.counts.tools} tools · ${result.counts.resources} resources`;
  } catch (error) {
    state.probeOK = false;
    elements.probeResult.hidden = false;
    elements.probeResult.classList.add("error");
    elements.probeResult.textContent = `${error.message} ${$("#field-required").checked ? t("Required 后端必须连接成功后才能保存。") : t("Optional 后端仍可保存并在后台重连。")}`;
  } finally {
    setBusy(false);
  }
}

async function saveForm(event) {
  event.preventDefault();
  clearFeedback();
  if (!elements.form.reportValidity()) return;
  if (!state.editing && !state.probeAttempted) {
    showError(t("请先测试连接，确认后端身份和能力后再注册。"));
    return;
  }
  if (!state.probeOK && $("#field-required").checked) {
    showError(t("Required 后端必须通过连接测试后才能保存。"));
    return;
  }
  let input;
  try { input = collectInput(); } catch (error) { showError(error.message); return; }
  setBusy(true, "save");
  try {
    const editing = state.editing;
    const path = editing ? `/backends/${encodeURIComponent(editing.id)}` : "/backends";
    await api(path, {
      method: editing ? "PUT" : "POST",
      headers: editing ? { "If-Match": `"${editing.revision}"` } : {},
      body: JSON.stringify(input),
    });
    announce(editing ? `${editing.id} ${t("已更新")}` : `${input.id} ${t("已注册")}`);
    closeInspector();
    await refresh();
  } catch (error) {
    showError(error.message);
    if (error.code === "revision_conflict") announce(t("检测到配置冲突，请刷新后重试"));
  } finally {
    setBusy(false);
  }
}

async function deleteCurrent() {
  const backend = state.editing;
  if (!backend) return;
  setBusy(true, "delete");
  try {
    await api(`/backends/${encodeURIComponent(backend.id)}`, {
      method: "DELETE", headers: { "If-Match": `"${backend.revision}"` },
    });
    $("#confirm-dialog").close();
    announce(`${backend.id} ${t("已删除")}`);
    closeInspector();
    await refresh();
  } catch (error) {
    $("#confirm-dialog").close();
    showError(error.message);
  } finally {
    setBusy(false);
  }
}

function clearFeedback() {
  elements.formError.hidden = true;
  elements.probeResult.hidden = true;
  elements.probeResult.classList.remove("error");
}

function showError(message) {
  elements.formError.textContent = message;
  elements.formError.hidden = false;
  elements.formError.scrollIntoView({ behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth", block: "nearest" });
}

function setBusy(value, operation = "") {
  state.busy = value;
  elements.save.disabled = value;
  elements.probe.disabled = value;
  elements.remove.disabled = value;
  if (value && operation === "save") elements.save.textContent = t("正在应用…");
  else if (value && operation === "probe") elements.probe.textContent = t("正在测试…");
  else {
    elements.save.textContent = state.editing ? t("保存并应用") : t("注册并启用");
    elements.probe.textContent = t("测试连接");
  }
}

function announce(message) {
  elements.live.textContent = "";
  requestAnimationFrame(() => { elements.live.textContent = message; });
}

const motion = { value: 0, target: 0, velocity: 0, frame: 0, last: 0, completion: null };

function isMobileSheet() { return matchMedia("(max-width: 700px)").matches; }
function sheetDimension() { return isMobileSheet() ? elements.inspector.offsetHeight : elements.inspector.offsetWidth; }

function setSheetPosition(value) {
  motion.value = value;
  elements.inspector.style.transform = isMobileSheet() ? `translate3d(0, ${value}px, 0)` : `translate3d(${value}px, 0, 0)`;
  const progress = Math.max(0, Math.min(1, 1 - value / Math.max(1, sheetDimension())));
  elements.scrim.style.opacity = String(progress);
}

function animateSheet(target, velocity = 0, completion = null) {
  if (matchMedia("(prefers-reduced-motion: reduce)").matches) {
    setSheetPosition(target);
    if (completion) completion();
    return;
  }
  cancelAnimationFrame(motion.frame);
  motion.target = target;
  motion.velocity = velocity;
  motion.last = performance.now();
  motion.completion = completion;
  const stiffness = 210;
  const damping = target === 0 && Math.abs(velocity) > 250 ? 24 : 29;
  const step = (now) => {
    const dt = Math.min((now - motion.last) / 1000, .032);
    motion.last = now;
    const acceleration = stiffness * (motion.target - motion.value) - damping * motion.velocity;
    motion.velocity += acceleration * dt;
    setSheetPosition(motion.value + motion.velocity * dt);
    if (Math.abs(motion.target - motion.value) < .45 && Math.abs(motion.velocity) < 4) {
      setSheetPosition(motion.target);
      const done = motion.completion;
      motion.completion = null;
      if (done) done();
      return;
    }
    motion.frame = requestAnimationFrame(step);
  };
  motion.frame = requestAnimationFrame(step);
}

const drag = { active: false, pointer: 0, startY: 0, startValue: 0, history: [] };

function rubberband(overshoot, dimension, constant = .55) {
  return (overshoot * dimension * constant) / (dimension + constant * Math.abs(overshoot));
}

function beginDrag(event) {
  if (!isMobileSheet()) return;
  cancelAnimationFrame(motion.frame);
  drag.active = true;
  drag.pointer = event.pointerId;
  drag.startY = event.clientY;
  drag.startValue = motion.value;
  drag.history = [{ y: event.clientY, time: performance.now() }];
  event.currentTarget.setPointerCapture(event.pointerId);
}

function moveDrag(event) {
  if (!drag.active || event.pointerId !== drag.pointer) return;
  const delta = event.clientY - drag.startY;
  let value = drag.startValue + delta;
  const dimension = elements.inspector.offsetHeight;
  if (value < 0) value = rubberband(value, dimension);
  else if (value > dimension) value = dimension + rubberband(value - dimension, dimension);
  setSheetPosition(value);
  drag.history.push({ y: event.clientY, time: performance.now() });
  drag.history = drag.history.filter((sample) => performance.now() - sample.time < 100);
}

function endDrag(event) {
  if (!drag.active || event.pointerId !== drag.pointer) return;
  drag.active = false;
  const first = drag.history[0];
  const last = drag.history.at(-1);
  const velocity = last && first && last.time > first.time ? ((last.y - first.y) / (last.time - first.time)) * 1000 : 0;
  const projected = motion.value + (velocity / 1000) * .99 / (1 - .99);
  if (velocity > 650 || projected > elements.inspector.offsetHeight * .34) closeInspector();
  else animateSheet(0, velocity);
}

const dialogSheetMotion = new WeakMap();
const dialogDrag = { dialog: null, pointer: 0, startY: 0, startValue: 0, history: [] };

function getDialogMotion(dialog) {
  let value = dialogSheetMotion.get(dialog);
  if (!value) {
    value = { value: 0, velocity: 0, frame: 0, last: 0, completion: null };
    dialogSheetMotion.set(dialog, value);
  }
  return value;
}

function setDialogSheetPosition(dialog, value) {
  const current = getDialogMotion(dialog);
  current.value = value;
  dialog.style.transform = isMobileSheet() ? `translate3d(0, ${value}px, 0)` : "";
}

function animateDialogSheet(dialog, target, velocity = 0, completion = null) {
  const current = getDialogMotion(dialog);
  cancelAnimationFrame(current.frame);
  if (!isMobileSheet() || matchMedia("(prefers-reduced-motion: reduce)").matches) {
    setDialogSheetPosition(dialog, target);
    if (completion) completion();
    return;
  }
  current.velocity = velocity;
  current.last = performance.now();
  current.completion = completion;
  const stiffness = 210;
  const damping = target === 0 && Math.abs(velocity) > 250 ? 24 : 29;
  const step = (now) => {
    const dt = Math.min((now - current.last) / 1000, .032);
    current.last = now;
    current.velocity += (stiffness * (target - current.value) - damping * current.velocity) * dt;
    setDialogSheetPosition(dialog, current.value + current.velocity * dt);
    if (Math.abs(target - current.value) < .45 && Math.abs(current.velocity) < 4) {
      setDialogSheetPosition(dialog, target);
      const done = current.completion;
      current.completion = null;
      if (done) done();
      return;
    }
    current.frame = requestAnimationFrame(step);
  };
  current.frame = requestAnimationFrame(step);
}

function showEditorDialog(dialog) {
  dialog.showModal();
  if (!isMobileSheet() || matchMedia("(prefers-reduced-motion: reduce)").matches) return;
  setDialogSheetPosition(dialog, dialog.offsetHeight);
  requestAnimationFrame(() => animateDialogSheet(dialog, 0));
}

function closeEditorDialog(dialog) {
  if (!dialog?.open) return;
  const finish = () => {
    dialog.style.transform = "";
    dialog.close();
  };
  if (!isMobileSheet() || matchMedia("(prefers-reduced-motion: reduce)").matches) {
    finish();
    return;
  }
  animateDialogSheet(dialog, dialog.offsetHeight, getDialogMotion(dialog).velocity, finish);
}

function beginDialogDrag(event) {
  const dialog = event.currentTarget.closest(".editor-dialog");
  if (!dialog || !isMobileSheet() || event.target.closest("button, input, select, textarea, a")) return;
  const current = getDialogMotion(dialog);
  cancelAnimationFrame(current.frame);
  dialogDrag.dialog = dialog;
  dialogDrag.pointer = event.pointerId;
  dialogDrag.startY = event.clientY;
  dialogDrag.startValue = current.value;
  dialogDrag.history = [{ y: event.clientY, time: performance.now() }];
  event.currentTarget.setPointerCapture(event.pointerId);
}

function moveDialogDrag(event) {
  if (!dialogDrag.dialog || event.pointerId !== dialogDrag.pointer) return;
  const dimension = dialogDrag.dialog.offsetHeight;
  let value = dialogDrag.startValue + event.clientY - dialogDrag.startY;
  if (value < 0) value = rubberband(value, dimension);
  else if (value > dimension) value = dimension + rubberband(value - dimension, dimension);
  setDialogSheetPosition(dialogDrag.dialog, value);
  dialogDrag.history.push({ y: event.clientY, time: performance.now() });
  dialogDrag.history = dialogDrag.history.filter((sample) => performance.now() - sample.time < 100);
}

function endDialogDrag(event) {
  if (!dialogDrag.dialog || event.pointerId !== dialogDrag.pointer) return;
  const dialog = dialogDrag.dialog;
  const first = dialogDrag.history[0];
  const last = dialogDrag.history.at(-1);
  const velocity = last && first && last.time > first.time ? ((last.y - first.y) / (last.time - first.time)) * 1000 : 0;
  const projected = getDialogMotion(dialog).value + (velocity / 1000) * .99 / (1 - .99);
  dialogDrag.dialog = null;
  if (velocity > 650 || projected > dialog.offsetHeight * .34) closeEditorDialog(dialog);
  else animateDialogSheet(dialog, 0, velocity);
}

function renderToolGroups() {
  elements.groupList.replaceChildren();
  elements.groupEmpty.hidden = state.groups.length !== 0;
  for (const group of state.groups) {
    const row = document.createElement("article");
    row.className = "backend-row";
    row.tabIndex = 0;
    row.setAttribute("role", "button");
    const identity = document.createElement("div");
    identity.className = "backend-identity";
    const icon = document.createElement("div");
    icon.className = "backend-icon";
    icon.textContent = group.id.slice(0, 2).toUpperCase();
    const labels = document.createElement("div");
    const title = document.createElement("strong");
    title.textContent = group.id;
    const url = document.createElement("small");
    url.textContent = group.base_url;
    labels.append(title, url); identity.append(icon, labels);
    const capabilities = document.createElement("div"); capabilities.className = "backend-meta";
    capabilities.innerHTML = `<strong>${group.runtime.tools} HTTP tools</strong><small>${group.oauth ? "OAuth client_credentials" : group.headers?.length ? t("静态 Headers") : t("无需上游认证")}</small>`;
    const policy = document.createElement("div"); policy.className = "backend-meta";
    policy.innerHTML = `<strong>${group.required_scopes?.length ? `${group.required_scopes.length} ${getLocale() === "en" ? "scopes" : "个 Scope"}` : t("无 Scope 限制")}</strong><small>${group.max_response_body_bytes} bytes ${t("响应上限")}</small>`;
    const actions = document.createElement("div"); actions.className = "row-actions";
    const pill = document.createElement("span"); pill.className = "state-pill";
    const dot = document.createElement("i"); dot.className = `status-dot ${group.enabled ? "ready" : "disabled"}`;
    pill.append(dot, document.createTextNode(group.enabled ? t("已启用") : t("已停用")));
    const chevron = document.createElement("span"); chevron.className = "chevron"; chevron.textContent = "›";
    actions.append(pill, chevron); row.append(identity, capabilities, policy, actions);
    row.addEventListener("click", () => openGroupDialog(group));
    row.addEventListener("keydown", (event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); openGroupDialog(group); } });
    elements.groupList.append(row);
  }
  translateDOM(elements.groupList);
}

async function openGroupDialog(group = null) {
  state.groupEditing = group;
  const dialog = $("#group-dialog");
  $("#group-form").reset();
  $("#group-error").hidden = true;
  $("#group-id").disabled = Boolean(group);
  $("#group-response-limit").value = "1048576";
  $("#group-timeout").value = "60s";
  $("#group-enabled").checked = true;
  $("#group-detail").hidden = !group;
  $("#delete-group").hidden = !group;
  $("#probe-group").hidden = !group;
  $("#save-group").textContent = group ? t("保存并应用") : t("创建工具组");
  $("#group-dialog-title").textContent = group ? group.id : t("新建工具组");
  if (group) {
    $("#group-id").value = group.id;
    $("#group-base-url").value = group.base_url;
    $("#group-timeout").value = group.request_timeout;
    $("#group-response-limit").value = String(group.max_response_body_bytes);
    $("#group-enabled").checked = group.enabled;
    $("#group-scopes").value = (group.required_scopes || []).join(", ");
    $("#group-rules").value = group.tool_rules?.length ? JSON.stringify(group.tool_rules, null, 2) : "";
    $("#group-headers").value = group.headers?.length ? JSON.stringify(group.headers.map((header) => ({ name: header.name })), null, 2) : "";
    if (group.oauth) {
      $("#group-oauth-issuer").value = group.oauth.issuer;
      $("#group-oauth-client-id").value = group.oauth.client_id;
      $("#group-oauth-scopes").value = (group.oauth.scopes || []).join(", ");
      $("#group-oauth-secret").placeholder = t("已安全保存，留空保持");
    }
    $("#group-public-prefix").textContent = `${t("公开命名空间：")}${group.id}.*`;
    await refreshGroupChildren(group.id);
  }
  showEditorDialog(dialog);
  requestAnimationFrame(() => $(group ? "#group-base-url" : "#group-id").focus());
}

async function refreshGroupChildren(groupID) {
  const [toolData, importData] = await Promise.all([api(`/tool-groups/${encodeURIComponent(groupID)}/tools`), api(`/tool-groups/${encodeURIComponent(groupID)}/imports`)]);
  const toolList = $("#group-tool-list"); toolList.replaceChildren();
  for (const tool of toolData.tools || []) {
    const row = document.createElement("div"); row.className = "mini-row";
    const label = document.createElement("div");
    const title = document.createElement("strong");
    const method = document.createElement("span"); method.className = "method-badge"; method.textContent = tool.method;
    title.append(method, document.createTextNode(` ${tool.name}`));
    const detail = document.createElement("small"); detail.textContent = `${tool.path} · ${tool.origin === "openapi" ? `OpenAPI ${tool.import_id}` : t("手工")}`;
    label.append(title, detail);
    const edit = document.createElement("button"); edit.className = "text-button pressable"; edit.type = "button"; edit.textContent = tool.origin === "manual" ? t("编辑") : t("由来源管理"); edit.disabled = tool.origin !== "manual";
    edit.addEventListener("click", () => openToolDialog(tool)); row.append(label, edit); toolList.append(row);
  }
  if (!toolData.tools?.length) toolList.textContent = t("还没有 HTTP Tools");
  const importList = $("#group-import-list"); importList.replaceChildren();
  for (const item of importData.imports || []) {
    const row = document.createElement("div"); row.className = "mini-row";
    const label = document.createElement("div");
    const title = document.createElement("strong"); title.textContent = item.id;
    const detail = document.createElement("small"); detail.textContent = `${item.title || item.openapi_version} · ${item.selected?.length || 0} operations · ${item.last_refresh_ok === false ? t("使用 LKG") : item.refresh_interval || t("手工更新")}`;
    label.append(title, detail);
    const actions = document.createElement("div");
    if (item.origin_type === "url") {
      const update = document.createElement("button"); update.type = "button"; update.className = "text-button pressable"; update.textContent = t("立即刷新");
      update.addEventListener("click", async () => { try { await api(`/tool-groups/${encodeURIComponent(groupID)}/imports/${encodeURIComponent(item.id)}/refresh`, { method: "POST", headers: { "If-Match": `"${item.revision}"` } }); await refreshGroupChildren(groupID); announce(`${item.id} ${t("已刷新")}`); } catch (error) { announce(`${t("刷新失败")}：${error.message}`); } });
      actions.append(update);
    }
    const remove = document.createElement("button"); remove.type = "button"; remove.className = "danger text-button pressable"; remove.textContent = t("删除");
    remove.addEventListener("click", async () => { if (!confirm(t("删除 OpenAPI 来源 {id} 及其生成的 tools？", { id: item.id }))) return; try { await api(`/tool-groups/${encodeURIComponent(groupID)}/imports/${encodeURIComponent(item.id)}`, { method: "DELETE", headers: { "If-Match": `"${item.revision}"` } }); await refreshGroupChildren(groupID); await refresh(); } catch (error) { announce(error.message); } });
    actions.append(remove); row.append(label, actions); importList.append(row);
  }
  if (!importData.imports?.length) importList.textContent = t("还没有 OpenAPI 来源");
  translateDOM($("#group-detail"));
}

function collectGroupInput() {
  let headers = [], rules = [];
  if ($("#group-headers").value.trim()) headers = JSON.parse($("#group-headers").value);
  if ($("#group-rules").value.trim()) rules = JSON.parse($("#group-rules").value);
  if (!Array.isArray(headers) || !Array.isArray(rules)) throw new Error(t("Headers 与 Tool Rules 必须是 JSON 数组"));
  const input = { id: $("#group-id").value.trim(), base_url: $("#group-base-url").value.trim(), enabled: $("#group-enabled").checked, required_scopes: splitValues($("#group-scopes").value), tool_rules: rules, request_timeout: $("#group-timeout").value.trim(), max_response_body_bytes: Number($("#group-response-limit").value), headers };
  if ($("#group-oauth-issuer").value.trim() || $("#group-oauth-client-id").value.trim()) {
    input.oauth = { type: "client_credentials", issuer: $("#group-oauth-issuer").value.trim(), client_id: $("#group-oauth-client-id").value.trim(), scopes: splitValues($("#group-oauth-scopes").value) };
    if ($("#group-oauth-secret").value) input.oauth.client_secret = $("#group-oauth-secret").value;
  }
  return input;
}

async function saveGroup(event) {
  event.preventDefault();
  if (!event.currentTarget.reportValidity()) return;
  let input; try { input = collectGroupInput(); } catch (error) { showDialogError("group", error.message); return; }
  const editing = state.groupEditing;
  try {
    const saved = await api(editing ? `/tool-groups/${encodeURIComponent(editing.id)}` : "/tool-groups", { method: editing ? "PUT" : "POST", headers: editing ? { "If-Match": `"${editing.revision}"` } : {}, body: JSON.stringify(input) });
    state.groupEditing = saved;
    closeEditorDialog($("#group-dialog")); announce(`${saved.id} ${editing ? t("已更新") : t("已创建")}`); await refresh();
  } catch (error) { showDialogError("group", error.message); }
}

function addParameterRow(parameter = {}) {
	  const row = document.createElement("div"); row.className = "parameter-row";
	  const main = document.createElement("div"); main.className = "parameter-main";
	  const name = Object.assign(document.createElement("input"), { value: parameter.name || "", placeholder: t("上游名称"), ariaLabel: t("上游参数名称") }); name.className = "param-name";
	  const argument = Object.assign(document.createElement("input"), { value: parameter.argument || "", placeholder: t("MCP 参数名"), ariaLabel: t("MCP 参数名称") }); argument.className = "param-argument";
	  const location = document.createElement("select"); location.className = "param-in"; location.innerHTML = '<option value="query">query</option><option value="path">path</option><option value="header">header</option>'; location.value = parameter.in || "query";
	  const type = document.createElement("select"); type.className = "param-type"; type.innerHTML = '<option>string</option><option>integer</option><option>number</option><option>boolean</option><option>array</option>'; type.value = parameter.schema?.type || "string";
	  const required = document.createElement("label"); const check = Object.assign(document.createElement("input"), { type: "checkbox", checked: Boolean(parameter.required) }); check.className = "param-required"; required.append(check, t("必填"));
	  const remove = document.createElement("button"); remove.type = "button"; remove.className = "icon-button pressable"; remove.textContent = "−"; remove.setAttribute("aria-label", t("删除参数")); remove.addEventListener("click", () => { row.remove(); renderToolRequestPreview(); });
	  main.append(name, argument, location, type, required, remove);
	  const description = Object.assign(document.createElement("input"), { value: parameter.description || "", placeholder: t("参数描述（可选）"), ariaLabel: t("参数描述") }); description.className = "param-description";
	  const advanced = document.createElement("details"); advanced.className = "param-advanced";
	  const summary = document.createElement("summary"); summary.textContent = t("高级 JSON Schema · enum / default / 约束");
	  const schema = document.createElement("textarea"); schema.className = "param-schema"; schema.rows = 4; schema.spellcheck = false; schema.setAttribute("aria-label", t("参数 JSON Schema")); schema.value = JSON.stringify(parameter.schema || { type: "string" }, null, 2);
	  advanced.append(summary, schema); row.append(main, description, advanced); $("#parameter-list").append(row);
	  type.addEventListener("change", () => {
	    try {
	      const value = schema.value.trim() ? JSON.parse(schema.value) : {};
	      value.type = type.value;
	      if (type.value === "array" && (!value.items || typeof value.items !== "object")) value.items = { type: "string" };
	      if (type.value !== "array") delete value.items;
	      schema.value = JSON.stringify(value, null, 2);
	    } catch (_) { advanced.open = true; }
	  });
	  renderToolRequestPreview();
}

function openToolDialog(tool = null) {
	  state.toolEditing = tool; $("#tool-form").reset(); $("#parameter-list").replaceChildren(); $("#tool-error").hidden = true;
  $("#tool-name").disabled = Boolean(tool); $("#delete-tool").hidden = !tool; $("#save-tool").textContent = tool ? t("保存并应用") : t("添加 Tool");
  $("#tool-dialog-title").textContent = tool ? `${t("编辑")} ${tool.name}` : t("添加 Tool");
	  if (tool) { $("#tool-name").value = tool.name; $("#tool-method").value = tool.method; $("#tool-path").value = tool.path; $("#tool-description").value = tool.description; $("#tool-enabled").checked = tool.enabled; $("#tool-body-required").checked = tool.body_required; $("#tool-body-schema").value = tool.body_schema ? JSON.stringify(tool.body_schema, null, 2) : ""; $("#tool-output-schema").value = tool.output_schema ? JSON.stringify(tool.output_schema, null, 2) : ""; for (const parameter of tool.parameters || []) addParameterRow(parameter); if ([tool.body_schema, tool.output_schema, ...(tool.parameters || []).map((item) => item.schema)].some(hasUnsafeJSONNumber)) showDialogError("tool", t("Schema 含超出浏览器安全整数范围的数值；请使用管理 API 编辑，页面不会覆盖该配置。")); }
	  renderToolRequestPreview();
	  showEditorDialog($("#tool-dialog"));
  requestAnimationFrame(() => $(tool ? "#tool-path" : "#tool-name").focus());
}

function collectToolInput() {
	  const parameters = $$(".parameter-row", $("#parameter-list")).map((row) => { const raw = $(".param-schema", row).value.trim(); const schema = raw ? JSON.parse(raw) : { type: $(".param-type", row).value }; assertSafeJSONNumbers(schema); return { name: $(".param-name", row).value.trim(), argument: $(".param-argument", row).value.trim(), in: $(".param-in", row).value, required: $(".param-required", row).checked, description: $(".param-description", row).value.trim(), schema }; });
	  const parseSchema = (selector) => { if (!$(selector).value.trim()) return undefined; const schema = JSON.parse($(selector).value); assertSafeJSONNumbers(schema); return schema; };
	  return { name: $("#tool-name").value.trim(), description: $("#tool-description").value.trim(), enabled: $("#tool-enabled").checked, method: $("#tool-method").value, path: $("#tool-path").value.trim(), parameters, body_schema: parseSchema("#tool-body-schema"), body_required: $("#tool-body-required").checked, output_schema: parseSchema("#tool-output-schema") };
}

function hasUnsafeJSONNumber(value) {
	  if (typeof value === "number") return !Number.isFinite(value) || (Number.isInteger(value) && !Number.isSafeInteger(value));
	  if (Array.isArray(value)) return value.some(hasUnsafeJSONNumber);
	  return Boolean(value && typeof value === "object" && Object.values(value).some(hasUnsafeJSONNumber));
}

function assertSafeJSONNumbers(value) {
	  if (hasUnsafeJSONNumber(value)) throw new Error(t("Schema 数值超出浏览器安全整数范围，请使用管理 API 精确配置"));
}

function renderToolRequestPreview() {
	  const group = state.groupEditing;
	  if (!group || !$("#tool-request-preview")) return;
	  let path = $("#tool-path").value.trim() || "/path";
	  const query = [];
	  const headers = [];
	  for (const row of $$(".parameter-row", $("#parameter-list"))) {
	    const name = $(".param-name", row).value.trim() || "name";
	    const argument = $(".param-argument", row).value.trim() || name;
	    const location = $(".param-in", row).value;
	    if (location === "path") path = path.split(`{${name}}`).join(`{${argument}}`);
	    if (location === "query") query.push(`${encodeURIComponent(name)}={${argument}}`);
	    if (location === "header") headers.push(`${name}: {${argument}}`);
	  }
	  const base = (group.base_url || "https://api.example.com").replace(/\/$/, "");
	  const lines = [`${$("#tool-method").value} ${base}${path}${query.length ? `?${query.join("&")}` : ""}`, ...headers];
	  if ($("#tool-body-schema").value.trim()) lines.push("", "JSON body ← {body}");
	  $("#tool-request-preview").textContent = lines.join("\n");
}

async function saveTool(event) {
	  event.preventDefault(); if (!event.currentTarget.reportValidity()) return;
	  let input; try { input = collectToolInput(); } catch (error) { showDialogError("tool", error.message || t("JSON Schema 无效")); return; }
  const groupID = state.groupEditing.id, editing = state.toolEditing;
  try { await api(editing ? `/tool-groups/${encodeURIComponent(groupID)}/tools/${encodeURIComponent(editing.name)}` : `/tool-groups/${encodeURIComponent(groupID)}/tools`, { method: editing ? "PUT" : "POST", headers: editing ? { "If-Match": `"${editing.revision}"` } : {}, body: JSON.stringify(input) }); closeEditorDialog($("#tool-dialog")); await refreshGroupChildren(groupID); await refresh(); announce(`${input.name} ${editing ? t("已更新") : t("已添加")}`); } catch (error) { showDialogError("tool", error.message); }
}

async function deleteTool() {
  const tool = state.toolEditing; if (!tool || !confirm(t("删除 {name}？", { name: tool.name }))) return;
  try { await api(`/tool-groups/${encodeURIComponent(state.groupEditing.id)}/tools/${encodeURIComponent(tool.name)}`, { method: "DELETE", headers: { "If-Match": `"${tool.revision}"` } }); closeEditorDialog($("#tool-dialog")); await refreshGroupChildren(state.groupEditing.id); await refresh(); } catch (error) { showDialogError("tool", error.message); }
}

function openImportDialog() { state.importPreview = null; state.importDocument = ""; $("#import-form").reset(); $("#operation-list").replaceChildren(); $("#import-summary").hidden = true; $("#import-error").hidden = true; $("#save-import").disabled = true; showEditorDialog($("#import-dialog")); requestAnimationFrame(() => $("#import-id").focus()); }

async function inspectImport() {
  const origin = $("#import-origin").value; let document = "", filename = "";
  if (origin === "upload") { const file = $("#import-file").files[0]; if (!file) { showDialogError("import", t("请选择规格文件")); return; } document = await file.text(); filename = file.name; }
  const input = { origin_type: origin, spec_url: $("#import-url").value.trim(), filename, document };
  try { const preview = await api(`/tool-groups/${encodeURIComponent(state.groupEditing.id)}/imports/inspect`, { method: "POST", body: JSON.stringify(input) }); state.importPreview = preview; state.importDocument = preview.document; renderOperations(preview); } catch (error) { showDialogError("import", error.message); }
}

function renderOperations(preview) {
  $("#import-summary").hidden = false; $("#import-summary").textContent = `${preview.title || "OpenAPI"} · ${preview.openapi_version} · ${preview.operations.length} operations`;
  const list = $("#operation-list"); list.replaceChildren();
  for (const operation of preview.operations) { const row = document.createElement("label"); row.className = `operation-row${operation.eligible ? "" : " unsupported"}`; const check = document.createElement("input"); check.type = "checkbox"; check.disabled = !operation.eligible; check.dataset.key = operation.key; const label = document.createElement("div"); const title = document.createElement("strong"); const method = document.createElement("span"); method.className = "method-badge"; method.textContent = operation.method; title.append(method, document.createTextNode(` ${operation.path}`)); const detail = document.createElement("small"); detail.textContent = operation.summary || operation.reason || operation.operation_id || ""; label.append(title, detail); const name = document.createElement("input"); name.type = "text"; name.value = operation.suggested_tool_name; name.disabled = !operation.eligible; name.className = "operation-name"; row.append(check, label, name); list.append(row); }
  $("#save-import").disabled = false;
}

async function saveImport(event) {
  event.preventDefault(); if (!state.importPreview) { showDialogError("import", t("请先解析规格")); return; }
  const selected = $$(".operation-row", $("#operation-list")).filter((row) => $("input[type=checkbox]", row).checked).map((row) => ({ operation_key: $("input[type=checkbox]", row).dataset.key, tool_name: $(".operation-name", row).value.trim(), enabled: true }));
  if (!selected.length) { showDialogError("import", t("至少选择一个 operation")); return; }
  const origin = $("#import-origin").value; const file = $("#import-file").files[0];
  const input = { id: $("#import-id").value.trim(), origin_type: origin, spec_url: $("#import-url").value.trim(), filename: file?.name || "", document: state.importDocument, sha256: state.importPreview.sha256, refresh_interval: origin === "url" ? $("#import-interval").value.trim() : "", selected };
  try { await api(`/tool-groups/${encodeURIComponent(state.groupEditing.id)}/imports`, { method: "POST", body: JSON.stringify(input) }); closeEditorDialog($("#import-dialog")); await refreshGroupChildren(state.groupEditing.id); await refresh(); announce(`${input.id} ${t("已导入")}`); } catch (error) { showDialogError("import", error.message); }
}

function showDialogError(kind, message) { const element = $(`#${kind}-error`); element.textContent = message; element.hidden = false; }

$("#register-button").addEventListener("click", () => openInspector());
$("#empty-register").addEventListener("click", () => openInspector());
$("#close-inspector").addEventListener("click", closeInspector);
elements.scrim.addEventListener("click", closeInspector);
elements.form.addEventListener("submit", saveForm);
elements.probe.addEventListener("click", probeForm);
$("#add-header").addEventListener("click", () => addHeaderRow());
$("#search").addEventListener("input", renderBackends);
for (const button of $$(".segmented button")) button.addEventListener("click", () => setAuthMode(button.dataset.auth));

elements.remove.addEventListener("click", () => {
  $("#confirm-copy").textContent = t("删除 {id} 后会立即从聚合目录移除，且无法撤销。", { id: state.editing?.id || t("此后端") });
  $("#confirm-dialog").showModal();
});
elements.copy.addEventListener("click", copyCurrent);
$("#cancel-delete").addEventListener("click", () => $("#confirm-dialog").close());
$("#confirm-delete").addEventListener("click", deleteCurrent);

$("#new-group-button").addEventListener("click", () => openGroupDialog());
$("#empty-group-button").addEventListener("click", () => openGroupDialog());
$("#group-form").addEventListener("submit", saveGroup);
$("#add-http-tool").addEventListener("click", () => openToolDialog());
$("#add-openapi-import").addEventListener("click", openImportDialog);
$("#add-parameter").addEventListener("click", () => addParameterRow());
$("#tool-form").addEventListener("submit", saveTool);
$("#tool-form").addEventListener("input", renderToolRequestPreview);
$("#tool-form").addEventListener("change", renderToolRequestPreview);
$("#delete-tool").addEventListener("click", deleteTool);
$("#import-form").addEventListener("submit", saveImport);
$("#inspect-import").addEventListener("click", inspectImport);
$("#import-origin").addEventListener("change", () => { const upload = $("#import-origin").value === "upload"; $("#import-url-field").hidden = upload; $("#import-file-field").hidden = !upload; $("#import-interval").disabled = upload; });
$("#probe-group").addEventListener("click", async () => { try { await api(`/tool-groups/${encodeURIComponent(state.groupEditing.id)}/probe`, { method: "POST" }); announce(t("Base URL 可达")); } catch (error) { showDialogError("group", error.message); } });
$("#delete-group").addEventListener("click", async () => { const group = state.groupEditing; if (!group || !confirm(t("删除工具组 {id}、其中 tools 和 imports？", { id: group.id }))) return; try { await api(`/tool-groups/${encodeURIComponent(group.id)}`, { method: "DELETE", headers: { "If-Match": `"${group.revision}"` } }); closeEditorDialog($("#group-dialog")); await refresh(); } catch (error) { showDialogError("group", error.message); } });
for (const button of $$(".dialog-close")) button.addEventListener("click", () => closeEditorDialog(button.closest("dialog")));
for (const dialog of $$(".editor-dialog")) {
  const header = $("header", dialog);
  header.addEventListener("pointerdown", beginDialogDrag);
  header.addEventListener("pointermove", moveDialogDrag);
  header.addEventListener("pointerup", endDialogDrag);
  header.addEventListener("pointercancel", endDialogDrag);
  dialog.addEventListener("cancel", (event) => { event.preventDefault(); closeEditorDialog(dialog); });
}

const handle = $("#drag-handle");
handle.addEventListener("pointerdown", beginDrag);
handle.addEventListener("pointermove", moveDrag);
handle.addEventListener("pointerup", endDrag);
handle.addEventListener("pointercancel", endDrag);

document.addEventListener("keydown", (event) => {
  if (event.key === "Escape" && !elements.inspector.hidden && !$("#confirm-dialog").open) closeInspector();
  if (event.key === "Tab" && !elements.inspector.hidden) trapFocus(event);
});

function trapFocus(event) {
  const focusable = $$("button:not([disabled]), input:not([disabled]), textarea:not([disabled]), summary", elements.inspector).filter((item) => item.offsetParent !== null);
  if (!focusable.length) return;
  const first = focusable[0];
  const last = focusable.at(-1);
  if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
}

window.addEventListener("resize", () => {
  if (!elements.inspector.hidden) setSheetPosition(0);
});

function updateLanguageControl() {
  const button = $("#language-toggle");
  const english = getLocale() === "en";
  button.textContent = english ? "中文" : "EN";
  button.setAttribute("aria-label", english ? "切换到中文" : "Switch to English");
}

function updateOpenEditorsForLocale() {
  if (!elements.inspector.hidden) {
    $("#inspector-kicker").textContent = state.editing ? t("编辑配置") : t("新建配置");
    $("#inspector-title").textContent = state.editing?.id || t("注册 MCP 后端");
    elements.save.textContent = state.editing ? t("保存并应用") : t("注册并启用");
    elements.probe.textContent = t("测试连接");
  }
  if ($("#group-dialog").open) {
    $("#group-dialog-title").textContent = state.groupEditing?.id || t("新建工具组");
    $("#save-group").textContent = state.groupEditing ? t("保存并应用") : t("创建工具组");
    if (state.groupEditing) $("#group-public-prefix").textContent = `${t("公开命名空间：")}${state.groupEditing.id}.*`;
  }
  if ($("#tool-dialog").open) {
    $("#tool-dialog-title").textContent = state.toolEditing ? `${t("编辑")} ${state.toolEditing.name}` : t("添加 Tool");
    $("#save-tool").textContent = state.toolEditing ? t("保存并应用") : t("添加 Tool");
  }
}

async function changeLanguage() {
  setLocale(getLocale() === "en" ? "zh-CN" : "en");
  translateDOM(document);
  updateLanguageControl();
  updateOpenEditorsForLocale();
  renderBackends();
  renderToolGroups();
  renderEvents(state.events);
  if (state.groupEditing && $("#group-dialog").open) await refreshGroupChildren(state.groupEditing.id);
  announce(getLocale() === "en" ? "Language changed to English" : "语言已切换为中文");
}

setLocale(getLocale());
translateDOM(document);
updateLanguageControl();
$("#language-toggle").addEventListener("click", changeLanguage);
refresh();
setInterval(() => { if (!state.busy) refresh(); }, 5000);
