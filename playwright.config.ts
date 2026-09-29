import { defineConfig, devices } from "@playwright/test";

// Fixed rather than ephemeral: Playwright has to know the URL before the
// server exists in order to wait for it.
//
// Not 8123, which this was: that is ClickHouse's HTTP port, and an ssh tunnel
// to a ClickHouse server held it. With reuseExistingServer on, the whole suite
// ran against ClickHouse's landing page and failed as if every selector had
// broken.
const port = Number(process.env.SPIREWEB_E2E_PORT ?? 18123);
const baseURL = `http://127.0.0.1:${port}`;

export default defineConfig({
  testDir: "e2e",
  fullyParallel: true,

  // A .only left in a commit passes locally and silently stops testing
  // everything else in CI.
  forbidOnly: !!process.env.CI,

  // No retries. These assert deterministic server output and one scroll; a
  // test that only passes on the second attempt is a bug worth seeing.
  retries: 0,

  reporter: process.env.CI ? "github" : "list",

  use: {
    ...devices["Desktop Chrome"],
    baseURL,
    // The reading pane's scroll position is the subject of one of the tests,
    // so the viewport has to be a known size rather than the runner's.
    viewport: { width: 1100, height: 700 },
    trace: "retain-on-failure",
  },

  webServer: {
    command: `./e2e/serve.sh ${port}`,
    // Readiness is judged by this URL answering, and so is "a server is
    // already running" when reuseExistingServer is on. / is answered by
    // anything listening on the port; app.js only by spireweb, so a stranger
    // on the port is not mistaken for it and serve.sh fails loudly on the
    // bind instead.
    url: `${baseURL}/static/app.js`,
    // Reuse a server already on the port while developing; never in CI, where
    // a stale one would mean testing the wrong binary.
    reuseExistingServer: !process.env.CI,
    stdout: "pipe",
    stderr: "pipe",
    // Cold, this compiles TypeScript, builds a cgo binary, and indexes the
    // fixtures.
    timeout: 180_000,
  },
});
