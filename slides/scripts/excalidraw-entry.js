// Bundled by render-diagrams.mjs into an IIFE and loaded in headless Chromium.
// It exposes the two Excalidraw functions the script needs, so the export uses
// the same renderer as excalidraw.com (rough.js strokes, Excalifont) offline.
import { exportToSvg, convertToExcalidrawElements } from "@excalidraw/excalidraw"

window.ExcalidrawExport = { exportToSvg, convertToExcalidrawElements }
