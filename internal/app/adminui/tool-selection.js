import { t } from "./i18n.js";

const names = (value) => [...new Set(value.split(/[\s,]+/).filter(Boolean))];

// The text field remains authoritative, including names absent from discovery.
export function renderToolSelection(container, field, tools, emptyMessage) {
  container.replaceChildren();
  const selected = new Set(names(field.value));
  const catalog = new Map(tools.map((tool) => [tool.name, tool]));
  for (const name of selected) if (!catalog.has(name)) catalog.set(name, { name, unavailable: true });
  if (!catalog.size) {
    const note = document.createElement("p");
    note.className = "field-note";
    note.textContent = t(emptyMessage);
    container.append(note);
    return;
  }
  const list = document.createElement("div");
  list.className = "tool-selection";
  for (const tool of catalog.values()) {
    const label = document.createElement("label");
    label.className = "tool-choice";
    const check = document.createElement("input");
    check.type = "checkbox";
    check.dataset.unsavedIgnore = "true";
    check.checked = selected.has(tool.name);
    const text = document.createElement("span");
    const title = document.createElement("strong");
    title.textContent = tool.name;
    text.append(title);
    if (tool.unavailable || tool.description) {
      const note = document.createElement("small");
      note.textContent = tool.unavailable ? t("当前目录未发现，保留已有选择") : tool.description;
      text.append(note);
    }
    check.addEventListener("change", () => {
      const value = new Set(names(field.value));
      if (check.checked) value.add(tool.name); else value.delete(tool.name);
      field.value = [...value].join("\n");
    });
    label.append(check, text);
    list.append(label);
  }
  container.append(list);
}
