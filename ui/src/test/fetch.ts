// fixtureFetch is a fetch for tests that answers /api/v1/ from the fixtures.
import { fixtureFor } from "../api/fixtures/index.ts";

const PREFIX = "/api/v1";

export function fixtureFetch(input: RequestInfo | URL): Promise<Response> {
  const url = new URL(typeof input === "string" ? input : input instanceof URL ? input.href : input.url, "http://test.invalid");
  const path = url.pathname + url.search;
  if (!path.startsWith(PREFIX + "/")) {
    return Promise.resolve(new Response("not found", { status: 404 }));
  }
  const { status, body } = fixtureFor(path.slice(PREFIX.length));
  return Promise.resolve(
    new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json; charset=utf-8" } }),
  );
}
