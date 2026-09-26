const accountMessages = {
  zh: {
    accounts: "已连接账号", accountsIntro: "连接您在这些服务中的账号，客户端调用时会自动使用。",
    connectAccount: "连接账号", reconnectAccount: "重新连接", disconnectAccount: "断开连接",
    not_connected: "尚未连接", connected: "已连接", reconnect_required: "需要重新连接", unavailable: "暂时无法验证", access_removed: "服务已停用或访问权限已移除",
    renewable: "到期自动续期", noRenewal: "到期后需要重新连接", noExpiry: "未提供到期时间",
    accountScopes: "上游授权范围", accountExpires: "到期时间", accountDetails: "查看授权详情",
    tokenTitle: "连接个人账号", tokenIntro: "输入此服务的个人 Token。验证成功后安全保存，客户端无需填写。",
    tokenLabel: "个人 Token", accountLabel: "账号备注（可选）", tokenExpires: "到期时间（可选）",
    saveAccount: "验证并连接", cancelAccount: "取消", accountSaved: "账号已连接。客户端将自动使用您的授权。",
    accountReplaced: "账号已重新连接。请重新进行客户端授权后继续使用。",
    replaceAccount: "重新连接会替换当前上游账号。现有客户端需要重新确认授权，正在执行的请求可能被取消。继续？",
    removeAccount: "断开后客户端将无法使用此账号，相关授权会失效。已执行的操作不会撤销。",
    accountRemoved: "已断开连接，相关客户端授权已失效。",
    cleanupPending: "已断开连接。凭证服务暂时不可用，后台会继续清理保存的凭证。",
    accountLoadFailed: "暂时无法加载账号列表。", retryAccounts: "重试",
    credential_unavailable: "凭证服务暂时不可用，请稍后重试。",
    connection_changed: "账号或服务配置已变更，请刷新后重试。",
    account_required: "请先连接此服务的个人账号。",
    account_rejected: "服务未接受此授权。请检查 Token、有效期和权限后重试。",
    account_access_denied: "您当前没有此服务的访问权限。",
    connection_connected: "账号连接成功，客户端调用将自动使用此授权。",
    connection_reconnected: "账号已重新连接。请重新进行客户端授权后继续使用。",
    connection_cancelled: "已取消授权，原有账号连接保持不变。",
    connection_expired: "授权链接已过期，请重新点击“连接账号”。",
    connection_rejected: "上游服务未接受此账号，请确认访问权限后重试。",
    connection_failed: "账号连接未完成，原有连接保持不变。请重试或联系管理员。",
  },
  en: {
    accounts: "Connected accounts", accountsIntro: "Connect your accounts once. Clients use your access automatically.",
    connectAccount: "Connect account", reconnectAccount: "Reconnect", disconnectAccount: "Disconnect",
    not_connected: "Not connected", connected: "Connected", reconnect_required: "Reconnect needed", unavailable: "Unable to verify", access_removed: "Service disabled or access removed",
    renewable: "Renews automatically", noRenewal: "Reconnect after expiry", noExpiry: "No expiry provided",
    accountScopes: "Upstream permissions", accountExpires: "Expires", accountDetails: "Authorization details",
    tokenTitle: "Connect your account", tokenIntro: "Enter a personal token for this service. It is verified and stored securely. Your client needs no token.",
    tokenLabel: "Personal token", accountLabel: "Account label (optional)", tokenExpires: "Expiry (optional)",
    saveAccount: "Verify and connect", cancelAccount: "Cancel", accountSaved: "Account connected. Clients will use your access automatically.",
    accountReplaced: "Account reconnected. Authorize your clients again to continue.",
    replaceAccount: "Reconnecting replaces this upstream account. Existing clients must authorize again, and active requests may be cancelled. Continue?",
    removeAccount: "Disconnect this account? Related client grants will stop working. Completed operations cannot be undone.",
    accountRemoved: "Account disconnected. Related client grants are no longer valid.",
    cleanupPending: "Disconnected. The credential service is unavailable; saved credentials will be cleaned up in the background.",
    accountLoadFailed: "Accounts could not be loaded.", retryAccounts: "Retry",
    credential_unavailable: "The credential service is unavailable. Try again later.",
    connection_changed: "The account or service changed. Refresh and try again.",
    account_required: "Connect your account for this service first.",
    account_rejected: "The service rejected these credentials. Check the token, expiry and permissions.",
    account_access_denied: "You do not currently have access to this service.",
    connection_connected: "Account connected. Clients will use this authorization automatically.",
    connection_reconnected: "Account reconnected. Authorize your clients again to continue.",
    connection_cancelled: "Authorization cancelled. Your existing connection is unchanged.",
    connection_expired: "The authorization link expired. Select Connect account again.",
    connection_rejected: "The service rejected this account. Check your access and try again.",
    connection_failed: "The account was not connected. Your existing connection is unchanged. Retry or contact an administrator.",
  },
};

