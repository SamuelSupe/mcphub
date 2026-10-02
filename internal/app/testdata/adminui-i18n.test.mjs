import test from "node:test";
import assert from "node:assert/strict";
import { t, setLocale } from "../adminui/i18n.js";

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
