// CodeMirror 6 本地打包入口文件
// 将 CodeMirror 6 核心模块 + 常用语言包打包为 IIFE 格式，供 vanilla JS 项目使用
import { EditorState, Compartment } from "@codemirror/state";
import { EditorView, keymap, lineNumbers, highlightActiveLine, highlightActiveLineGutter, drawSelection } from "@codemirror/view";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { syntaxHighlighting, defaultHighlightStyle, bracketMatching, foldGutter, indentOnInput, indentUnit } from "@codemirror/language";
import { javascript } from "@codemirror/lang-javascript";
import { python } from "@codemirror/lang-python";
import { json } from "@codemirror/lang-json";
import { markdown } from "@codemirror/lang-markdown";
import { html } from "@codemirror/lang-html";
import { css } from "@codemirror/lang-css";
import { go } from "@codemirror/lang-go";
import { sql } from "@codemirror/lang-sql";
import { yaml } from "@codemirror/lang-yaml";
import { xml } from "@codemirror/lang-xml";

// 语言包映射：扩展名 -> CodeMirror 语言扩展工厂函数
const langMap = {
  javascript, python, json, markdown, html, css, go, sql, yaml, xml
};

// 根据文件名扩展名返回对应的语言扩展（数组形式，便于展开到 extensions）
function getLanguageForFile(filename) {
  const ext = (filename.split('.').pop() || '').toLowerCase();
  const mapping = {
    js: javascript, mjs: javascript, cjs: javascript, jsx: javascript,
    ts: javascript, tsx: javascript,
    py: python,
    json: json,
    md: markdown, markdown: markdown,
    html: html, htm: html,
    css: css,
    go: go,
    sql: sql,
    yml: yaml, yaml: yaml,
    xml: xml, svg: xml,
  };
  const langFn = mapping[ext];
  // langFn() 返回单一 Extension 对象，包装为数组以便用 ... 展开到 extensions
  return langFn ? [langFn()] : [];
}

// 创建编辑器实例
// parent: DOM 父元素，opts: { content, language, readOnly, onChange }
function createEditor(parent, opts = {}) {
  let langExt;
  if (opts.language) {
    // langMap[opts.language]() 返回单一 Extension 对象，包装为数组
    if (langMap[opts.language]) {
      langExt = [langMap[opts.language]()];
    } else {
      langExt = getLanguageForFile(opts.language); // 已返回数组
    }
  } else {
    langExt = [];
  }

  const extensions = [
    lineNumbers(),
    foldGutter(),
    history(),
    bracketMatching(),
    indentOnInput(),
    indentUnit.of("  "),
    highlightActiveLine(),
    highlightActiveLineGutter(),
    drawSelection(),
    syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
    keymap.of([...defaultKeymap, ...historyKeymap, indentWithTab]),
    EditorView.lineWrapping,
    ...langExt,
  ];

  if (opts.onChange) {
    extensions.push(EditorView.updateListener.of(v => {
      if (v.docChanged) opts.onChange(v.state.doc.toString());
    }));
  }

  if (opts.readOnly) {
    extensions.push(EditorState.readOnly.of(true));
  }

  const state = EditorState.create({
    doc: opts.content || "",
    extensions,
  });

  return new EditorView({ state, parent });
}

// 暴露全局对象，供 app.js 调用
window.CodeMirrorEditor = {
  createEditor,
  getLanguageForFile,
  EditorView,
  EditorState,
};
