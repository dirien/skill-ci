<script setup lang="ts">
// Local override of @pulumi/slidev-theme's PulumiFooter. The theme reads the
// page number from useNav().currentPage, which is the deck-wide current page:
// correct while presenting, but "1" on every page of a PDF/PNG export, where
// all slides render at once. $page comes from the slide's own context, so it
// is right in both. Everything else matches the theme's component.
import { useNav, useSlideContext } from "@slidev/client"

const { $page } = useSlideContext()
const { total } = useNav()
</script>

<template>
  <footer class="pulumi-footer">
    <div class="pulumi-footer-logo-wrap">
      <img
        class="pulumi-footer-logo pulumi-footer-logo-light"
        src="/logos/pulumi-logo-horizontal-color-light.svg"
        alt="Pulumi"
      />
      <img
        class="pulumi-footer-logo pulumi-footer-logo-dark"
        src="/logos/pulumi-logo-horizontal-color-dark.svg"
        alt="Pulumi"
      />
    </div>
    <span class="pulumi-footer-page">{{ $page }} / {{ total }}</span>
  </footer>
</template>

<style scoped>
.pulumi-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 1.25rem 3.5rem;
  flex-shrink: 0;
}

.pulumi-footer-logo {
  height: 1.5rem;
  width: auto;
}

.pulumi-footer-logo-light {
  display: var(--p-logo-light-display, block);
}

.pulumi-footer-logo-dark {
  display: var(--p-logo-dark-display, none);
}

.pulumi-footer-page {
  font-size: 0.7rem;
  color: var(--p-fg-subtle);
  font-variant-numeric: tabular-nums;
  letter-spacing: 0.02em;
}
</style>
