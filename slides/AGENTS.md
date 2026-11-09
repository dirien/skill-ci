# AGENTS.md: the deck's brief and sources

"Skills Are Packages Now. Your Platform Should Treat Them That Way."
KubeAuto Day North America 2026, Salt Lake City (co-located with KubeCon NA),
November 9, 2026, 25 minutes. Speaker: Engin Diri (Pulumi). This folder is the
Slidev deck; the project it talks about is the repo one level up
(`../README.md`, `../DEMO.md`, `../CONTRACT.md`).

## Where the deck comes from

- Setup copied from the workshop deck `pulumi/workshops`, commit `1e5db02`,
  folder `neo-in-a-docker-sandbox/slides/`: Slidev `^0.50.0`,
  `@pulumi/slidev-theme` 0.4.0 (pinned by `package-lock.json`), Inter and
  Monaspace Neon in `public/fonts/`, accent lines, Pulumi logos, `QRCode.vue`,
  and the card, caption, and info-card styles of `style.css`.
- Style (one short sentence per slide in very large type, classic memes as
  full-screen breaks) comes from the speaker's lightning talk
  `../../your-ai-agent-needs-more-than-a-pod/`: `.statement` slides, `Meme.vue`
  (captions are live text in Anton), the local `PulumiFooter.vue`.
- Pulumi brand: sentence-case headings, Pulumi terminology, no AI-generated
  brand imagery, no Pulumipus.

## Rules that bind

- Every number on a slide comes from `../README.md`, `../DEMO.md`,
  `../CONTRACT.md`, the repo history, or a cited paper. Anything unverified is a
  `TODO` in the speaker notes of that slide, never on the slide.
- Speaker notes on every slide, with a `[m:ss to m:ss]` window; the windows add
  up to 25:00.
- No "not X but Y", no rhetorical questions, no emoji bullets, no lorem ipsum.
- The scores on the slides, the demo video (`public/demo/demo.mp4`) and the
  backup screenshots (`public/fallback/`) all come from one cluster state:
  release 0.12.0, 2026-11-03, scored without Discoverability, the tables in
  `../DEMO.md` "The state before the talk". Change one, change all of them.
- No live cluster is assumed: the demo plays from the recording, and the step
  slides carry the same commands and numbers on their own if the video fails.

## Commands

```bash
npm ci
npm run dev                  # slidev slides.md --open
npx slidev slides.md --remote --port 3030   # reachable from outside the sandbox
npm run build                # static site in dist/
npm run export               # PDF
npm run diagrams             # re-render diagrams/*.excalidraw to public/diagrams/*.svg
```

## Diagrams

- Mermaid blocks in `slides.md` (event flow, SkillEval lifecycle) use the
  theme's Pulumi mermaid setup.
- Hand-drawn diagrams are Excalidraw scenes in `diagrams/*.excalidraw`, the
  source. `npm run diagrams` renders each to `public/diagrams/<name>.svg` and
  `<name>-dark.svg` with Excalidraw's own `exportToSvg` (`@excalidraw/excalidraw`
  0.18.1, pinned) in headless Chromium, with the Excalifont subset embedded, so
  the deck needs no network. The `<Diagram name="..." />` component shows the
  variant that matches the theme. To edit: open the scene at
  https://excalidraw.com, save it over the file, run `npm run diagrams`.
- Chromium for the script is the one from `playwright-chromium`, or set
  `CHROMIUM_PATH`.
- `slidev-addon-excalidraw` 1.1.1 was checked and not used: it loads Excalidraw
  from esm.sh at view time, which fails on conference wifi.

## Gotchas

- The dev server keeps stale scoped styles after slides move or a `<style
  scoped>` block changes: restart it before judging a screenshot. Stop it by
  the PID of `node_modules/.bin/slidev`; `pkill -f` with the command line also
  kills the shell that runs it.
- Class names can collide with UnoCSS utilities (`h4` is `height: 1rem`):
  prefix custom classes per slide (`hres__row`, `reg__row`).
- A full-canvas image covers the top bar and footer with `position: fixed;
  left: 0; top: 0; width: 1920px; height: 1080px; z-index: 20` (slide 1 and the
  backup slides). `object-fit: contain` keeps the aspect ratio.
- A two-column grid whose right column starts hidden (`v-click`) resizes on
  the first click; use `grid-cols-[minmax(0,…fr)_minmax(0,…fr)]`.
