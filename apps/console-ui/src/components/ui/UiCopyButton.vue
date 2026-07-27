<script setup lang="ts">
import { Check, Copy } from "@lucide/vue";
import { onBeforeUnmount, ref } from "vue";

import UiButton from "./UiButton.vue";

const props = withDefaults(defineProps<{
  value: string;
  label?: string;
  copiedLabel?: string;
  disabled?: boolean;
  duration?: number;
  variant?: "solid" | "soft" | "outline" | "ghost";
}>(), {
  label: "复制",
  copiedLabel: "已复制",
  disabled: false,
  duration: 1800,
  variant: "ghost"
});

const emit = defineEmits<{ copied: []; error: [error: unknown] }>();
const copied = ref(false);
let timer: number | undefined;

async function copyValue() {
  if (!props.value || props.disabled) return;
  try {
    await navigator.clipboard.writeText(props.value);
    copied.value = true;
    if (timer !== undefined) window.clearTimeout(timer);
    timer = window.setTimeout(() => { copied.value = false; }, props.duration);
    emit("copied");
  } catch (error) {
    emit("error", error);
  }
}

onBeforeUnmount(() => { if (timer !== undefined) window.clearTimeout(timer); });
</script>

<template>
  <UiButton :variant="variant" color="secondary" size="sm" :disabled="disabled || !value" @click="copyValue">
    <Check v-if="copied" :size="14" /><Copy v-else :size="14" />{{ copied ? copiedLabel : label }}
  </UiButton>
</template>
