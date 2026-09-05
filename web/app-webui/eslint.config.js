import js from '@eslint/js'
import ts from 'typescript-eslint'
import vue from 'eslint-plugin-vue'

export default ts.config(
  { ignores: ['node_modules/**', 'playwright-report/**', 'test-results/**'] },
  js.configs.recommended,
  ...ts.configs.recommended,
  ...vue.configs['flat/recommended'],
  {
    files: ['**/*.vue'],
    languageOptions: { parserOptions: { parser: ts.parser } },
    rules: {
      'no-undef': 'off',
      'vue/multi-word-component-names': 'off',
      'vue/max-attributes-per-line': 'off',
      'vue/singleline-html-element-content-newline': 'off',
      'vue/html-self-closing': 'off',
      'vue/html-indent': 'off',
      'vue/multiline-html-element-content-newline': 'off',
    },
  },
  { rules: { '@typescript-eslint/no-explicit-any': 'off' } },
)
