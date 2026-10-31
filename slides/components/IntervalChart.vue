<script setup>
// The version-over-version gate, drawn as 95% intervals of "candidate minus stable".
// Top row: the measured acme-release-notes v1.2.0 run on Claude Code (2026-10-31).
// Bottom row: the worked Inconclusive example in CONTRACT.md.
const lo = -0.4
const hi = 0.1
const w = 1000
const x = (v) => ((v - lo) / (hi - lo)) * w
const rows = [
  // side: where the numbers go, so they never cross the zero or tolerance lines
  { label: 'v1.2.0, measured', mean: -0.29, a: -0.31, b: -0.27, verdict: 'Regressed', kind: 'bad', side: 'right' },
  { label: 'v1.2.0, spread-out trials', mean: -0.08, a: -0.18, b: 0.03, verdict: 'Inconclusive', kind: 'warn', side: 'left' },
]
const ticks = [-0.4, -0.3, -0.2, -0.1, 0, 0.1]
const fmt = (v) => (v > 0 ? '+' : v < 0 ? '−' : '') + Math.abs(v).toFixed(2)
</script>

<template>
  <svg class="ci" viewBox="-140 0 1250 440" role="img" aria-label="Confidence intervals of the difference to the stable version">
    <rect :x="x(lo)" y="40" :width="x(-0.05) - x(lo)" height="290" class="ci__zone" />
    <line :x1="x(0)" y1="40" :x2="x(0)" y2="330" class="ci__zero" />
    <text :x="x(0)" y="26" class="ci__zero-label" text-anchor="middle">0 = same as stable</text>
    <line :x1="x(-0.05)" y1="40" :x2="x(-0.05)" y2="330" class="ci__tol" />
    <text :x="x(-0.05)" y="362" class="ci__tick" text-anchor="middle">tolerance −0.05</text>
    <g v-for="(r, i) in rows" :key="r.verdict" :transform="`translate(0 ${120 + i * 125})`">
      <line :x1="x(r.a)" y1="0" :x2="x(r.b)" y2="0" :class="['ci__bar', `ci__bar--${r.kind}`]" />
      <line :x1="x(r.a)" y1="-20" :x2="x(r.a)" y2="20" :class="['ci__cap', `ci__bar--${r.kind}`]" />
      <line :x1="x(r.b)" y1="-20" :x2="x(r.b)" y2="20" :class="['ci__cap', `ci__bar--${r.kind}`]" />
      <circle :cx="x(r.mean)" cy="0" r="12" :class="['ci__dot', `ci__fill--${r.kind}`]" />
      <text :x="x(r.mean)" y="-36" text-anchor="middle" :class="['ci__verdict', `ci__fill--${r.kind}`]">{{ r.verdict }}</text>
      <text
        :x="r.side === 'right' ? x(r.b) + 20 : x(r.a) - 20"
        y="9"
        :text-anchor="r.side === 'right' ? 'start' : 'end'"
        class="ci__nums"
      >{{ fmt(r.mean) }}, 95% CI [{{ fmt(r.a) }}, {{ fmt(r.b) }}]</text>
    </g>
    <line :x1="x(lo)" y1="386" :x2="x(hi)" y2="386" class="ci__axis" />
    <g v-for="t in ticks" :key="t">
      <line :x1="x(t)" y1="386" :x2="x(t)" y2="396" class="ci__axis" />
      <text :x="x(t)" y="426" text-anchor="middle" class="ci__tick">{{ fmt(t) }}</text>
    </g>
  </svg>
</template>

<style scoped>
.ci { width: 100%; height: auto; font-family: var(--slidev-font-mono); overflow: visible; }
.ci__zone { fill: color-mix(in srgb, #ee1721 9%, transparent); }
.ci__zero { stroke: var(--p-fg); stroke-width: 2; }
.ci__zero-label { fill: var(--p-fg-muted); font-size: 24px; }
.ci__tol { stroke: var(--p-fg-muted); stroke-width: 2; stroke-dasharray: 6 6; }
.ci__tick { fill: var(--p-fg-muted); font-size: 24px; }
.ci__axis { stroke: var(--p-fg-muted); stroke-width: 2; }
.ci__bar { stroke-width: 9; stroke-linecap: round; }
.ci__cap { stroke-width: 7; }
.ci__bar--bad { stroke: #bd141c; }
.ci__bar--warn { stroke: #b45309; }
.ci__fill--bad { fill: #bd141c; }
.ci__fill--warn { fill: #b45309; }
.ci__zero-label, .ci__tick, .ci__verdict, .ci__nums { paint-order: stroke; stroke: var(--p-bg); stroke-width: 8px; stroke-linejoin: round; }
.ci__dot { stroke: var(--p-bg); stroke-width: 3; }
.ci__verdict { font-size: 34px; font-weight: 700; }
.ci__nums { fill: var(--p-fg); font-size: 23px; }
:global(.dark) .ci__bar--bad { stroke: #ff9fa4; }
:global(.dark) .ci__fill--bad { fill: #ff9fa4; }
:global(.dark) .ci__bar--warn { stroke: #fbbf24; }
:global(.dark) .ci__fill--warn { fill: #fbbf24; }
</style>
