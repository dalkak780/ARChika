import assert from "node:assert/strict";
import { execFileSync, spawn } from "node:child_process";
import { mkdtemp, mkdir, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { chromium } from "playwright";

const directory = await mkdtemp(join(tmpdir(), "archika-e2e-"));
const baseURL = process.env.ARCHIKA_E2E_URL || "http://127.0.0.1:18091";
const password = "local-e2e-password-only";
const browser = await chromium.launch();
const context = await browser.newContext({
  locale: "en-US",
  viewport: { width: 1280, height: 900 },
});
const page = await context.newPage();
const errors = [];
page.on("pageerror", (error) => errors.push(error.message));
page.on("dialog", (dialog) => dialog.accept());
let server;
let serverOutput = "";
const passed = [];
const pass = (name) => {
  passed.push(name);
  console.log(`PASS ${name}`);
};
const visible = (locator) => locator.waitFor({ state: "visible" });
const snapshots = {};
const capture = async (name) => {
  if (!process.env.ARCHIKA_E2E_LAYOUT) return;
  await page.addStyleTag({
    content: "*, *::before, *::after { transition: none !important; animation: none !important; }",
  });
  await page.evaluate(() => document.fonts.ready);
  snapshots[name] = await page.evaluate(() =>
    [
      ...document.querySelectorAll(
        "main, header, h1, h3, .work-card, .reader-image, .reader-toolbar, button, select",
      ),
    ].map((element) => {
      const box = element.getBoundingClientRect();
      const style = getComputedStyle(element);
      return {
        tag: element.tagName,
        class: element.className,
        label: element.getAttribute("aria-label"),
        text: element.textContent.trim(),
        box: [box.x, box.y, box.width, box.height].map((value) => Math.round(value * 100) / 100),
        display: style.display,
        visibility: style.visibility,
        color: style.color,
        background: style.backgroundColor,
        font: style.font,
        overflow: style.overflow,
      };
    }),
  );
};
const imagesReady = () =>
  page.waitForFunction(() => {
    const images = [...document.querySelectorAll(".reader-image img")];
    return images.length > 0 && images.every((image) => image.complete && image.naturalWidth > 0);
  });
const search = async (query) => {
  await page.getByRole("searchbox").fill(query);
  await page.getByRole("button", { name: "Search", exact: true }).click();
  await page.waitForURL((url) => url.searchParams.get("q") === query);
};

try {
  for (const title of ["Artist Book", "Other Book"]) {
    const gallery = join(directory, "library", "Collection", title);
    await mkdir(gallery, { recursive: true });
    for (let index = 1; index <= 19; index++) {
      const png = await page.evaluate((number) => {
        const canvas = document.createElement("canvas");
        canvas.width = number === 3 ? 960 : 480;
        canvas.height = 720;
        const drawing = canvas.getContext("2d");
        drawing.fillStyle = number % 2 ? "#f5b942" : "#4287f5";
        drawing.fillRect(0, 0, canvas.width, canvas.height);
        drawing.fillStyle = "#111111";
        drawing.font = "80px sans-serif";
        drawing.fillText(String(number), 40, 120);
        return canvas.toDataURL("image/png").split(",")[1];
      }, index);
      await writeFile(
        join(gallery, `${String(index).padStart(2, "0")}.png`),
        Buffer.from(png, "base64"),
      );
    }
  }
  const library = join(directory, "library");
  const config = join(directory, "config.json");
  await writeFile(
    config,
    JSON.stringify({
      listen: process.env.ARCHIKA_E2E_IMAGE
        ? `0.0.0.0:${new URL(baseURL).port}`
        : new URL(baseURL).host,
      data_dir: join(directory, "data"),
      cache_dir: join(directory, "cache"),
      allowed_paths: [library],
      roots: [{ key: "e2e", name: "E2E Library", path: library, quiet_seconds: 1 }],
      quiet_seconds: 1,
    }),
  );
  const image = process.env.ARCHIKA_E2E_IMAGE;
  const executable = join(directory, "archika");
  if (!image)
    execFileSync("go", ["build", "-o", executable, "./cmd/archika"], {
      cwd: resolve(".."),
      stdio: "pipe",
    });
  server = spawn(
    image ? "docker" : executable,
    image
      ? [
          "run",
          "--rm",
          "--read-only",
          "--cap-drop=ALL",
          "--security-opt=no-new-privileges",
          "--user",
          `${process.getuid()}:${process.getgid()}`,
          "--tmpfs",
          "/tmp:rw,nosuid,nodev",
          "--publish",
          `127.0.0.1:${new URL(baseURL).port}:${new URL(baseURL).port}`,
          "--volume",
          `${directory}:${directory}`,
          "--volume",
          `${library}:${library}:ro`,
          "--env",
          "ARCHIKA_INITIAL_ADMIN_PASSWORD",
          image,
          "serve",
          "--config",
          config,
        ]
      : ["serve", "--config", config],
    {
      env: { ...process.env, ARCHIKA_INITIAL_ADMIN_PASSWORD: password },
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  for (const stream of [server.stdout, server.stderr])
    stream.on("data", (chunk) => {
      serverOutput += chunk;
    });
  for (let attempt = 0; ; attempt++) {
    if (server.exitCode !== null) throw new Error(`E2E server exited: ${serverOutput}`);
    if (
      await fetch(`${baseURL}/healthz`)
        .then((response) => response.ok)
        .catch(() => false)
    )
      break;
    if (attempt === 100) throw new Error("E2E server readiness timed out");
    await new Promise((done) => setTimeout(done, 100));
  }
  await page.goto(baseURL);
  await page.getByLabel("Username").fill("admin");
  await page.getByLabel("Password", { exact: true }).fill("wrong-password");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await visible(page.getByRole("alert"));
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await visible(page.getByRole("heading", { name: "Library", exact: true }));
  assert.equal(await page.locator(".work-card").count(), 0);
  pass("login rejection, login success, no startup scan");

  await page.getByRole("link", { name: "Administration", exact: true }).click();
  await page.getByRole("link", { name: "Jobs", exact: true }).click();
  await page.getByRole("button", { name: "Enqueue job", exact: true }).click();
  await page.waitForFunction(
    () =>
      [...document.querySelectorAll("tbody tr")].some((row) =>
        row.textContent.includes("completed"),
      ),
    { timeout: 30000 },
  );
  await page.getByRole("link", { name: "Library", exact: true }).click();
  await page.waitForFunction(() => document.querySelectorAll(".work-card").length === 2);
  pass("explicit discovery and catalog rendering");

  await search("Artist");
  await page.waitForFunction(() => document.querySelectorAll(".work-card").length === 1);
  await page.getByRole("button", { name: "Add favorite: Artist Book", exact: true }).click();
  await visible(page.getByRole("button", { name: "Remove favorite: Artist Book", exact: true }));
  await search("favorite:true");
  await visible(page.getByRole("heading", { name: "Artist Book", exact: true }));
  await search("Artist");
  await page.getByLabel(/^Sort/).selectOption("title");
  await page.waitForURL((url) => url.searchParams.get("sort") === "title");
  const origin = page.url();
  pass("search, controlled select, favorite mutation and cache refresh");
  await capture("desktop-library");

  await page.locator(".work-link").click();
  await imagesReady();
  await page.getByRole("button", { name: "Reader options (O)", exact: true }).click();
  await page.getByLabel(/^Layout/).selectOption("spread");
  await page.getByRole("button", { name: "Close", exact: true }).click();
  await page.waitForFunction(() => document.querySelectorAll(".reader-image img").length === 2);
  await imagesReady();
  await page.getByRole("button", { name: "Next page", exact: true }).click();
  await page.waitForURL((url) => url.searchParams.get("page") === "3");
  await imagesReady();
  assert.equal(await page.locator(".reader-image img").count(), 1);
  pass("reader images, saved spread preference, wide-page boundary");
  await capture("desktop-reader-wide");

  await page.getByRole("button", { name: "Page overview (Q)", exact: true }).click();
  await page.getByRole("button", { name: "Go to page 5", exact: true }).click();
  await page.waitForURL((url) => url.searchParams.get("page") === "5");
  await imagesReady();
  // Exercise the in-page fullscreen fallback, including two-step browser Back.
  await page.evaluate(() => {
    Element.prototype.requestFullscreen = () => Promise.reject(new Error("E2E fallback"));
  });
  await page.getByRole("button", { name: "Fullscreen (F)", exact: true }).click();
  await page.waitForURL((url) => url.searchParams.get("immersive") === "true");
  await page.goBack();
  await page.waitForURL(
    (url) => url.searchParams.get("immersive") === "false" && url.searchParams.get("page") === "5",
  );
  await page.goBack();
  await page.waitForURL(origin);
  pass("virtual overview jump and immersive two-step Back preserving filters");

  await page.getByRole("button", { name: "Folders", exact: true }).click();
  await visible(page.locator(".folder-card").filter({ hasText: "Collection" }));
  await page.locator(".folder-card").filter({ hasText: "Collection" }).click();
  await visible(page.getByRole("heading", { name: "Artist Book", exact: true }));
  pass("folder navigation");

  await page.getByRole("link", { name: "admin", exact: true }).click();
  await page.getByLabel(/^Language/).selectOption("ko");
  await visible(page.getByRole("heading", { name: "계정", exact: true }));
  await page.reload();
  await visible(page.getByRole("heading", { name: "계정", exact: true }));
  await page.locator("#language").selectOption("en");
  await visible(page.getByRole("heading", { name: "Account", exact: true }));
  pass("language mutation, reactive translation and reload persistence");

  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("link", { name: "Library", exact: true }).click();
  await visible(page.getByRole("heading", { name: "Artist Book", exact: true }));
  await page.locator(".work-link").filter({ hasText: "Artist Book" }).click();
  await imagesReady();
  await page.getByRole("button", { name: "Reader options (O)", exact: true }).click();
  await page.getByLabel(/^Mode/).selectOption("scroll");
  await page.getByRole("button", { name: "Close", exact: true }).click();
  await imagesReady();
  await page.waitForFunction(() => document.querySelectorAll(".reader-image img").length < 19);
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  await capture("mobile-continuous-reader");
  if (process.env.ARCHIKA_E2E_SCREENSHOT)
    await page.screenshot({ path: process.env.ARCHIKA_E2E_SCREENSHOT });
  pass("mobile continuous reader virtualization and no horizontal overflow");

  await page.getByRole("button", { name: "Close reader", exact: true }).click();
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await visible(page.getByRole("button", { name: "Sign in", exact: true }));
  assert.deepEqual(errors, []);
  pass("logout and no uncaught browser errors");
  console.log(`${passed.length} E2E scenarios passed`);
  if (process.env.ARCHIKA_E2E_LAYOUT)
    await writeFile(process.env.ARCHIKA_E2E_LAYOUT, JSON.stringify(snapshots, null, 2));
} finally {
  await browser.close();
  if (server && server.exitCode === null) {
    server.kill("SIGTERM");
    await new Promise((done) => server.once("exit", done));
  }
  await rm(directory, { recursive: true, force: true });
}
