import { t, getLocale } from "./i18n.js";
import { element } from "./tool-policies.js";
const rendered = new WeakMap();

export function ldapTestCredentials() {
  return new Promise((resolve) => {
    const dialog = element("dialog", "", "local-account-dialog");
    const form = element("form", "", "identity-form");
    form.append(element("h2", t("验证 LDAP 测试账号")), element("p", t("使用普通测试账号。密码只用于本次验证，不保存；目录的登录失败与锁定规则仍生效。"), "field-note"));
    const fields = [
      ["username", "测试账号", "text", 256],
      ["password", "测试账号密码", "password", 1024],
    ].map(([name, title, type, max]) => {
      const label = element("label", t(title), "field"), input = element("input", "");
      input.name = name; input.type = type; input.maxLength = max; input.required = true;
      input.autocomplete = type === "password" ? "current-password" : "username";
      label.append(input); form.append(label); return input;
    });
    const run = element("button", t("验证登录与身份映射"), "primary"), cancel = element("button", t("取消"), "secondary");
    run.type = "submit"; cancel.type = "button";
    const finish = (value) => { form.reset(); dialog.close(); dialog.remove(); resolve(value); };
    form.onsubmit = (event) => { event.preventDefault(); finish({ username: fields[0].value, password: fields[1].value }); };
    cancel.onclick = () => finish(null);
    dialog.oncancel = (event) => { event.preventDefault(); finish(null); };
    form.append(run, cancel); dialog.append(form); document.body.append(dialog);
    dialog.showModal(); fields[0].focus();
  });
}

export function renderConnectionTest(container, result) {
  const key = JSON.stringify([getLocale(), result]);
  if (rendered.get(container) === key) return;
  rendered.set(container, key);
  container.replaceChildren();
  container.setAttribute("role", "status");
  if (!result) return;
  const status = { pending: "请打开测试登录，完成企业账号登录后返回。", running: "等待企业登录结果…", passed: "登录与身份映射验证通过。请核对结果，再保存配置。", failed: "登录验证失败，请检查客户端密钥、回调地址、租户与 Claim 类型。" };
  container.append(element("p", t(status[result.status]), "field-note"));
  if (result.start_url) {
    const link = element("a", t("打开测试登录"), "secondary");
    link.href = result.start_url; link.target = "_blank"; link.rel = "noopener noreferrer";
    container.append(link);
  }
  const identity = result.identity;
  if (!identity) return;
  const list = element("dl", "", "connection-test-identity");
  const values = [
    ["身份来源", identity.provider], ["稳定 Subject", identity.subject], ["显示名称", identity.name || "—"],
    ["用户组", identity.groups?.map((id) => identity.group_names?.[id] ? `${identity.group_names[id]} (${id})` : id).join(", ") || "—"],
    ["部门", identity.departments?.join(", ") || "—"],
  ];
  for (const [title, value] of values) list.append(element("dt", t(title)), element("dd", value));
  container.append(list);
  if (identity.missing_claims?.length) container.append(element("p", t("这些 Claim 未返回，请确认映射是否正确：{claims}", { claims: identity.missing_claims.join(", ") }), "attention"));
}

export function confirmIdentityChange(changes) {
  return new Promise((resolve) => {
    const dialog = element("dialog", "", "local-account-dialog"), content = element("div", "", "identity-form");
    content.append(element("h2", t("确认身份服务变更")));
    content.append(element("p", t("保存立即生效，以下来源的用户会话将被撤销，需要重新登录。本地恢复管理员仍可登录。")));
    for (const change of changes) content.append(element("p", `${change.source.toUpperCase()} · ${t(change.enabled ? "已启用" : "已停用")} · ${t("当前来源有 {count} 个用户", { count: change.users })}`));
    content.append(element("p", t("更换来源或应用会创建独立身份空间；已有组权限不会转移到新来源。请核对测试中的 Subject 和组映射。"), "field-note"));
    const save = element("button", t("确认并保存"), "primary"), cancel = element("button", t("继续编辑"), "secondary");
    save.type = cancel.type = "button";
    const finish = (value) => { dialog.close(); dialog.remove(); resolve(value); };
    save.onclick = () => finish(true); cancel.onclick = () => finish(false);
    dialog.oncancel = (event) => { event.preventDefault(); finish(false); };
    content.append(save, cancel); dialog.append(content); document.body.append(dialog);
    dialog.showModal(); cancel.focus();
  });
}
