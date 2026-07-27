<script setup lang="ts">
import { LoaderCircle } from "@lucide/vue";

withDefaults(defineProps<{
  variant?: "spinner" | "dots" | "progress";
  size?: "sm" | "md" | "lg";
  label?: string;
  value?: number;
}>(), {
  variant: "spinner",
  size: "md",
  label: "正在加载",
  value: 0
});
</script>

<template>
  <span class="ui-indicator" :data-size="size" :data-variant="variant" role="status" :aria-label="label">
    <LoaderCircle v-if="variant === 'spinner'" class="ui-indicator__spinner" aria-hidden="true" />
    <span v-else-if="variant === 'dots'" class="ui-indicator__dots" aria-hidden="true"><span /><span /><span /></span>
    <progress v-else :value="Math.max(0, Math.min(100, value))" max="100">{{ value }}%</progress>
  </span>
</template>
