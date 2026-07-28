<script setup lang="ts">
import { ref, useId } from "vue";

withDefaults(defineProps<{
  text: string;
  disabled?: boolean;
}>(), {
  disabled: false
});

const visible = ref(false);
const tooltipId = useId();
</script>

<template>
  <span
    class="ui-tooltip-root"
    :aria-describedby="visible && !disabled ? tooltipId : undefined"
    @mouseenter="visible = true"
    @mouseleave="visible = false"
    @focusin="visible = true"
    @focusout="visible = false"
    @keydown.esc="visible = false"
  >
    <slot />
    <Transition name="ui-popover-fade"><span v-if="visible && !disabled" :id="tooltipId" class="ui-tooltip" role="tooltip">{{ text }}</span></Transition>
  </span>
</template>
