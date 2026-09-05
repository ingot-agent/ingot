import { execFileSync } from 'node:child_process'
import { URL } from 'node:url'

const path = 'plugins/app-webui/app/webdist'
const root = new URL('../../../', import.meta.url)
const changes = execFileSync('git', ['status', '--porcelain', '--', path], { cwd: root, encoding: 'utf8' })
if (changes.trim()) {
  throw new Error('Embedded Web UI is out of date. Run npm run build and include webdist changes.\n' + changes)
}
