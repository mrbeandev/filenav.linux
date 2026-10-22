// CodeMirror 6 editor wrapper exposed as window.FNEditor (loaded on demand).
import { EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter, drawSelection,
  dropCursor, rectangularSelection, crosshairCursor, highlightSpecialChars } from '@codemirror/view';
import { EditorState, Compartment } from '@codemirror/state';
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
import { indentOnInput, bracketMatching, foldGutter, foldKeymap, syntaxHighlighting, defaultHighlightStyle, LanguageDescription } from '@codemirror/language';
import { searchKeymap, highlightSelectionMatches, search } from '@codemirror/search';
import { closeBrackets, closeBracketsKeymap, autocompletion, completionKeymap } from '@codemirror/autocomplete';
import { oneDark } from '@codemirror/theme-one-dark';
import { languages } from '@codemirror/language-data';

// extra filename → language hints that language-data doesn't match by itself
const ALIAS = {
  '.gitignore': 'shell', '.dockerignore': 'shell', '.env': 'properties', '.npmrc': 'properties', '.editorconfig': 'properties',
  '.prettierignore': 'shell', '.gitattributes': 'properties', '.bashrc': 'shell', '.zshrc': 'shell', '.profile': 'shell',
  'makefile': 'cmake', 'caddyfile': 'nginx', 'go.mod': 'go', '.service': 'properties', '.conf': 'nginx',
};

function findLang(name) {
  const lower = name.toLowerCase();
  let d = LanguageDescription.matchFilename(languages, name);
  if (d) return d;
  for (const [k, v] of Object.entries(ALIAS)) {
    if (lower === k || lower.endsWith(k)) { d = LanguageDescription.matchLanguageName(languages, v, true); if (d) return d; }
  }
  return null;
}

export async function create(parent, { doc, name, dark, onChange, onSave, readOnly }) {
  const langConf = new Compartment();
  const wrapConf = new Compartment();
  const view = new EditorView({
    parent,
    state: EditorState.create({
      doc,
      extensions: [
        lineNumbers(), highlightActiveLineGutter(), highlightSpecialChars(), history(), foldGutter(), drawSelection(),
        dropCursor(), EditorState.allowMultipleSelections.of(true), indentOnInput(),
        syntaxHighlighting(defaultHighlightStyle, { fallback: true }), bracketMatching(), closeBrackets(), autocompletion(),
        rectangularSelection(), crosshairCursor(), highlightActiveLine(), highlightSelectionMatches(), search({ top: true }),
        keymap.of([
          { key: 'Mod-s', preventDefault: true, run: () => { onSave && onSave(); return true; } },
          ...closeBracketsKeymap, ...defaultKeymap, ...searchKeymap, ...historyKeymap, ...foldKeymap, ...completionKeymap, indentWithTab,
        ]),
        dark ? oneDark : [],
        langConf.of([]),
        wrapConf.of([]),
        EditorState.readOnly.of(!!readOnly),
        EditorView.updateListener.of(u => { if (u.docChanged && onChange) onChange(); }),
        EditorView.theme({ '&': { height: '100%', fontSize: '13px' }, '.cm-scroller': { fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace' } }),
      ],
    }),
  });
  const desc = findLang(name);
  let langName = 'Plain text';
  if (desc) {
    langName = desc.name;
    desc.load().then(support => view.dispatch({ effects: langConf.reconfigure(support) })).catch(() => { });
  }
  let wrapped = false;
  return {
    view,
    language: langName,
    getValue: () => view.state.doc.toString(),
    setWrap(on) { wrapped = on; view.dispatch({ effects: wrapConf.reconfigure(on ? EditorView.lineWrapping : []) }); },
    get wrapped() { return wrapped; },
    focus: () => view.focus(),
    destroy: () => view.destroy(),
  };
}
