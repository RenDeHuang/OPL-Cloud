<script setup lang="ts">
import { computed, useId } from "vue";

defineOptions({ inheritAttrs: false });

const model = defineModel<string>({ default: "" });
const props = withDefaults(defineProps<{
  id?: string;
  label?: string;
  description?: string;
  error?: string;
  placeholder?: string;
  disabled?: boolean;
  required?: boolean;
  rows?: number;
}>(), {
  id: "",
  label: "",
  description: "",
  error: "",
  placeholder: "",
  disabled: false,
  required: false,
  rows: 4
});

const generatedId = useId();
const controlId = computed(() => props.id || generatedId);
const describedBy = computed(() => [props.description && `${controlId.value}-description`, props.error && `${controlId.value}-error`].filter(Boolean).join(" ") || undefined);
</script>

<template>
  <label class="ui-field" :for="controlId" :data-disabled="disabled">
    <span v-if="label" class="ui-field__label">{{ label }}</span>
    <textarea
      :id="controlId"
      v-model="model"
      v-bind="$attrs"
      class="ui-field__control"
      :rows="rows"
      :placeholder="placeholder"
      :disabled="disabled"
      :required="required"
      :aria-invalid="Boolean(error) || undefined"
      :aria-describedby="describedBy"
    />
    <p v-if="description" :id="`${controlId}-description`" class="ui-field__description">{{ description }}</p>
    <p v-if="error" :id="`${controlId}-error`" class="ui-field__error" role="alert">{{ error }}</p>
  </label>
</template>
