// The search behind the app's cmd-F bar. The bar is native (find.swift) and
// nothing here can drive it, but everything it shows comes from these calls:
// the count, which match is current, and where the pane scrolls.
import { expect, type Page, test } from "@playwright/test";

interface FindResult {
  current: number;
  total: number;
  more: boolean;
}

type Find = {
  search(q: string): FindResult;
  step(delta: number): FindResult;
};

const search = (page: Page, q: string) =>
  page.evaluate(
    (q) => (window as unknown as { brightlanternFind: Find }).brightlanternFind.search(q),
    q,
  );

const step = (page: Page, delta: number) =>
  page.evaluate(
    (d) => (window as unknown as { brightlanternFind: Find }).brightlanternFind.step(d),
    delta,
  );

// Whether the current match lies inside the reading pane's visible box.
const currentInView = (page: Page) =>
  page.evaluate(() => {
    const current = [...(CSS.highlights.get("find-current") ?? [])][0];
    const pane = document.querySelector(".reading");
    if (!(current instanceof Range) || !pane) return false;
    const r = current.getBoundingClientRect();
    const box = pane.getBoundingClientRect();
    return r.top >= box.top && r.bottom <= box.bottom;
  });

test("find matches the transcript, not the session list", async ({ page }) => {
  await page.goto("/sessions/e2e-charlie");
  // The list's row says "ingest" too; the bar used to count it.
  await expect(page.locator(".list")).toContainText("ingest");

  // The heading and the first message.
  expect(await search(page, "ingest")).toEqual({ current: 1, total: 2, more: false });

  const outside = await page.evaluate(() => {
    const pane = document.querySelector(".reading");
    const all = [
      ...(CSS.highlights.get("find") ?? []),
      ...(CSS.highlights.get("find-current") ?? []),
    ];
    return all.filter((r) => !pane?.contains((r as Range).startContainer)).length;
  });
  expect(outside).toBe(0);
});

test("find ignores case", async ({ page }) => {
  await page.goto("/sessions/e2e-charlie");
  const lower = await search(page, "stalls");
  expect(lower.total).toBeGreaterThan(1);
  expect(await search(page, "STALLS")).toEqual(lower);
});

test("find scrolls the reading pane to the match", async ({ page }) => {
  await page.goto("/sessions/e2e-charlie");
  // One occurrence, in the last of 41 messages.
  expect(await search(page, "quicksand")).toEqual({ current: 1, total: 1, more: false });

  const scrolled = await page.locator(".reading").evaluate((pane) => pane.scrollTop);
  expect(scrolled).toBeGreaterThan(0);
  expect(await currentInView(page)).toBe(true);
});

test("stepping says which match is current, wraps, and scrolls to it", async ({ page }) => {
  await page.goto("/sessions/e2e-charlie");
  const first = await search(page, "stalls");
  expect(first.current).toBe(1);

  expect((await step(page, 1)).current).toBe(2);
  await step(page, -1);
  // Back past the first wraps to the last, which is below the fold.
  expect(await step(page, -1)).toEqual({ ...first, current: first.total });
  expect(await currentInView(page)).toBe(true);

  expect((await step(page, 1)).current).toBe(1);
  expect(await currentInView(page)).toBe(true);
});

// WebKit repaints ranges added to or removed from a registered highlight, and
// not those of a highlight swapped into the registry: stepping between two
// matches on screen left the old one painted as current. Chromium repaints
// either way, so what this pins is the in-place change WebKit needs.
test("stepping changes the registered highlights in place", async ({ page }) => {
  await page.goto("/sessions/e2e-charlie");
  const { total } = await search(page, "stalls");

  await page.evaluate(() => {
    const w = window as unknown as { before: unknown[] };
    w.before = [CSS.highlights.get("find"), CSS.highlights.get("find-current")];
  });
  await step(page, 1);

  const state = await page.evaluate(() => {
    const w = window as unknown as { before: unknown[] };
    const all = CSS.highlights.get("find");
    const current = CSS.highlights.get("find-current");
    return {
      same: all === w.before[0] && current === w.before[1],
      all: all?.size,
      current: current?.size,
    };
  });
  expect(state).toEqual({ same: true, all: total - 1, current: 1 });
});

test("a collapsed tool call matches nothing, an opened one its output", async ({ page }) => {
  await page.goto("/sessions/e2e-bravo");
  const tool = page.locator("details.tool");
  // The summary line is clipped with an ellipsis, so what it holds is not
  // necessarily on screen; and the output is not even loaded yet.
  await expect(tool.locator("summary")).toContainText("oneline");
  expect((await search(page, "oneline")).total).toBe(0);
  expect((await search(page, "tighten the retry")).total).toBe(0);

  await tool.locator("summary").click();
  await expect(page.locator(".tool-output")).toContainText("tighten the retry");
  expect((await search(page, "tighten the retry")).total).toBe(1);
});

// What the app's cmd-F does.
const openBar = (page: Page) =>
  page.evaluate(() =>
    (window as unknown as { brightlanternFind: { open(): void } }).brightlanternFind.open(),
  );

test("the bar sits at the foot of the reading pane, as wide as it", async ({ page }) => {
  await page.goto("/sessions/e2e-charlie");
  const bar = page.locator(".findbar");
  await expect(bar).toBeHidden();

  await openBar(page);
  await expect(bar).toBeVisible();
  await expect(page.locator(".findbar-input")).toBeFocused();

  const reading = await page.locator(".reading").boundingBox();
  const box = await bar.boundingBox();
  const viewport = page.viewportSize();
  expect(box?.x).toBe(reading?.x);
  expect(box?.width).toBe(reading?.width);
  expect(Math.round((box?.y ?? 0) + (box?.height ?? 0))).toBe(viewport?.height);
  // Beneath the pane, not over its last lines.
  expect((reading?.y ?? 0) + (reading?.height ?? 0)).toBeLessThanOrEqual(box?.y ?? 0);
});

test("the bar counts as you type, steps on Return, closes on Escape", async ({ page }) => {
  await page.goto("/sessions/e2e-charlie");
  await openBar(page);
  const count = page.locator(".findbar-count");

  await page.keyboard.type("ingest");
  await expect(count).toHaveText("1 of 2 matches");
  await page.keyboard.press("Enter");
  await expect(count).toHaveText("2 of 2 matches");
  await page.keyboard.press("Shift+Enter");
  await expect(count).toHaveText("1 of 2 matches");

  await page.keyboard.type("zzz");
  await expect(count).toHaveText("Not found");
  await expect(page.locator(".findbar-next")).toBeDisabled();

  await page.keyboard.press("Escape");
  await expect(page.locator(".findbar")).toBeHidden();
  expect(await page.evaluate(() => CSS.highlights.get("find")?.size ?? 0)).toBe(0);
});

test("the bar stays open, with its query, in the next session opened", async ({ page }) => {
  await page.goto("/sessions/e2e-charlie");
  await openBar(page);
  await page.keyboard.type("stage");

  await page.locator('a.row[href="/sessions/e2e-bravo"]').click();
  await expect(page).toHaveURL(/e2e-bravo/);
  await expect(page.locator(".findbar")).toBeVisible();
  await expect(page.locator(".findbar-input")).toHaveValue("stage");
  // Counted again, in this session.
  await expect(page.locator(".findbar-count")).not.toHaveText("");

  // And Done is remembered too.
  await page.locator(".findbar-done").click();
  await page.locator('a.row[href="/sessions/e2e-charlie"]').click();
  await expect(page).toHaveURL(/e2e-charlie/);
  await expect(page.locator(".findbar")).toBeHidden();
});
