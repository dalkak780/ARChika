import React from "react";
import { test } from "vite-plus/test";
import assert from "node:assert/strict";
import { renderToStaticMarkup } from "react-dom/server";
import { jobActions, Result } from "./admin.tsx";

test("queue actions match each job state", () => {
  assert.deepEqual(jobActions("queued"), ["pause", "cancel"]);
  assert.deepEqual(jobActions("running"), ["pause", "cancel"]);
  assert.deepEqual(jobActions("paused"), ["resume", "cancel"]);
  assert.deepEqual(jobActions("failed"), ["retry"]);
  assert.deepEqual(jobActions("cancelled"), ["retry"]);
  assert.deepEqual(jobActions("completed"), []);
  assert.deepEqual(jobActions("unknown"), []);
});

test("job results explain zero counts and queued jobs, preserving unknown details", () => {
  assert.match(
    renderToStaticMarkup(<Result value={{ count: 0 }} job />),
    /This job would cover 0 items/,
  );
  assert.match(renderToStaticMarkup(<Result value={{ id: "job-1" }} job />), /Job queued/);
  assert.match(renderToStaticMarkup(<Result value={{ ok: true }} />), /Saved/);
  const unknown = renderToStaticMarkup(<Result value={{ findings: ["Review relocation"] }} job />);
  assert.match(unknown, /<pre/);
  assert.match(unknown, /Review relocation/);
  assert.match(
    renderToStaticMarkup(<Result value={{ count: 2, blocked: ["missing root"] }} />),
    /missing root/,
  );
});
