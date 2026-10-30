<script setup>
// A hand-drawn diagram rendered from diagrams/<name>.excalidraw by
// `npm run diagrams` (public/diagrams/<name>.svg and <name>-dark.svg).
// Both variants are in the page; the theme's logo variables show one of them.
defineProps({
  name: { type: String, required: true },
  alt: { type: String, default: '' },
})
</script>

<template>
  <div class="diagram">
    <img class="diagram__img diagram__img--light" :src="`/diagrams/${name}.svg`" :alt="alt" />
    <img class="diagram__img diagram__img--dark" :src="`/diagrams/${name}-dark.svg`" :alt="alt" />
  </div>
</template>

<style scoped>
.diagram { display: flex; align-items: center; justify-content: center; width: 100%; height: 100%; min-height: 0; }
.diagram__img { width: 100%; height: 100%; object-fit: contain; }
.diagram__img--light { display: var(--p-logo-light-display, block); }
.diagram__img--dark { display: var(--p-logo-dark-display, none); }
</style>
