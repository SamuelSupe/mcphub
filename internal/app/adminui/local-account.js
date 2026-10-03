const copy = {
  账号: "Username",
  密码: "Password",
  当前密码: "Current password",
  验证器密钥: "Authenticator secret",
  生成验证器密钥: "Generate authenticator secret",
  重新开始设置: "Restart setup",
  "请在 5 分钟内完成确认。密钥过期时重新开始设置。": "Confirm within 5 minutes. Restart setup if the secret expires.",
  "登录尝试过于频繁，请等待 5 分钟后重试。": "Too many sign-in attempts. Wait 5 minutes before retrying.",
  "登录和修改密码时，需要输入验证器中的动态验证码。": "Sign-in and password changes require your authenticator code.",
  "先验证当前密码，再将密钥添加到验证器。输入验证码确认后才会启用 MFA。": "Verify your current password, then add the secret to your authenticator. MFA starts only after you confirm its code.",
  动态验证码: "Authenticator code",
  "动态验证码（已启用 MFA 时填写）": "Authenticator code (if MFA is enabled)",
  登录: "Sign in",
  我的账号: "My account",
  关闭: "Close",
  修改密码: "Change password",
  "新密码（至少 12 个字符）": "New password (at least 12 characters)",
  "启用 MFA": "Enable MFA",
  "确认 MFA": "Confirm MFA",
  "MFA 已启用": "MFA enabled",
  "将密钥添加到验证器，再输入动态验证码确认。":
    "Add this secret to your authenticator, then enter its code to confirm.",
  "密码修改或 MFA 启用后会退出所有会话，请重新登录。":
    "Changing your password or enabling MFA signs out all sessions. Sign in again.",
  验证身份: "Verify identity",
  "审批加强认证需要已启用 MFA。请输入当前密码和验证器验证码；尚未启用时，取消并打开「我的账号」。": "Approval verification requires MFA. Enter your current password and authenticator code. If MFA is not enabled, cancel and open My account first.",
  取消: "Cancel",
  "验证失败，请检查密码和动态验证码。":
    "Verification failed. Check your password and authenticator code.",
  "请求失败，请稍后重试。": "Request failed. Please retry later.",
  创建用户: "Create user",
  重置密码: "Reset password",
  "重置后该用户需要重新登录。": "After a reset, this user must sign in again.",
  显示名称: "Display name",
  保存: "Save",
};
const text = (value) =>
  document.documentElement.lang.startsWith("en") ? copy[value] || value : value;
