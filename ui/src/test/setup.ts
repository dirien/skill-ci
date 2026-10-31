// Test setup: every test gets a fresh DOM and a fetch that answers from the fixtures.
import { cleanup } from "@testing-library/react";
import { afterEach, beforeEach, vi } from "vitest";

import { fixtureFetch } from "./fetch.ts";

beforeEach(() => {
  vi.stubGlobal("fetch", fixtureFetch);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  document.documentElement.removeAttribute("data-theme");
});
