<script setup lang="ts">
import { Inbox } from "@lucide/vue";

withDefaults(defineProps<{
  title: string;
  description?: string;
  live?: "off" | "polite" | "assertive";
}>(), {
  description: "",
  live: "polite"
});
</script>

<template>
  <section class="ui-empty" :aria-live="live">
    <span class="ui-empty__icon"><slot name="icon"><Inbox :size="20" aria-hidden="true" /></slot></span>
    <h3>{{ title }}</h3>
    <p v-if="description">{{ description }}</p>
    <div v-if="$slots.action" class="ui-empty__action"><slot name="action" /></div>
  </section>
</template>
