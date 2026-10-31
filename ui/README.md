# skill-ci reporting UI

The single page app of the controller's reporting site (CONTRACT.md, "Reporting site"). It is built into `controller/internal/reportsite/web/dist/`, which the controller embeds with `go:embed` and serves on port 8082 next to the JSON API under `/api/v1/`.

## Commands

Run them from the repository root; Node is the release in `.nvmrc`.

| Command | What it does |
| --- | --- |
| `make ui-install` | `npm ci` from `package-lock.json` |
| `make ui-test` | eslint, `tsc --noEmit` and vitest |
| `make ui-build` | Production build into `controller/internal/reportsite/web/dist/` (generated, gitignored) |
| `make ui-dev` | Dev server on <http://localhost:5173> that answers `/api/v1/` from `src/api/fixtures/` |
| `make ui-dev SKILLCI_API=http://localhost:8082` | Dev server that proxies `/api` and the stored report files to a controller (`make reports-ui` port-forwards one) |

Without a build the controller serves `web/ui-placeholder.html`, so `go build` and `go test` never need Node.

## Layout

| Path | Content |
| --- | --- |
| `src/api/types.ts` | The API types, a hand-written mirror of `controller/internal/reportsite/apiv1` (the Go types are the source) |
| `src/api/client.ts` | Typed GET requests, one function per endpoint |
| `src/api/fixtures/` | API responses the UI is built and tested against, and `fixtureFor`, which maps a request path to one. The Go test `TestFixturesMatchTypes` decodes each file into its Go type, so a fixture is always a valid response |
| `mock/fixtures.ts` | The Vite plugin that serves the fixtures in `make ui-dev` |
| `src/routes.tsx` | The routes and their breadcrumbs |
| `src/components/`, `src/pages/` | The app shell and one component per page |
| `src/styles/tokens.css` | Design tokens: light, dark (`prefers-color-scheme`) and the explicit toggle (`data-theme` on `<html>`) |
| `src/test/` | Test setup: a `fetch` that answers from the fixtures |

## Rules

- The controller's CSP is `default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'`. No inline scripts, no `<style>` tags, no CDN, no web fonts. Dynamic styles go through React's `style` prop (the DOM), never a `style` attribute in static markup.
- Agent output is untrusted. Render it as text, never as HTML: eslint forbids `dangerouslySetInnerHTML`.
- `/runs/{name}/report`, `/runs/{name}/result.json` and the `/api/v1/.../output/{k}` and `trajectory.json` downloads are server responses, not app routes: link to them with a full page load (`<a href>` or `reloadDocument`).
- Every `localStorage` access is wrapped in `try`/`catch`; the app works without storage.
- Status never relies on color alone: an icon and a word go with it.

## Pinned versions

Every dependency is pinned exactly (`save-exact` in `.npmrc`), and every version was published at least 14 days before it was chosen on 2026-10-31 (`npm view <package> time --json`). The lockfile was resolved with `npm install --before=2026-10-17T00:00:00Z`, so the transitive dependencies (211 entries) meet the same rule. To update, pick versions that are at least 14 days old and resolve again with `--before` set to 14 days ago.

| Package | Version | Published | Note |
| --- | --- | --- | --- |
| Node | 24.22.0 | 2026-10-14 | LTS line 24 (Krypton). 24.22.1 (2026-10-20) was too new |
| react | 19.3.1 | 2026-10-14 | |
| react-dom | 19.3.1 | 2026-10-14 | |
| react-router | 8.4.0 | 2026-09-15 | 8.5.1 (2026-10-27) was too new |
| vite | 8.3.4 | 2026-10-08 | 8.3.5 (2026-10-21) was too new |
| @vitejs/plugin-react | 6.1.2 | 2026-10-05 | |
| typescript | 6.0.3 | 2026-04-16 | 7.0.2 is older than 14 days but typescript-eslint 8.71.2 supports TypeScript below 6.1 only |
| vitest | 5.0.5 | 2026-10-15 | |
| jsdom | 30.1.3 | 2026-10-15 | Needs Node 22.22.2, 24.15.0 or later |
| @testing-library/react | 16.3.3 | 2026-08-27 | |
| @testing-library/dom | 10.4.2 | 2026-09-13 | Peer dependency of @testing-library/react |
| eslint | 10.13.0 | 2026-10-16 | 10.14.0 (2026-10-30) was too new |
| @eslint/js | 10.0.1 | 2026-02-06 | The recommended rules of eslint |
| typescript-eslint | 8.71.2 | 2026-10-12 | 8.72.1 (2026-10-26) was too new |
| @types/react | 19.3.0 | 2026-09-09 | |
| @types/react-dom | 19.3.0 | 2026-09-09 | |

CI sets up Node with `actions/setup-node` v7.1.0 (published 2026-10-08), pinned by commit `949feb2413d6458794dcd2491c4babbbce0c15c1` like the other actions.
