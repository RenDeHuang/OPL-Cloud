<script setup lang="ts">
import UiCopyButton from "./UiCopyButton.vue";

withDefaults(defineProps<{
  code: string;
  language?: string;
  copyLabel?: string;
}>(), {
  language: "text",
  copyLabel: "复制代码"
});

const emit = defineEmits<{ copied: []; error: [error: unknown] }>();
</script>

<template>
  <div class="ui-code-block" :data-language="language">
    <pre><code><slot>{{ code }}</slot></code></pre>
    <UiCopyButton class="ui-code-block__copy" :value="code" :label="copyLabel" variant="ghost" @copied="emit('copied')" @error="emit('error', $event)" />
  </div>
</template>
