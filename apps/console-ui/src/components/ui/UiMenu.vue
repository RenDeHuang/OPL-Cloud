<script setup lang="ts">
import { Check } from "@lucide/vue";
import { nextTick, ref, watch } from "vue";

import UiPopover from "./UiPopover.vue";

interface UiMenuItem {
  id: string;
  label: string;
  disabled?: boolean;
  color?: "default" | "danger";
  checked?: boolean;
  separatorBefore?: boolean;
}

withDefaults(defineProps<{
  items: UiMenuItem[];
  label?: string;
  position?: "bottom-start" | "bottom-end" | "top";
}>(), {
  label: "操作菜单",
  position: "bottom-end"
});

const emit = defineEmits<{ select: [id: string] }>();
const open = ref(false);
const menuRoot = ref<HTMLElement | null>(null);
let returnFocus: HTMLElement | null = null;

function enabledItems() {
  return Array.from(menuRoot.value?.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)') || []);
}

function focusItem(items: HTMLButtonElement[], index: number) {
  items[(index + items.length) % items.length]?.focus();
}

function onMenuKeydown(event: KeyboardEvent) {
  if (event.key === "Tab") {
    open.value = false;
    return;
  }
  const items = enabledItems();
  if (!items.length) return;
  const current = items.indexOf(document.activeElement as HTMLButtonElement);
  if (event.key === "ArrowDown") {
    event.preventDefault();
    focusItem(items, current + 1);
  } else if (event.key === "ArrowUp") {
    event.preventDefault();
    focusItem(items, current < 0 ? items.length - 1 : current - 1);
  } else if (event.key === "Home") {
    event.preventDefault();
    focusItem(items, 0);
  } else if (event.key === "End") {
    event.preventDefault();
    focusItem(items, items.length - 1);
  }
}

async function closeAndRestore(close: () => void) {
  close();
  await nextTick();
  returnFocus?.focus();
}

function choose(item: UiMenuItem, close: () => void) {
  if (item.disabled) return;
  emit("select", item.id);
  void closeAndRestore(close);
}

watch(open, async (value) => {
  if (!value) return;
  returnFocus = document.activeElement instanceof HTMLElement ? document.activeElement : null;
  await nextTick();
  enabledItems()[0]?.focus();
});
</script>

<template>
  <UiPopover v-model="open" :label="label" :position="position">
    <template #trigger="trigger"><slot name="trigger" v-bind="trigger" /></template>
    <template #default="{ close }">
      <div ref="menuRoot" class="ui-menu" role="menu" :aria-label="label" @keydown="onMenuKeydown" @keydown.esc.stop.prevent="closeAndRestore(close)">
        <template v-for="item in items" :key="item.id">
          <div v-if="item.separatorBefore" class="ui-menu__separator" role="separator" />
          <button
            class="ui-menu__item"
            type="button"
            role="menuitem"
            :data-color="item.color || 'default'"
            :aria-checked="item.checked === undefined ? undefined : item.checked"
            :disabled="item.disabled"
            @click="choose(item, close)"
          ><slot name="item" :item="item">{{ item.label }}</slot><Check v-if="item.checked" class="ui-menu__check" :size="14" /></button>
        </template>
      </div>
    </template>
  </UiPopover>
</template>
