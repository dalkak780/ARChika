import React from "react";
import { test } from "vite-plus/test";
import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import i18n, { resolveLanguage, statusLabel } from "./i18n.ts";
import { en } from "./locales/en.ts";
import { ko } from "./locales/ko.ts";
import { APIError, ErrorNotice } from "./api.tsx";
import { Result } from "./admin.tsx";

function leaves(value: object, prefix = ""): [string, string][] {
  return Object.entries(value).flatMap(([key, child]) => {
    const path = prefix ? `${prefix}.${key}` : key;
    assert.ok(child !== null);
    if (typeof child === "string") return [[path, child]];
    assert.equal(typeof child, "object");
    return leaves(child, path);
  });
}

test("locales have identical keys and Korean messages are not empty", () => {
  const english = leaves(en);
  const korean = leaves(ko);
  assert.deepEqual(english.map(([key]) => key).sort(), korean.map(([key]) => key).sort());
  for (const [key, value] of korean) assert.ok(value.trim(), key);
});

test("language resolution uses preference, cache, browser languages, then English", () => {
  assert.equal(resolveLanguage("en", "ko", ["ko-KR"]), "en");
  assert.equal(resolveLanguage("ko", "en", ["en-US"]), "ko");
  assert.equal(resolveLanguage(undefined, "ko", ["en-US"]), "ko");
  assert.equal(resolveLanguage(undefined, "en", ["ko-KR"]), "en");
  assert.equal(resolveLanguage("", "en", ["ko-KR"]), "ko");
  assert.equal(resolveLanguage("", "ko", ["en-GB"]), "en");
  assert.equal(resolveLanguage(undefined, null, ["fr-FR", "ko-KR", "en-US"]), "ko");
  assert.equal(resolveLanguage(undefined, null, ["en-GB", "ko-KR"]), "en");
  assert.equal(resolveLanguage("ja", "invalid", ["KO-kr"]), "ko");
  assert.equal(resolveLanguage(undefined, null, ["fr-FR"]), "en");
  assert.equal(resolveLanguage(undefined, null, []), "en");
});

test("counts and status labels preserve English and translate Korean", async () => {
  try {
    await i18n.changeLanguage("en");
    assert.equal(i18n.t("common.workCount", { count: 1 }), "1 work");
    assert.equal(i18n.t("common.workCount", { count: 2 }), "2 works");
    assert.equal(statusLabel("queued"), "queued");
    await i18n.changeLanguage("ko");
    assert.equal(i18n.t("common.workCount", { count: 1 }), "작품 1개");
    assert.equal(i18n.t("common.workCount", { count: 3 }), "작품 3개");
    assert.equal(statusLabel("queued"), "대기 중");
    assert.equal(statusLabel("account.update"), "계정 수정");
    assert.equal(statusLabel("future-status"), "future-status");
  } finally {
    await i18n.changeLanguage("en");
  }
});

test("API errors preserve server text in English and translate known codes in Korean", async () => {
  const credentials = new APIError(401, {
    error: { code: "invalid_credentials", message: "Invalid account or password" },
  });
  const query = new APIError(400, { error: { code: "invalid_query", message: "Unexpected AND" } });
  const unknown = new APIError(400, { error: { code: "future_code", message: "Server detail" } });
  try {
    await i18n.changeLanguage("en");
    assert.equal(
      renderToStaticMarkup(<ErrorNotice error={credentials} />),
      '<p class="notice error" role="alert"><strong>invalid_credentials: </strong>Invalid account or password</p>',
    );
    await i18n.changeLanguage("ko");
    const translated = renderToStaticMarkup(<ErrorNotice error={credentials} />);
    assert.ok(translated.includes('title="invalid_credentials"'));
    assert.ok(translated.includes(ko.errors.invalid_credentials));
    assert.ok(!translated.includes("<strong>"));
    assert.ok(!translated.includes("Invalid account or password"));
    assert.ok(
      renderToStaticMarkup(<ErrorNotice error={query} />).includes(
        `${ko.errors.invalid_query} (Unexpected AND)`,
      ),
    );
    assert.ok(renderToStaticMarkup(<ErrorNotice error={unknown} />).includes("Server detail"));
    assert.ok(
      renderToStaticMarkup(<ErrorNotice error={new Error("Raw failure")} />).includes(
        "Raw failure",
      ),
    );
  } finally {
    await i18n.changeLanguage("en");
  }
});

test("structured results translate status labels without changing user data or manifests", async () => {
  try {
    await i18n.changeLanguage("ko");
    const result = renderToStaticMarkup(
      <Result
        value={{
          actor: "admin",
          action: "account.update",
          detail: '{"user_id":"user-1"}',
          fields: { title: "Original title", status: "ready" },
        }}
      />,
    );
    assert.ok(result.includes("계정 수정"));
    assert.ok(result.includes("실행자"));
    assert.ok(result.includes("admin"));
    assert.ok(result.includes("user-1"));
    assert.ok(result.includes("Original title"));
    assert.ok(result.includes("ready"));
    const manifest = renderToStaticMarkup(
      <Result value={{ id: "plugin-1", permissions: [] }} raw />,
    );
    assert.ok(manifest.includes("&quot;id&quot;"));
    assert.ok(manifest.includes("permissions"));
  } finally {
    await i18n.changeLanguage("en");
  }
});
