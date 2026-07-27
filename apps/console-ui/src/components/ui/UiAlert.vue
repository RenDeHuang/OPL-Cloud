<script setup lang="ts">
import { CircleAlert, CircleCheck, Info, TriangleAlert, X } from "@lucide/vue";
import { computed } from "vue";

const props = withDefaults(defineProps<{
  color?: "info" | "success" | "warning" | "danger";
  title?: string;
  dismissible?: boolean;
}>(), {
  color: "info",
  title: "",
  dismissible: false
});

const emit = defineEmits<{ dismiss: [] }>();
const role = computed(() => props.color === "danger" || props.color === "warning" ? "alert" : "status");
const icon = computed(() => ({ info: Info, success: CircleCheck, warning: TriangleAlert, danger: CircleAlert })[props.color]);
</script>

<template>
  <section class="ui-alert" :data-color="color" :role="role">
    <span class="ui-alert__icon"><slot name="icon"><component :is="icon" :size="18" aria-hidden="true" /></slot></span>
    <div class="ui-alert__body"><strong v-if="title" class="ui-alert__title">{{ title }}</strong><slot /></div>
    <div v-if="$slots.action || dismissible" class="ui-alert__action">
      <slot name="action" />
      <button v-if="dismissible" class="ui-alert__dismiss" type="button" aria-label="关闭提示" @click="emit('dismiss')"><X :size="15" /></button>
    </div>
  </section>
</template>
