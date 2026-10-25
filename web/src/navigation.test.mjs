import test from "node:test";
import assert from "node:assert/strict";
import {
  visiblePages,
  navigatePage,
  keyAction,
  ignoresKeys,
  gestureAction,
} from "./navigation.mjs";
const portrait = { width: 600, height: 900 },
  landscape = { width: 1200, height: 600 };
test("mixed spreads consume each source exactly once, including odd tail", () => {
  const pages = [portrait, portrait, landscape, portrait, portrait, portrait];
  let index = 0;
  const read = [];
  while (true) {
    const shown = visiblePages(pages, index, "spread");
    read.push(...shown);
    if (shown.at(-1) === pages.length - 1) break;
    index = navigatePage(pages, index, "next", "spread", shown);
  }
  assert.deepEqual(read, [0, 1, 2, 3, 4, 5]);
  assert.equal(navigatePage(pages, 3, "previous", "spread"), 2);
  assert.equal(navigatePage(pages, 2, "previous", "spread"), 0);
});
test("slow dimensions never skip and known shown boundary stays stable", () => {
  const pages = [portrait, {}, portrait];
  const shown = visiblePages(pages, 0, "spread");
  pages[1] = portrait;
  assert.equal(navigatePage(pages, 0, "next", "spread", shown), 1);
  assert.deepEqual(visiblePages(pages, 2, "spread"), [2]);
});
function focus(tag, type, isContentEditable = false) {
  const target = {
    type,
    isContentEditable,
    closest: (selector) => selector.split(", ").includes(tag) ? target : null,
  };
  return target;
}
test("keys ignore text entry and arrow-driven inputs but work from buttons, links and dialogs", () => {
  for (const target of [
    focus("input", "text"),
    focus("input", "number"),
    focus("input", "password"),
    focus("input", "radio"),
    focus("input", "range"),
    focus("textarea"),
    focus("select"),
    focus("span", undefined, true),
  ]) {
    assert.equal(ignoresKeys(target), true);
    assert.equal(keyAction({ key: "ArrowRight", target }), null);
  }
  for (const target of [
    focus("button"),
    focus("a"),
    focus("dialog"),
    focus('[role="dialog"]'),
    focus('[role="button"]'),
    ...["button", "submit", "reset", "image", "checkbox"].map(
      (type) => focus("input", type),
    ),
  ]) {
    assert.equal(ignoresKeys(target), false);
    assert.equal(keyAction({ key: "ArrowRight", target }), "next");
    assert.equal(keyAction({ key: "ArrowLeft", target }), "previous");
    assert.equal(keyAction({ key: " ", target }), null);
    assert.equal(keyAction({ key: "Enter", target }), null);
  }
});
test("keys respect modifiers and native continuous scrolling", () => {
  assert.equal(keyAction({ key: "ArrowRight" }), "next");
  assert.equal(keyAction({ key: "PageDown" }, "scroll"), null);
  assert.equal(keyAction({ key: "ArrowRight", ctrlKey: true }), null);
});
test("horizontal swipe only; vertical, pinch, zoom and OS edge gestures are untouched", () => {
  const start = { x: 200, y: 100, time: 0, touches: 1 };
  const end = { x: 90, y: 105, time: 150, touches: 1 };
  assert.equal(gestureAction(start, end), "next");
  assert.equal(gestureAction(start, { ...end, y: 300 }), null);
  assert.equal(gestureAction({ ...start, touches: 2 }, end), null);
  assert.equal(gestureAction({ ...start, x: 4 }, end), null);
  assert.equal(gestureAction(start, end, { zoom: 2 }), null);
  assert.equal(gestureAction(start, end, { mode: "scroll" }), null);
});