const node = (tag, value, className) => {
  const el = document.createElement(tag);
  if (value) {
    el.textContent = text(value);
    if (copy[value]) el.dataset.localCopy = value;
  }
  if (className) el.className = className;
  return el;
};
function field(form, name, title, type, required = true) {
  const label = node("label", title, "field"),
    input = document.createElement("input");
  input.name = name;
  input.type = type;
  input.required = required;
  input.maxLength = type === "password" ? 1024 : 64;
  input.autocomplete =
    type === "password"
      ? "current-password"
      : name === "code"
        ? "one-time-code"
        : name === "name"
          ? "nickname"
          : "username";
  if (name === "code") {
    input.inputMode = "numeric";
    input.pattern = "[0-9]{6}";
    input.maxLength = 6;
  }
  label.append(input);
  form.append(label);
  return input;
}
async function send(path, data, csrf) {
  const response = await fetch(path, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      ...(csrf ? { "X-MCPHub-CSRF": csrf } : {}),
    },
    body: JSON.stringify(data),
  });
  const result =
    response.status === 204 ? null : await response.json().catch(() => ({}));
  if (!response.ok)
    throw new Error(
      response.status === 429 ? text("登录尝试过于频繁，请等待 5 分钟后重试。") : response.status >= 500 ? text("请求失败，请稍后重试。") : document.documentElement.lang.startsWith("en")
        ? text("验证失败，请检查密码和动态验证码。")
        : result?.error?.message || text("请求失败，请稍后重试。"),
    );
  return result;
}
export function loginForm(path, onSuccess) {
  const form = node("form");
  form.className = "local-login-form";
  const username = field(form, "username", "账号", "text"),
    password = field(form, "password", "密码", "password"),
    code = field(form, "code", "动态验证码（已启用 MFA 时填写）", "text", false);
  const button = node("button", "登录", "primary");
  button.type = "submit";
  const message = node("p");
  message.setAttribute("role", "status");
  form.append(button, message);
  form.onsubmit = async (event) => {
    event.preventDefault();
    button.disabled = true;
    message.textContent = "";
    try {
      await send(path, {
        username: username.value,
        password: password.value,
        code: code.value,
      });
      form.reset();
      await onSuccess();
    } catch (error) {
      message.textContent = error.message;
      password.value = "";
      code.value = "";
    } finally {
      button.disabled = false;
    }
  };
  return form;
}
export function passwordProof() {
  return new Promise((resolve) => {
    const dialog = node("dialog"),
      form = node("form"),
      title = node("h2", "验证身份");
    dialog.className = "local-account-dialog";
    title.id = "local-verification-title";
    dialog.setAttribute("aria-labelledby", title.id);
    const password = field(form, "password", "当前密码", "password"),
      code = field(form, "code", "动态验证码", "text");
    const confirm = node("button", "验证身份", "primary"),
      cancel = node("button", "取消", "secondary");
    confirm.type = "submit";
    cancel.type = "button";
    form.append(confirm, cancel);
    dialog.append(title, node("p", "审批加强认证需要已启用 MFA。请输入当前密码和验证器验证码；尚未启用时，取消并打开「我的账号」。"), form);
    document.body.append(dialog);
    let finished = false;
    const finish = (value) => {
      if (finished) return;
      finished = true;
      form.reset();
      dialog.close();
      dialog.remove();
      resolve(value);
    };
    cancel.onclick = () => finish(null);
    dialog.onclose = () => finish(null);
    dialog.oncancel = (event) => {
      event.preventDefault();
      finish(null);
    };
    form.onsubmit = (event) => {
      event.preventDefault();
      finish({ password: password.value, code: code.value });
    };
    dialog.showModal();
    password.focus();
  });
}
export async function openLocalAccount(base, csrf) {
  if (document.querySelector("dialog.local-account-dialog[open]")) return;
  const response = await fetch(base + "/account", { cache: "no-store" });
  if (!response.ok) throw new Error(text("请求失败，请稍后重试。"));
  const status = await response.json();
  if (document.querySelector("dialog.local-account-dialog[open]")) return;
  const dialog = node("dialog"), title = node("h2", "我的账号");
  dialog.className = "local-account-dialog";
  title.id = "local-account-title";
  dialog.setAttribute("aria-labelledby", title.id);
  dialog.append(title, node("p", status.username),
    node("p", "密码修改或 MFA 启用后会退出所有会话，请重新登录。"));
  const finish = () => {
    for (const form of dialog.querySelectorAll("form")) form.reset();
    dialog.close();
    dialog.remove();
  };
  dialog.oncancel = (event) => { event.preventDefault(); finish(); };
  const passwordSection = node("details", null, "account-security");
  passwordSection.append(node("summary", "修改密码"));
  const passwordForm = node("form"),
    password = field(passwordForm, "password", "当前密码", "password");
  const code = status.mfa_enabled ? field(passwordForm, "code", "动态验证码", "text") : null;
  const newPassword = field(passwordForm, "new_password", "新密码（至少 12 个字符）", "password");
  newPassword.autocomplete = "new-password";
  newPassword.minLength = 12;
  const change = node("button", "修改密码", "primary"), passwordMessage = node("p");
  change.type = "submit";
  passwordMessage.setAttribute("role", "status");
  passwordForm.append(change, passwordMessage);
  passwordSection.append(passwordForm);
  passwordForm.onsubmit = async (event) => {
    event.preventDefault();
    change.disabled = true;
    passwordMessage.textContent = "";
    try {
      await send(base + "/account/password", {
        password: password.value, code: code?.value || "", new_password: newPassword.value,
      }, csrf);
      finish();
      location.reload();
    } catch (error) { passwordMessage.textContent = error.message; }
    finally { change.disabled = false; }
  };
  const mfaSection = node("details", null, "account-security");
  mfaSection.append(node("summary", status.mfa_enabled ? "MFA 已启用" : "启用 MFA"));
  if (status.mfa_enabled) {
    mfaSection.append(node("p", "登录和修改密码时，需要输入验证器中的动态验证码。"));
  } else {
    const mfaForm = node("form"),
      mfaPassword = field(mfaForm, "password", "当前密码", "password"),
      setup = node("div"),
      mfaCode = field(mfaForm, "code", "动态验证码", "text", false),
      confirm = node("button", "生成验证器密钥", "primary"),
      message = node("p");
    mfaCode.parentElement.hidden = true;
    confirm.type = "submit";
    message.setAttribute("role", "status");
    mfaCode.parentElement.before(setup);
    mfaForm.append(confirm, message);
    mfaSection.append(node("p", "先验证当前密码，再将密钥添加到验证器。输入验证码确认后才会启用 MFA。"), mfaForm);
    let pending = false;
    mfaForm.onsubmit = async (event) => {
      event.preventDefault();
      confirm.disabled = true;
      message.textContent = "";
      try {
        const result = await send(base + "/account/mfa/" + (pending ? "confirm" : "setup"), {
          password: mfaPassword.value, code: mfaCode.value,
        }, csrf);
        if (pending) { finish(); location.reload(); return; }
        pending = true;
        setup.append(node("p", "将密钥添加到验证器，再输入动态验证码确认。"));
        const secretLabel = node("label", "验证器密钥", "field"), secret = node("input");
        secret.readOnly = true;
        secret.value = result.secret;
        secret.autocomplete = "off";
        secretLabel.append(secret);
        setup.append(secretLabel);
        setup.append(node("p", "请在 5 分钟内完成确认。密钥过期时重新开始设置。"));
        const restart = node("button", "重新开始设置", "secondary");
        restart.type = "button";
        restart.onclick = () => {
          if (confirm.disabled) return;
          pending = false;
          setup.replaceChildren();
          message.textContent = "";
          mfaCode.value = "";
          mfaCode.required = false;
          mfaCode.parentElement.hidden = true;
          confirm.textContent = text("生成验证器密钥");
          confirm.dataset.localCopy = "生成验证器密钥";
          mfaPassword.focus();
        };
        setup.append(restart);
        mfaCode.parentElement.hidden = false;
        mfaCode.required = true;
        confirm.textContent = text("确认 MFA");
        confirm.dataset.localCopy = "确认 MFA";
        mfaCode.focus();
      } catch (error) { message.textContent = error.message; }
      finally { confirm.disabled = false; }
    };
  }
  const close = node("button", "关闭", "secondary");
  close.type = "button";
  close.onclick = finish;
  dialog.onclose = finish;
  dialog.append(passwordSection, mfaSection, close);
  document.body.append(dialog);
  dialog.showModal();
  close.focus();
}

