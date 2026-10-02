const copy = {
  账号: "Username",
  密码: "Password",
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
      document.documentElement.lang.startsWith("en")
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
    const password = field(form, "password", "密码", "password"),
      code = field(form, "code", "动态验证码", "text");
    const confirm = node("button", "验证身份", "primary"),
      cancel = node("button", "取消", "secondary");
    confirm.type = "submit";
    cancel.type = "button";
    form.append(confirm, cancel);
    dialog.append(title, form);
    document.body.append(dialog);
    const finish = (value) => {
      form.reset();
      dialog.close();
      dialog.remove();
      resolve(value);
    };
    cancel.onclick = () => finish(null);
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
  const response = await fetch(base + "/account", { cache: "no-store" });
  if (!response.ok) throw new Error(text("请求失败，请稍后重试。"));
  const status = await response.json();
  const dialog = node("dialog"),
    form = node("form"),
    title = node("h2", "我的账号");
  dialog.className = "local-account-dialog";
  dialog.append(
    title,
    node("p", status.username),
    node("p", "密码修改或 MFA 启用后会退出所有会话，请重新登录。"),
  );
  const password = field(form, "password", "密码", "password"),
    code = field(form, "code", status.mfa_enabled ? "动态验证码" : "动态验证码（已启用 MFA 时填写）", "text", status.mfa_enabled),
    newPassword = field(
      form,
      "new_password",
      "新密码（至少 12 个字符）",
      "password",
    );
  newPassword.autocomplete = "new-password";
  newPassword.minLength = 12;
  const message = node("p");
  message.setAttribute("role", "status");
  const change = node("button", "修改密码", "primary"),
    mfa = node(
      "button",
      status.mfa_enabled ? "MFA 已启用" : "启用 MFA",
      "secondary",
    ),
    close = node("button", "关闭", "secondary");
  change.type = "submit";
  mfa.type = close.type = "button";
  mfa.disabled = status.mfa_enabled;
  form.append(change, mfa, close, message);
  dialog.append(form);
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
  let pending = false;
  mfa.onclick = async () => {
    if (!password.reportValidity()) return;
    if (pending && !code.reportValidity()) return;
    mfa.disabled = true;
    message.textContent = "";
    try {
      const result = await send(
        base + "/account/mfa/" + (pending ? "confirm" : "setup"),
        { password: password.value, code: code.value },
        csrf,
      );
      if (pending) {
        finish();
        location.reload();
        return;
      }
      pending = true;
      message.append(node("p", "将密钥添加到验证器，再输入动态验证码确认。"));
      const secret = node("input");
      secret.readOnly = true;
      secret.value = result.secret;
      secret.setAttribute("aria-label", "TOTP secret");
      message.append(secret);
      code.required = true;
      mfa.textContent = text("确认 MFA");
      mfa.dataset.localCopy = "确认 MFA";
      code.focus();
    } catch (error) {
      message.textContent = error.message;
    } finally {
      mfa.disabled = false;
    }
  };
  form.onsubmit = async (event) => {
    event.preventDefault();
    change.disabled = true;
    try {
      await send(
        base + "/account/password",
        {
          password: password.value,
          code: code.value,
          new_password: newPassword.value,
        },
        csrf,
      );
      finish();
      location.reload();
    } catch (error) {
      message.textContent = error.message;
    } finally {
      change.disabled = false;
    }
  };
  dialog.showModal();
  password.focus();
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
