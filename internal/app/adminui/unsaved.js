import { t } from "./i18n.js";

const baselines = new Map();
let confirmation;

export function clearUnsaved() { baselines.clear(); }

function values(form) {
  return JSON.stringify([...form.querySelectorAll("input, select, textarea")]
    .filter((input) => !input.readOnly && !input.hasAttribute("data-unsaved-ignore"))
    .map((input) => [input.id || input.name, input.type, input.type === "checkbox" || input.type === "radio" ? input.checked : input.value]));
}

export function markClean(root) {
  const forms = root.matches?.("form") ? [root] : [...root.querySelectorAll("form")];
  for (const form of forms) baselines.set(form, values(form));
}

function dirtyForms(root = document, except = null) {
  const forms = [];
  for (const [form, baseline] of baselines) {
    if (!form.isConnected) { baselines.delete(form); continue; }
    if (form === except || !root.contains(form) || form.closest("[hidden]")) continue;
    const dialog = form.closest("dialog");
    if (dialog && !dialog.open) continue;
    if (values(form) !== baseline) forms.push(form);
  }
  return forms;
}

export async function confirmDiscard(root = document, except = null) {
  if (root.matches?.("form[data-saving]") && root !== except) return false;
  if ([...root.querySelectorAll("form[data-saving]")].some((form) => form !== except)) return false;
  const forms = dirtyForms(root, except);
  if (!forms.length) return true;
  if (!confirmation) confirmation = new Promise((resolve) => {
    const dialog = document.createElement("dialog");
    dialog.className = "local-account-dialog";
    dialog.setAttribute("aria-labelledby", "unsaved-title");
    const title = document.createElement("h2");
    title.id = "unsaved-title";
    title.textContent = t("未保存的修改");
    const message = document.createElement("p");
    message.textContent = t("有未保存的修改。放弃修改并继续？");
    const cancel = document.createElement("button");
    cancel.type = "button";
    cancel.className = "primary";
    cancel.textContent = t("继续编辑");
    const discard = document.createElement("button");
    discard.type = "button";
    discard.className = "secondary danger";
    discard.textContent = t("放弃修改");
    const finish = (value) => { dialog.close(); dialog.remove(); confirmation = null; resolve(value); };
    cancel.onclick = () => finish(false);
    discard.onclick = () => finish(true);
    dialog.oncancel = (event) => { event.preventDefault(); finish(false); };
    dialog.append(title, message, cancel, discard);
    document.body.append(dialog);
    dialog.showModal();
    cancel.focus();
  });
  if (!await confirmation) return false;
  forms.forEach(markClean);
  return true;
}

export function lockForm(form) {
  const controls = [...form.querySelectorAll("input, select, textarea, button")];
  const disabled = controls.map((control) => control.disabled);
  form.dataset.saving = "true";
  controls.forEach((control) => { control.disabled = true; });
  let locked = true;
  return () => {
    if (!locked) return;
    locked = false;
    controls.forEach((control, index) => { control.disabled = disabled[index]; });
    delete form.dataset.saving;
  };
}

export function initUnsavedChanges() {
  let hash = location.hash;
  document.addEventListener("click", async (event) => {
    const link = event.target.closest("a[href^='#']");
    if (!link || link.hash === "#main-content" || link.hash === location.hash) return;
    event.preventDefault();
    if (await confirmDiscard()) location.hash = link.hash;
  }, true);
  window.addEventListener("hashchange", (event) => {
    if (!dirtyForms().length) { hash = location.hash; return; }
    event.stopImmediatePropagation();
    confirmDiscard().then((discard) => {
      if (discard) { hash = location.hash; window.dispatchEvent(new HashChangeEvent("hashchange")); }
      else history.replaceState(null, "", location.pathname + location.search + hash);
    });
  });
  window.addEventListener("beforeunload", (event) => {
    if (!dirtyForms().length) return;
    event.preventDefault();
    event.returnValue = "";
  });
}