export function editAccountCredentials(onSubmit, username) {
  const dialog = node("dialog"),
    form = node("form"),
    title = node("h2", username ? "重置密码" : "创建用户");
  dialog.className = "local-account-dialog";
  const name = username ? null : field(form, "username", "账号", "text"),
    display = username ? null : field(form, "name", "显示名称", "text", false),
    password = field(form, "password", "新密码（至少 12 个字符）", "password");
  password.autocomplete = "new-password";
  password.minLength = 12;
  const save = node("button", "保存", "primary"),
    close = node("button", "取消", "secondary"),
    message = node("p");
  save.type = "submit";
  close.type = "button";
  form.append(save, close, message);
  dialog.append(title, form);
  if (username) {
    title.after(node("p", username), node("p", "重置后该用户需要重新登录。"));
  }
  document.body.append(dialog);
  const finish = () => {
    form.reset();
    dialog.close();
    dialog.remove();
  };
  close.onclick = finish;
  dialog.oncancel = (event) => {
    event.preventDefault();
    finish();
  };
  dialog.onclose = finish;
  form.onsubmit = async (event) => {
    event.preventDefault();
    save.disabled = true;
    try {
      await onSubmit({
        username: name?.value,
        name: display?.value,
        password: password.value,
      });
      finish();
    } catch (error) {
      message.textContent = error.message;
    } finally {
      save.disabled = false;
    }
  };
  dialog.showModal();
  (name || password).focus();
}

new MutationObserver(() => {
  for (const el of document.querySelectorAll("[data-local-copy]")) {
    if (el.firstChild?.nodeType === Node.TEXT_NODE)
      el.firstChild.nodeValue = text(el.dataset.localCopy);
  }
}).observe(document.documentElement, {
  attributes: true,
  attributeFilter: ["lang"],
});
