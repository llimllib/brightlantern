// The second server: a copy of the fixtures that the tests write into and the
// server watches. Shared by the config, which starts it, and the tests, which
// have to know where to write; computed rather than passed, because a test
// worker cannot see what the config process decided.
//
// A separate server because an arrival is permanent: the index keeps a
// session whose file is gone, so one written here would show up as a fourth
// row in every smoke test that counts three.
import { tmpdir } from "node:os";
import { join } from "node:path";

export const watchedPort = Number(process.env.BRIGHTLANTERN_E2E_WATCHED_PORT ?? 18124);
export const watchedURL = `http://127.0.0.1:${watchedPort}`;
export const watchedDir = join(tmpdir(), `brightlantern-e2e-${watchedPort}`, "sessions");
