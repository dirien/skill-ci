import { defineConfig } from 'vite'

export default defineConfig({
  slidev: {
    components: {
      // Slidev registers theme components before ./components, and the first
      // name wins by default. Allow overrides so components/PulumiFooter.vue
      // (export page-number fix) replaces the theme's footer.
      allowOverrides: true,
    },
  },
})
