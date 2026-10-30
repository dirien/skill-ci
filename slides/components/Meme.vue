<script setup>
// Full-bleed classic meme. The template fills the 16:9 slide and the
// captions are live text (Anton, white with a black outline), so they stay
// editable in slides.md. fit="contain" keeps the whole template and fills
// the letterbox with a blurred copy; use it for templates far from 16:9.
defineProps({
  src: { type: String, required: true },
  top: { type: String, default: '' },
  bottom: { type: String, default: '' },
  fit: { type: String, default: 'cover' },
  position: { type: String, default: 'center' },
  // Caption size in rem; long captions want ~5.
  size: { type: Number, default: 6.2 },
  // Labels placed on people in the picture (templates like "Floating Boy"):
  // [{ text, x, y, size? }] with x and y in percent of the slide.
  labels: { type: Array, default: () => [] },
})
</script>

<template>
  <div class="meme">
    <img v-if="fit === 'contain'" class="meme__backdrop" :src="src" alt="" />
    <img class="meme__img" :src="src" :style="{ objectFit: fit, objectPosition: position }" alt="" />
    <div v-if="top" class="meme__caption meme__caption--top" :style="{ fontSize: `${size}rem` }">{{ top }}</div>
    <div v-if="bottom" class="meme__caption meme__caption--bottom" :style="{ fontSize: `${size}rem` }">{{ bottom }}</div>
    <div
      v-for="label in labels"
      :key="label.text"
      class="meme__caption meme__label"
      :style="{ left: `${label.x}%`, top: `${label.y}%`, fontSize: `${label.size || size}rem` }"
    >{{ label.text }}</div>
  </div>
</template>

<style scoped>
.meme {
  position: absolute;
  inset: 0;
  overflow: hidden;
  background: #000;
  z-index: 10;
}
.meme__backdrop {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  object-fit: cover;
  /* Scale past the blur radius so the soft, darkened edges stay off-screen. */
  transform: scale(1.3);
  filter: blur(40px) brightness(0.5);
}
.meme__img {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
}
.meme__caption {
  position: absolute;
  left: 4%;
  right: 4%;
  text-align: center;
  font-family: 'Anton', Impact, 'Arial Narrow Bold', sans-serif;
  line-height: 1.08;
  letter-spacing: 0.01em;
  text-transform: uppercase;
  text-wrap: balance;
  color: #fff;
  text-shadow:
    0.05em 0 0 #000, -0.05em 0 0 #000, 0 0.05em 0 #000, 0 -0.05em 0 #000,
    0.035em 0.035em 0 #000, -0.035em 0.035em 0 #000, 0.035em -0.035em 0 #000, -0.035em -0.035em 0 #000;
}
.meme__caption--top { top: 3.5%; }
.meme__caption--bottom { bottom: 3.5%; }
.meme__label { left: auto; right: auto; white-space: nowrap; transform: translate(-50%, -50%); }
</style>
