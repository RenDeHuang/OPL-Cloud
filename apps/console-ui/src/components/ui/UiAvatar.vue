<script setup lang="ts">
import { computed, ref } from "vue";

defineOptions({ inheritAttrs: false });

const props = withDefaults(defineProps<{
  name?: string;
  imageUrl?: string;
  overflowCount?: number;
  size?: number;
  color?: "primary" | "secondary" | "success" | "info" | "danger";
  variant?: "soft" | "solid";
  interactive?: boolean;
  label?: string;
}>(), {
  name: "",
  imageUrl: "",
  overflowCount: 0,
  size: 32,
  color: "secondary",
  variant: "soft",
  interactive: false,
  label: ""
});

const emit = defineEmits<{ click: [event: MouseEvent] }>();
const imageFailed = ref(false);
const initial = computed(() => props.name.trim().charAt(0).toLocaleUpperCase());
const overflowLabel = computed(() => `+${new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 0 }).format(props.overflowCount).toLocaleLowerCase()}`);
</script>

<template>
  <component
    :is="interactive ? 'button' : 'span'"
    v-bind="$attrs"
    class="ui-avatar"
    :type="interactive ? 'button' : undefined"
    :role="interactive ? undefined : 'presentation'"
    :aria-label="interactive ? (label || name) : undefined"
    :data-color="color"
    :data-variant="variant"
    :style="{ '--ui-avatar-size': `${size}px` }"
    @click="interactive && emit('click', $event)"
  >
    <img v-if="imageUrl && !imageFailed" :src="imageUrl" alt="" draggable="false" @error="imageFailed = true" />
    <slot v-else-if="$slots.icon" name="icon" />
    <span v-else-if="overflowCount" class="ui-avatar__overflow">{{ overflowLabel }}</span>
    <span v-else class="ui-avatar__initial">{{ initial }}</span>
  </component>
</template>
