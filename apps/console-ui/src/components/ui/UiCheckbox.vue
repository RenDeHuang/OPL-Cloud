<script setup lang="ts">
import { Check } from "@lucide/vue";
import { computed, useId } from "vue";

const model = defineModel<boolean>({ default: false });
const props = withDefaults(defineProps<{
  id?: string;
  label: string;
  description?: string;
  error?: string;
  disabled?: boolean;
}>(), {
  id: "",
  description: "",
  error: "",
  disabled: false
});

const generatedId = useId();
const controlId = computed(() => props.id || generatedId);
const descriptionId = computed(() => props.description ? `${controlId.value}-description` : undefined);
const errorId = computed(() => props.error ? `${controlId.value}-error` : undefined);
const describedBy = computed(() => [descriptionId.value, errorId.value].filter(Boolean).join(" ") || undefined);
</script>

<template>
  <label class="ui-checkbox" :data-disabled="disabled" :data-invalid="Boolean(error)">
    <input :id="controlId" v-model="model" class="ui-checkbox__input" type="checkbox" :disabled="disabled" :aria-invalid="Boolean(error) || undefined" :aria-describedby="describedBy" />
    <span class="ui-checkbox__box" aria-hidden="true"><Check v-if="model" :size="13" :stroke-width="3" /></span>
    <span class="ui-checkbox__text"><span>{{ label }}</span><span v-if="description" :id="descriptionId" class="ui-checkbox__description">{{ description }}</span><span v-if="error" :id="errorId" class="ui-field__error" role="alert">{{ error }}</span></span>
  </label>
</template>
