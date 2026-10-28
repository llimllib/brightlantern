// Sessions indexed while a page is open go into its list (#95).
//
// Against the watched server, which these tests write into. Each test names
// its sessions uniquely and asserts order relative to the fixtures, not
// absolute positions: they run in parallel against one server, and a reused
// server while developing still holds every earlier run's arrivals.
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { expect, test, type Page } from "@playwright/test";
import { watchedDir, watchedURL } from "./watched";

test.use({ baseURL: watchedURL });

// The watcher settles for 2s and an idle page polls every 10s.
const arrives = { timeout: 25_000 };
test.setTimeout(60_000);

let n = 0;
function uniqueID(name: string): string {
  return `e2e-${name}-${Date.now()}-${process.pid}-${n++}`;
}

function writeSession(id: string, startedAt: string, text: string): void {
  const dir = join(watchedDir, "--Users-e2e-code-arrivals--");
  mkdirSync(dir, { recursive: true });
  const lines = [
    { type: "session", version: 3, id, timestamp: startedAt, cwd: "/Users/e2e/code/arrivals" },
    {
      type: "message",
      timestamp: startedAt,
      message: { role: "user", content: [{ type: "text", text }] },
    },
  ];
  writeFileSync(join(dir, `${id}.jsonl`), lines.map((l) => JSON.stringify(l)).join("\n") + "\n");
}

const row = (page: Page, id: string) => page.locator(`#rows > li[data-id="${id}"]`);

async function indexOfRow(page: Page, id: string): Promise<number> {
  return page
    .locator("#rows > li[data-id]")
    .evaluateAll((lis, want) => lis.findIndex((li) => (li as HTMLElement).dataset.id === want), id);
}

test("an arrival goes into the list where its date puts it", async ({ page }) => {
  await page.goto("/");
  await expect(row(page, "e2e-charlie")).toBeVisible();

  // Newer than everything, and one between charlie (Mar 3) and bravo (Mar 2):
  // a catch-up build finds files in any order, so arrivals are not always
  // the newest session.
  const newest = uniqueID("newest");
  const between = uniqueID("between");
  writeSession(newest, "2030-01-01T00:00:00.000Z", "arrived while the page was open");
  writeSession(between, "2026-03-02T12:00:00.000Z", "arrived out of order");

  await expect(row(page, newest)).toBeAttached(arrives);
  await expect(row(page, between)).toBeAttached(arrives);

  const charlie = await indexOfRow(page, "e2e-charlie");
  const at = await indexOfRow(page, between);
  expect(await indexOfRow(page, newest)).toBeLessThan(charlie);
  expect(at).toBeGreaterThan(charlie);
  expect(at).toBeLessThan(await indexOfRow(page, "e2e-bravo"));

  // Nobody was looking, so nothing was held: the list stayed at its top,
  // which is where the new rows went, and there is nothing out of sight to
  // point at.
  expect(await page.locator(".list").evaluate((pane) => pane.scrollTop)).toBe(0);
  await expect(page.locator(".arrivals-note")).toBeHidden();
});

test("an arrival does not move the row the cursor is on", async ({ page }) => {
  await page.goto("/");
  const rows = page.locator("a.row");
  await page.keyboard.press("j");
  const focused = rows.nth(1);
  await expect(focused).toBeFocused();
  const href = await focused.getAttribute("href");
  const before = await focused.boundingBox();

  const id = uniqueID("held");
  writeSession(id, "2031-01-01T00:00:00.000Z", "arrived above the cursor");
  await expect(row(page, id)).toBeAttached(arrives);

  // Same row, same focus, same place on screen; the new one is above it,
  // out of sight, and the note says so.
  const still = page.locator(`a.row[href="${href}"]`);
  await expect(still).toBeFocused();
  const after = await still.boundingBox();
  expect(after?.y).toBeCloseTo(before?.y ?? NaN, 0);
  // A sliver of a pixel can remain: row heights are fractional, scrollTop
  // is not.
  await expect(row(page, id)).not.toBeInViewport({ ratio: 0.01 });

  const note = page.locator(".arrivals-note button");
  await expect(note).toBeVisible();
  await expect(note).toHaveText(/^↑ \d+ new sessions?$/);

  // The note goes to them, and goes away once they are in sight.
  await note.click();
  await expect(row(page, id)).toBeInViewport();
  await expect(page.locator(".arrivals-note")).toBeHidden();
});

// The top row is not the one to hold: an arrival dated between it and the
// cursor would push the cursor's row down. This one lands just above bravo,
// with charlie on top and the cursor on bravo.
test("an arrival between the top row and the cursor does not move the cursor", async ({
  page,
}) => {
  await page.goto("/");
  await page.keyboard.press("j");
  const bravo = page.locator('a.row[href="/sessions/e2e-bravo"]');
  await expect(bravo).toBeFocused();
  const before = await bravo.boundingBox();

  const id = uniqueID("above-cursor");
  writeSession(id, "2026-03-02T18:00:00.000Z", "arrived between the top row and the cursor");
  await expect(row(page, id)).toBeAttached(arrives);

  expect(await indexOfRow(page, id)).toBeLessThan(await indexOfRow(page, "e2e-bravo"));
  await expect(bravo).toBeFocused();
  const after = await bravo.boundingBox();
  expect(after?.y).toBeCloseTo(before?.y ?? NaN, 0);
});

test("a search page counts arrivals instead of placing them", async ({ page }) => {
  await page.goto("/");
  await page.fill('input[name="q"]', "flexbox");
  const rows = page.locator("a.row");
  await expect(rows).toHaveCount(1);

  const id = uniqueID("searched");
  writeSession(id, "2032-01-01T00:00:00.000Z", "flexbox, arriving during a search");

  const note = page.locator(".arrivals-note button");
  await expect(note).toBeVisible(arrives);
  await expect(note).toHaveText(/^\d+ new sessions?$/);
  // Whether it belongs in these results is #96; for now it is not placed.
  await expect(row(page, id)).toHaveCount(0);

  await note.click();
  await expect(page).toHaveURL(`${watchedURL}/`);
  await expect(row(page, id)).toBeAttached();
});
