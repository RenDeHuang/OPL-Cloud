<script setup lang="ts">
import UiIndicator from "./UiIndicator.vue";

defineOptions({ inheritAttrs: false });

withDefaults(defineProps<{
  type?: "button" | "submit" | "reset";
  variant?: "solid" | "soft" | "outline" | "ghost";
  color?: "primary" | "secondary" | "success" | "danger" | "warning" | "info";
  size?: "sm" | "md" | "lg";
  block?: boolean;
  loading?: boolean;
  disabled?: boolean;
  loadingLabel?: string;
}>(), {
  type: "button",
  variant: "solid",
  color: "primary",
  size: "md",
  block: false,
  loading: false,
  disabled: false,
  loadingLabel: "处理中"
});
</script>

<template>
  <button
    v-bind="$attrs"
    class="ui-button"
    :type="type"
    :disabled="disabled || loading"
    :aria-busy="loading || undefined"
    :data-variant="variant"
    :data-color="color"
    :data-size="size"
    :data-block="block"
  >
    <UiIndicator v-if="loading" size="sm" :label="loadingLabel" />
    <span class="ui-button__content"><slot /></span>
  </button>
</template>
