<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from "vue";

const open = defineModel<boolean>({ default: false });
withDefaults(defineProps<{
  position?: "bottom-start" | "bottom-end" | "top";
  label?: string;
}>(), {
  position: "bottom-start",
  label: "弹出内容"
});

const root = ref<HTMLElement | null>(null);

function close() {
  open.value = false;
}

function toggle() {
  open.value = !open.value;
}

function onDocumentPointerDown(event: PointerEvent) {
  if (open.value && root.value && !root.value.contains(event.target as Node)) close();
}

onMounted(() => document.addEventListener("pointerdown", onDocumentPointerDown));
onBeforeUnmount(() => document.removeEventListener("pointerdown", onDocumentPointerDown));
</script>

<template>
  <span ref="root" class="ui-popover-root" @keydown.esc="close">
    <slot name="trigger" :open="open" :toggle="toggle" :close="close" />
    <Transition name="ui-popover-fade">
      <section v-if="open" class="ui-popover" :data-position="position" role="dialog" :aria-label="label" tabindex="-1">
        <slot :close="close" />
      </section>
    </Transition>
  </span>
</template>
