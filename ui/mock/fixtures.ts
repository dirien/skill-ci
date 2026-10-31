// A Vite dev server plugin that answers /api/v1/ from the fixtures, so the UI
// runs without a cluster: `npm run dev` (make ui-dev).
import type { Plugin } from "vite";

import { fixtureFor } from "../src/api/fixtures/index.ts";

const PREFIX = "/api/v1";

// Node's types are not installed (vite's own types skip them), so the request
// and response are narrowed to the members used here.
interface Req {
  url?: string;
  method?: string;
}

interface Res {
  statusCode: number;
  setHeader(name: string, value: string): void;
  end(body?: string): void;
}

/** fixtureMock serves GET /api/v1/* from src/api/fixtures. */
export function fixtureMock(): Plugin {
  return {
    name: "skill-ci-fixture-mock",
    configureServer(server) {
      server.middlewares.use((request, response, next) => {
        const req = request as unknown as Req;
        const res = response as unknown as Res;
        const url = req.url ?? "";
        if (!url.startsWith(PREFIX + "/")) {
          next();
          return;
        }
        const { status, body } =
          req.method === "GET" || req.method === "HEAD"
            ? fixtureFor(url.slice(PREFIX.length))
            : { status: 405, body: { error: { status: 405, code: "bad_request", message: "The API is read-only." } } };
        res.statusCode = status;
        res.setHeader("Content-Type", "application/json; charset=utf-8");
        res.setHeader("Cache-Control", "no-store");
        res.end(JSON.stringify(body));
      });
    },
  };
}
