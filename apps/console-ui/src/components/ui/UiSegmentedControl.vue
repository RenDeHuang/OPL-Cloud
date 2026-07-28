<script setup lang="ts">
import { ref } from "vue";

interface SegmentOption {
  value: string;
  label: string;
  disabled?: boolean;
}

const model = defineModel<string>({ required: true });
const props = withDefaults(defineProps<{
  options: SegmentOption[];
  label: string;
  block?: boolean;
}>(), {
  block: false
});

const root = ref<HTMLElement | null>(null);

function select(value: string, disabled = false) {
  if (!disabled) model.value = value;
}

function onKeydown(event: KeyboardEvent) {
  if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
  const enabled = props.options.filter((option) => !option.disabled);
  if (!enabled.length) return;
  event.preventDefault();
  const current = Math.max(0, enabled.findIndex((option) => option.value === model.value));
  const nextIndex = event.key === "Home"
    ? 0
    : event.key === "End"
      ? enabled.length - 1
      : (current + (event.key === "ArrowRight" ? 1 : -1) + enabled.length) % enabled.length;
  model.value = enabled[nextIndex].value;
  requestAnimationFrame(() => root.value?.querySelector<HTMLElement>(`[data-value="${CSS.escape(model.value)}"]`)?.focus());
}
</script>

<template>
  <div ref="root" class="ui-segmented" role="radiogroup" :aria-label="label" :data-block="block" @keydown="onKeydown">
    <button
      v-for="option in options"
      :key="option.value"
      class="ui-segmented__option"
      type="button"
      role="radio"
      :data-value="option.value"
      :aria-checked="model === option.value"
      :tabindex="model === option.value ? 0 : -1"
      :disabled="option.disabled"
      @click="select(option.value, option.disabled)"
    >{{ option.label }}</button>
  </div>
</template>
