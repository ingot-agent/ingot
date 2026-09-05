import { spawn } from 'node:child_process'
import { URL } from 'node:url'
import process from 'node:process'

const child = spawn('go', ['test', '-run', '^TestBrowserFixture$', '-count=1', '-timeout', '0', '-v', './app'], {
  cwd: new URL('../../../plugins/app-webui/', import.meta.url),
  env: { ...process.env, GOWORK: 'off', INGOT_WEBUI_FIXTURE_ADDR: process.env.INGOT_WEBUI_FIXTURE_ADDR || '127.0.0.1:17316' },
  stdio: 'inherit',
})
process.on('SIGINT', () => child.kill('SIGINT'))
process.on('SIGTERM', () => child.kill('SIGTERM'))
child.on('exit', code => { process.exitCode = code || 0 })
