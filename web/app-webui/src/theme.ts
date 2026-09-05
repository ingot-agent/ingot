export type Theme = 'light' | 'dark' | 'system'
export function readPreference(key: string, fallback: string): string {
  try { return localStorage.getItem('ingot.' + key) || fallback } catch { return fallback }
}
export function savePreference(key: string, value: string) {
  try { localStorage.setItem('ingot.' + key, value) } catch { /* Storage can be disabled. */ }
}
export function applyTheme(theme: string) {
  const dark = theme === 'dark' || (theme === 'system' && window.matchMedia('(prefers-color-scheme: dark)').matches)
  document.documentElement.classList.toggle('dark', dark)
}
applyTheme(readPreference('theme', 'light'))
window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => applyTheme(readPreference('theme', 'light')))
