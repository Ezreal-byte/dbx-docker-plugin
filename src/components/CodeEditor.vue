<script setup lang="ts">
import { onMounted, onUnmounted, ref, watch, computed } from 'vue';
import { basicSetup } from 'codemirror';
import { EditorState, Compartment } from '@codemirror/state';
import { EditorView, keymap } from '@codemirror/view';
import { indentWithTab } from '@codemirror/commands';
import { HighlightStyle, syntaxHighlighting } from '@codemirror/language';
import { tags } from '@lezer/highlight';
import { languageExtension, languageName } from '../editorLanguages';
import { local } from '../automation';
const props = withDefaults(defineProps<{ modelValue: string; path: string; readOnly?: boolean; height?: string }>(), { readOnly: false, height: '360px' });
const emit = defineEmits<{ 'update:modelValue': [string] }>();
const host = ref<HTMLElement>(), wrap = ref(true);
const language = computed(() => languageName(props.path));
const mode = new Compartment(), editable = new Compartment(), wrapping = new Compartment();
let view: EditorView | undefined;
const colors = HighlightStyle.define([
  { tag: [tags.keyword, tags.bool, tags.null], color: 'var(--code-keyword)' },
  { tag: [tags.propertyName, tags.attributeName, tags.definition(tags.variableName)], color: 'var(--code-property)' },
  { tag: [tags.string, tags.special(tags.string), tags.content], color: 'var(--code-string)' },
  { tag: [tags.number, tags.atom], color: 'var(--code-number)' },
  { tag: [tags.tagName, tags.typeName], color: 'var(--code-keyword)' },
  { tag: tags.comment, color: 'var(--muted-foreground)', fontStyle: 'italic' },
]);
onMounted(() => {
  view = new EditorView({ parent: host.value, state: EditorState.create({ doc: props.modelValue, extensions: [
    basicSetup, keymap.of([indentWithTab]), mode.of(languageExtension(props.path)),
    editable.of([EditorState.readOnly.of(props.readOnly), EditorView.editable.of(!props.readOnly)]),
    wrapping.of(EditorView.lineWrapping), syntaxHighlighting(colors),
    EditorView.contentAttributes.of({ 'aria-label': props.path, spellcheck: 'false' }),
    EditorView.updateListener.of(update => { if (update.docChanged && update.state.doc.toString() !== props.modelValue) emit('update:modelValue', update.state.doc.toString()); }),
  ] }) });
});
watch(() => props.modelValue, value => { if (view && view.state.doc.toString() !== value) view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: value } }); });
watch(() => props.path, value => view?.dispatch({ effects: mode.reconfigure(languageExtension(value)) }));
watch(() => props.readOnly, value => view?.dispatch({ effects: editable.reconfigure([EditorState.readOnly.of(value), EditorView.editable.of(!value)]) }));
watch(wrap, value => view?.dispatch({ effects: wrapping.reconfigure(value ? EditorView.lineWrapping : []) }));
onUnmounted(() => view?.destroy());
</script>
<template>
  <div class="code-editor" :style="{ height }">
    <div class="code-editor-bar"><span class="truncate" :title="path">{{ path }}</span><span class="code-language">{{ language }}</span><label class="check-label"><input type="checkbox" v-model="wrap" />{{ local('换行', 'Wrap') }}</label><span v-if="readOnly" class="code-readonly">{{ local('只读', 'Read only') }}</span></div>
    <div ref="host" class="code-editor-host" />
  </div>
</template>