async function loadAccounts() {
  const root = byId("accounts");
  root.replaceChildren();
  root.hidden = true;
  try {
    const result = await api("api/accounts");
    if (!result.accounts.length) return;
    root.hidden = false;
    root.append(element("h2", t("accounts")), element("p", t("accountsIntro"), "muted"));
    const list = element("div", undefined, "account-list");
    for (const account of result.accounts) list.append(accountCard(account));
    root.append(list);
  } catch {
    root.hidden = false;
    root.append(element("p", t("accountLoadFailed")), action(t("retryAccounts"), loadAccounts, "secondary"));
  }
}

function accountCard(account) {
  const node = element("article", undefined, "account-card");
  const header = element("div", undefined, "account-heading");
  const title = element("h3", account.endpoint);
  const status = element("span", t(account.status), "account-status " + (account.status === "connected" ? "ready" : ""));
  header.append(title, status);
  node.append(header);
  if (account.account && account.connected) node.append(element("p", account.account, "account-name"));
  if (account.connected) {
    const details = element("details", undefined, "account-details");
    details.append(element("summary", t("accountDetails")));
    const expiry = Date.parse(account.expires_at);
    details.append(element("p", Number.isFinite(expiry) && expiry > 0 ? t("accountExpires") + ": " + new Date(expiry).toLocaleString() : t("noExpiry")));
    details.append(element("p", t(account.renewable ? "renewable" : "noRenewal")));
    if (account.scopes?.length) details.append(element("p", t("accountScopes") + ": " + account.scopes.join(", ")));
    node.append(details);
  }
  const buttons = element("div", undefined, "actions");
  if (account.can_connect) buttons.append(action(t(account.connected ? "reconnectAccount" : "connectAccount"), async () => {
    if (account.connected && !confirm(t("replaceAccount"))) return;
    if (account.oauth) {
      const result = await api("api/accounts/" + encodeURIComponent(account.endpoint) + "/login", "POST");
      const target = new URL(result.authorization_url);
      if (target.protocol !== "https:") throw new Error(t("connection_failed"));
      location.assign(target.href);
    } else openAccountDialog(account);
  }, account.connected ? "secondary" : ""));
  if (account.connected) buttons.append(action(t("disconnectAccount"), async () => {
    if (!confirm(t("removeAccount"))) return;
    const result = await api("api/accounts/" + encodeURIComponent(account.uid) + "/disconnect", "POST", { revision: account.revision });
    await load(false);
    byId("message").textContent = t(result.cleanup_pending ? "cleanupPending" : "accountRemoved");
  }, "secondary"));
  node.append(buttons);
  return node;
}

function openAccountDialog(account) {
  const dialog = byId("account-dialog");
  dialog.replaceChildren();
  const form = element("form");
  const title = element("h2", t("tokenTitle") + " · " + account.endpoint);
  title.id = "account-dialog-title";
  form.append(title, element("p", t("tokenIntro"), "muted"));
  const inputs = {};
  for (const [key, label, type] of [["token", "tokenLabel", "password"], ["account", "accountLabel", "text"], ["expires", "tokenExpires", "datetime-local"]]) {
    const field = element("label", undefined, "account-field");
    const input = element("input");
    input.type = type;
    input.name = key;
    input.autocomplete = key === "token" ? "new-password" : "off";
    input.required = key === "token";
    if (key === "account") input.maxLength = 128;
    if (key === "token") input.maxLength = 32768;
    field.append(element("span", t(label)), input);
    form.append(field);
    inputs[key] = input;
  }
  const error = element("p", "", "account-error");
  error.setAttribute("role", "alert");
  const buttons = element("div", undefined, "actions");
  const submit = element("button", t("saveAccount"));
  submit.type = "submit";
  const cancel = element("button", t("cancelAccount"), "secondary");
  cancel.type = "button";
  cancel.addEventListener("click", () => dialog.close());
  buttons.append(submit, cancel);
  form.append(error, buttons);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!form.reportValidity()) return;
    submit.disabled = true;
    cancel.disabled = true;
    dialog.oncancel = (event) => event.preventDefault();
    error.textContent = "";
    try {
      const body = { token: inputs.token.value, account: inputs.account.value.trim(), revision: account.revision };
      if (inputs.expires.value) body.expires_at = new Date(inputs.expires.value).toISOString();
      await api("api/accounts/" + encodeURIComponent(account.endpoint) + "/token", "POST", body);
      dialog.close();
      await load(false);
      byId("message").textContent = t(account.revision > 0 ? "accountReplaced" : "accountSaved");
    } catch (failure) { error.textContent = failure.message; }
    finally { submit.disabled = false; cancel.disabled = false; dialog.oncancel = null; }
  });
  dialog.append(form);
  dialog.onclose = () => { inputs.token.value = ""; dialog.replaceChildren(); };
  dialog.showModal();
  inputs.token.focus();
}
