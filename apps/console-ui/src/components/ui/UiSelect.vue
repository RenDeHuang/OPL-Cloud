<script setup lang="ts">
import { ChevronDown } from "@lucide/vue";
import { computed, useId } from "vue";

defineOptions({ inheritAttrs: false });

interface UiSelectOption {
  value: string | number;
  label: string;
  disabled?: boolean;
}

const model = defineModel<string | number>({ default: "" });
const props = withDefaults(defineProps<{
  id?: string;
  label?: string;
  description?: string;
  error?: string;
  disabled?: boolean;
  required?: boolean;
  options?: UiSelectOption[];
}>(), {
  id: "",
  label: "",
  description: "",
  error: "",
  disabled: false,
  required: false,
  options: () => []
});

const generatedId = useId();
const controlId = computed(() => props.id || generatedId);
const descriptionId = computed(() => props.description ? `${controlId.value}-description` : undefined);
const errorId = computed(() => props.error ? `${controlId.value}-error` : undefined);
const describedBy = computed(() => [descriptionId.value, errorId.value].filter(Boolean).join(" ") || undefined);
</script>

<template>
  <label class="ui-field" :for="controlId" :data-disabled="disabled">
    <span v-if="label" class="ui-field__label">{{ label }}</span>
    <span class="ui-field__frame">
      <select
        :id="controlId"
        v-model="model"
        v-bind="$attrs"
        class="ui-select__control"
        :disabled="disabled"
        :required="required"
        :aria-invalid="Boolean(error) || undefined"
        :aria-describedby="describedBy"
      >
        <slot><option v-for="option in options" :key="option.value" :value="option.value" :disabled="option.disabled">{{ option.label }}</option></slot>
      </select>
      <ChevronDown class="ui-select__icon" :size="16" aria-hidden="true" />
    </span>
    <p v-if="description" :id="descriptionId" class="ui-field__description">{{ description }}</p>
    <p v-if="error" :id="errorId" class="ui-field__error" role="alert">{{ error }}</p>
  </label>
</template>
