<script setup lang="ts">
import { computed, useId } from "vue";

defineOptions({ inheritAttrs: false });

const model = defineModel<string | number>({ default: "" });
const props = withDefaults(defineProps<{
  id?: string;
  label?: string;
  description?: string;
  error?: string;
  optional?: boolean;
  type?: string;
  placeholder?: string;
  disabled?: boolean;
  required?: boolean;
  autocomplete?: string;
}>(), {
  id: "",
  label: "",
  description: "",
  error: "",
  optional: false,
  type: "text",
  placeholder: "",
  disabled: false,
  required: false,
  autocomplete: "off"
});

const generatedId = useId();
const controlId = computed(() => props.id || generatedId);
const descriptionId = computed(() => props.description ? `${controlId.value}-description` : undefined);
const errorId = computed(() => props.error ? `${controlId.value}-error` : undefined);
const describedBy = computed(() => [descriptionId.value, errorId.value].filter(Boolean).join(" ") || undefined);
</script>

<template>
  <label class="ui-field" :for="controlId" :data-disabled="disabled">
    <span v-if="label" class="ui-field__label"><span>{{ label }}</span><span v-if="optional" class="ui-field__optional">可选</span></span>
    <span class="ui-field__frame">
      <input
        :id="controlId"
        v-model="model"
        v-bind="$attrs"
        class="ui-field__control"
        :type="type"
        :placeholder="placeholder"
        :disabled="disabled"
        :required="required"
        :autocomplete="autocomplete"
        :aria-invalid="Boolean(error) || undefined"
        :aria-describedby="describedBy"
      />
    </span>
    <p v-if="description" :id="descriptionId" class="ui-field__description">{{ description }}</p>
    <p v-if="error" :id="errorId" class="ui-field__error" role="alert">{{ error }}</p>
  </label>
</template>
