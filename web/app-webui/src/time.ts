import { useI18n } from 'vue-i18n'

// relativeTime formats an ISO timestamp as a compact relative age for the
// conversation sidebar. Coarse buckets are enough for a session list.
export function useRelativeTime() {
  const { t } = useI18n()
  return (iso: string): string => {
    const then = new Date(iso).getTime()
    if (Number.isNaN(then)) return ''
    const seconds = Math.max(0, Math.floor((Date.now() - then) / 1000))
    if (seconds < 60) return t('timeJustNow')
    const minutes = Math.floor(seconds / 60)
    if (minutes < 60) return t('timeMinute', { n: minutes })
    const hours = Math.floor(minutes / 60)
    if (hours < 24) return t('timeHour', { n: hours })
    const days = Math.floor(hours / 24)
    if (days < 30) return t('timeDay', { n: days })
    const months = Math.floor(days / 30)
    if (months < 12) return t('timeMonth', { n: months })
    return t('timeYear', { n: Math.floor(months / 12) })
  }
}

// workspaceBasename extracts the trailing folder name of an absolute workspace
// path so the sidebar groups by the folder the user selected rather than the
// whole OS path.
export function workspaceBasename(path: string): string {
  if (!path) return ''
  const trimmed = path.replace(/[\\/]+$/, '')
  const parts = trimmed.split(/[\\/]+/)
  return parts[parts.length - 1] || path
}
