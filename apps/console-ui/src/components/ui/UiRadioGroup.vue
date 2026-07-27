<script setup lang="ts">
import { computed, nextTick, ref } from "vue";

interface UiRadioOption {
  value: string | number;
  label: string;
  description?: string;
  disabled?: boolean;
}

const model = defineModel<string | number>({ default: "" });
const props = withDefaults(defineProps<{
  options: UiRadioOption[];
  label?: string;
  disabled?: boolean;
}>(), {
  label: "选项",
  disabled: false
});

const root = ref<HTMLElement | null>(null);
const tabStopValue = computed(() => {
  if (props.disabled) return null;
  const selected = props.options.find((option) => option.value === model.value && !option.disabled);
  return selected?.value ?? props.options.find((option) => !option.disabled)?.value ?? null;
});

function enabledItems() {
  return Array.from(root.value?.querySelectorAll<HTMLButtonElement>('[role="radio"]:not(:disabled)') || []);
}

async function selectAt(index: number) {
  const items = enabledItems();
  if (!items.length) return;
  const item = items[(index + items.length) % items.length];
  item.click();
  await nextTick();
  item.focus();
}

function onKeydown(event: KeyboardEvent) {
  const items = enabledItems();
  const current = items.indexOf(document.activeElement as HTMLButtonElement);
  if (event.key === "ArrowDown" || event.key === "ArrowRight") {
    event.preventDefault();
    void selectAt(current + 1);
  } else if (event.key === "ArrowUp" || event.key === "ArrowLeft") {
    event.preventDefault();
    void selectAt(current < 0 ? items.length - 1 : current - 1);
  } else if (event.key === "Home") {
    event.preventDefault();
    void selectAt(0);
  } else if (event.key === "End") {
    event.preventDefault();
    void selectAt(items.length - 1);
  }
}
</script>

<template>
  <div ref="root" class="ui-radio-group" role="radiogroup" :aria-label="props.label" :data-disabled="props.disabled" @keydown="onKeydown">
    <button
      v-for="option in props.options"
      :key="option.value"
      class="ui-radio-group__option"
      type="button"
      role="radio"
      :aria-checked="model === option.value"
      :tabindex="!props.disabled && !option.disabled && tabStopValue === option.value ? 0 : -1"
      :disabled="props.disabled || option.disabled"
      @click="model = option.value"
    >
      <span class="ui-radio-group__control" aria-hidden="true"><span /></span>
      <span class="ui-radio-group__copy"><strong>{{ option.label }}</strong><small v-if="option.description">{{ option.description }}</small></span>
    </button>
  </div>
</template>
