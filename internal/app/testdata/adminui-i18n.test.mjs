import test from "node:test";
import assert from "node:assert/strict";
import { t, setLocale } from "../adminui/i18n.js";
import { readdirSync, readFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { missingToolScopes } from "../adminui/identity-permissions.js";
import { setupProgress, pairingCommand } from "../adminui/onboarding.js";

test("configuration examples retain HTTP path parameters while translated counts interpolate", () => {
  const previous = globalThis.document;
  globalThis.document = { documentElement: { lang: "" } };
  try {
    for (const locale of ["zh-CN", "en"]) {
      setLocale(locale);
      assert.equal(t("/users/{id}"), "/users/{id}");
      assert.equal(t("(&(uid={{username}})(member={{dn}}))"), "(&(uid={{username}})(member={{dn}}))");
      assert.equal(t("{count} 个资源", { count: 2 }), locale === "en" ? "2 resources" : "2 个资源");
    }
  } finally {
    setLocale("zh-CN");
    globalThis.document = previous;
  }
});

test("group scope suggestions include selected tools and inherited role scopes without unrelated grants", () => {
  const endpoints = [{ id: "docs", required_scopes: ["service:read"], tools: [
    { name: "search", required_scopes: ["service:read", "docs:read"] },
    { name: "delete", required_scopes: ["service:read", "docs:write"] },
  ] }];
  const permissions = { roles: ["approver"], scopes: ["docs:read"], access: [{ endpoint_id: "docs", tools: ["search"] }] };
  assert.deepEqual(missingToolScopes(permissions, endpoints), ["service:read"]);
  assert.deepEqual(missingToolScopes(permissions, endpoints, { approver: ["service:read"] }), []);
  assert.deepEqual(missingToolScopes({ access: [{ endpoint_id: "docs", resources: true }] }, endpoints), ["service:read"]);
  permissions.access[0].tools.push("delete");
  assert.deepEqual(missingToolScopes(permissions, endpoints), ["docs:write", "service:read"]);
});

test("the copyable onboarding command supplies required pairing inputs and preserves its URL as one argument", () => {
  for (const url of ["https://hub.example.com/mcp", "https://hub.example.com/o'hare?query=$(printf unexpected)"]) {
    const result = spawnSync("sh", ["-c", `mcpbridge() { printf '%s\\n' "$@"; }\n${pairingCommand(url)}`], { encoding: "utf8" });
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(result.stdout.trimEnd().split("\n"), ["pair", "start", "--server", url, "--profile", "work", "--name", "Work Agent", "--json"]);
  }
  assert.equal(pairingCommand(""), "");
});

test("onboarding requires the selected user's tool grant and actual successful execution", () => {
  const endpoint = { id: "docs", enabled: true, ready: true, require_client_grant: true };
  const tool = { name: "search", published: true, available: true, effect: "read", required_scopes: ["docs:read"] };
  const identity = { identity_id: "alice", active: true, permissions: { scopes: ["docs:read"], access: [{ endpoint_id: "docs", tools: ["search"] }] } };
  const grant = { endpoint_id: "docs", subject: "alice", status: "active", expires_at: "2030-01-01T00:00:00Z", allowed_scopes: ["docs:read"], allowed_tools: ["search"], capabilities: { tools: true } };
  const call = { method: "tools/call", outcome: "success", endpoint: "docs", tool: "search", subject: "alice" };
  const now = Date.parse("2026-10-03T00:00:00Z");
  assert.deepEqual(setupProgress(endpoint, tool, identity, [grant], [call], now).complete, [true, true, true, true, true]);
  for (const other of [{ ...call, subject: "bob" }, { ...call, method: "tools/list" }, { ...call, outcome: "tool_error" }, { ...call, tool: "other" }]) {
    assert.equal(setupProgress(endpoint, tool, identity, [grant], [other], now).complete[4], false);
  }
  assert.equal(setupProgress(endpoint, tool, identity, [{ ...grant, status: "revoked" }], [], now).complete[3], false);
  assert.equal(setupProgress(endpoint, tool, { ...identity, permissions: { ...identity.permissions, scopes: [] } }, [grant], [], now).complete[2], false);
});

// A duplicate import binding left the whole management console on its loading
// screen. Parse entry modules in the same module mode used by the browser.
test("management and authorization UI assets parse as browser modules", () => {
 for (const relative of ["../adminui/", "../clientportal/"]) {
 const directory = new URL(relative, import.meta.url);
 for (const name of readdirSync(directory).filter(name => name.endsWith(".js"))) {
  const result = spawnSync(process.execPath, ["--input-type=module", "--check"], { input: readFileSync(new URL(name, directory)), encoding: "utf8" });
  assert.equal(result.status, 0, `${relative}${name}: ${result.stderr}`);
 }
 }
});
