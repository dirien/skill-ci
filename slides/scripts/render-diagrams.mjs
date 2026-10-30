// Renders every diagrams/*.excalidraw scene to public/diagrams/<name>.svg
// (light) and <name>-dark.svg with Excalidraw's own exportToSvg, offline.
//
//   npm run diagrams
//
// The scenes in diagrams/ are the source: open one at https://excalidraw.com
// (Open, or drag the file in), edit, save it over the file, run this script.
// The SVGs embed Excalifont as base64, so the deck needs no network.
// Chromium: playwright-chromium's own download, or CHROMIUM_PATH.
import { build } from "vite"
import { chromium } from "playwright-chromium"
import { createServer } from "node:http"
import { readFile, readdir, writeFile, mkdir, mkdtemp, rm } from "node:fs/promises"
import { existsSync } from "node:fs"
import { tmpdir } from "node:os"
import { dirname, extname, join, resolve } from "node:path"
import { fileURLToPath } from "node:url"

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..")
const scenesDir = join(root, "diagrams")
const outDir = join(root, "public", "diagrams")
const assets = join(root, "node_modules/@excalidraw/excalidraw/dist/prod")

const bundleDir = await mkdtemp(join(tmpdir(), "excalidraw-bundle-"))
await build({
  root,
  logLevel: "error",
  configFile: false,
  define: { "process.env.NODE_ENV": '"production"', "process.env.IS_PREACT": '"false"' },
  build: {
    outDir: bundleDir,
    emptyOutDir: true,
    minify: true,
    lib: { entry: join(root, "scripts/excalidraw-entry.js"), formats: ["iife"], name: "ExcalidrawBundle", fileName: () => "bundle.js" },
  },
})

const types = { ".js": "text/javascript", ".css": "text/css", ".woff2": "font/woff2", ".html": "text/html" }
const server = createServer(async (req, res) => {
  const url = new URL(req.url, "http://localhost")
  const file = url.pathname.startsWith("/assets/") ? join(assets, url.pathname.slice(8)) : join(bundleDir, url.pathname === "/" ? "index.html" : url.pathname)
  try {
    res.setHeader("content-type", types[extname(file)] ?? "application/octet-stream")
    res.end(await readFile(file))
  } catch {
    res.statusCode = 404
    res.end()
  }
})
await new Promise((ok) => server.listen(0, "127.0.0.1", ok))
const origin = `http://127.0.0.1:${server.address().port}`
await writeFile(
  join(bundleDir, "index.html"),
  `<!doctype html><meta charset="utf-8"><script>window.EXCALIDRAW_ASSET_PATH="${origin}/assets/"</script><script src="/bundle.js"></script><body></body>`,
)

const executablePath = process.env.CHROMIUM_PATH || undefined
const browser = await chromium.launch({ executablePath, args: ["--no-sandbox"] })
try {
  const page = await browser.newPage()
  page.on("pageerror", (e) => console.error("page error:", e.message))
  await page.goto(origin)
  await page.waitForFunction(() => window.ExcalidrawExport)
  await mkdir(outDir, { recursive: true })

  const scenes = (await readdir(scenesDir)).filter((f) => f.endsWith(".excalidraw"))
  for (const file of scenes) {
    const scene = JSON.parse(await readFile(join(scenesDir, file), "utf8"))
    const name = file.replace(/\.excalidraw$/, "")
    for (const dark of [false, true]) {
      const svg = await page.evaluate(async ({ scene, dark }) => {
        const el = await window.ExcalidrawExport.exportToSvg({
          elements: scene.elements.filter((e) => !e.isDeleted),
          appState: { ...scene.appState, exportWithDarkMode: dark, exportBackground: false, exportPadding: 24 },
          files: scene.files ?? null,
        })
        return new XMLSerializer().serializeToString(el)
      }, { scene, dark })
      const target = join(outDir, `${name}${dark ? "-dark" : ""}.svg`)
      await writeFile(target, svg)
      console.log(`${file} -> public/diagrams/${name}${dark ? "-dark" : ""}.svg (${Math.round(svg.length / 1024)} KB)`)
    }
  }
} finally {
  await browser.close()
  server.close()
  await rm(bundleDir, { recursive: true, force: true })
}
