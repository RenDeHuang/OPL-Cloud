<script setup lang="ts">
withDefaults(defineProps<{
  variant?: "solid" | "soft" | "outline";
  color?: "primary" | "secondary" | "success" | "danger" | "warning" | "info";
  size?: "sm" | "md" | "lg";
  pill?: boolean;
  dot?: boolean;
}>(), {
  variant: "soft",
  color: "secondary",
  size: "md",
  pill: false,
  dot: false
});
</script>

<template>
  <span class="ui-badge" :data-variant="variant" :data-color="color" :data-size="size" :data-pill="pill">
    <span v-if="dot" class="ui-badge__dot" aria-hidden="true" />
    <slot />
  </span>
</template>
