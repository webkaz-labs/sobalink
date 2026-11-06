import assert from 'node:assert/strict'
import test, { after } from 'node:test'
import { spawnSync } from 'node:child_process'
import { mkdir, mkdtemp, readdir, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import config from '../playwright.config.mjs'
import { safeArtifactFilename } from './fixture-safety.mjs'

after(() => rm(config.outputDir, { recursive: true, force: true }))

const [isolated, serial] = config.projects
const matchesIsolated = file => isolated.testMatch.includes(`**/${file}`)

test('audited files use at most two workers while every unlisted spec stays serial', async () => {
  assert.equal(config.workers, 2)
  assert.equal(config.fullyParallel, false)
  assert.equal(config.retries, 0)
  assert.equal(config.testMatch, '**/*.spec.mjs')
  assert.equal(config.projects.length, 2)
  assert.equal(isolated.workers, 2)
  assert.equal(serial.workers, 1)
  assert.equal(isolated.teardown, serial.name)
  assert.equal(isolated.dependencies, undefined)
  assert.equal(serial.dependencies, undefined)
  assert.deepEqual(serial.testIgnore, isolated.testMatch)
  assert.equal(new Set(isolated.testMatch).size, isolated.testMatch.length)
  const files = (await readdir(new URL('.', import.meta.url))).filter(file => file.endsWith('.spec.mjs'))
  for (const pattern of isolated.testMatch) assert.ok(files.includes(pattern.slice(3)), 'Every parallel entry must name an existing spec')
  for (const file of ['advanced', 'advertised-overview', 'client-helpers', 'lifecycle-saved', 'network', 'port-proposals', 'workflows', 'future-unreviewed']) {
    assert.equal(matchesIsolated(`${file}.spec.mjs`), false, 'Native bind/probe cases and new files must stay serial')
  }
  for (const file of files) {
    const isolatedMatches = Number(matchesIsolated(file))
    const serialMatches = Number(!serial.testIgnore.includes(`**/${file}`))
    assert.equal(isolatedMatches + serialMatches, 1, 'Every spec must run in exactly one project')
  }
})

test('artifact ownership survives duplicate labels, worker reuse, retries and repeats', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'soba-artifact-ownership-'))
  const privateID = '/private/fixture/PRIVATE_SENTINEL'
  const first = { testId: privateID, repeatEachIndex: 0, retry: 0, workerIndex: 0 }
  try {
    const owners = [first, { ...first, testId: `${privateID}-other` }, { ...first, repeatEachIndex: 1 }, { ...first, retry: 1 }]
    const names = owners.map(owner => safeArtifactFilename('shared-capture', 'json', owner))
    assert.equal(new Set(names).size, owners.length)
    assert.equal(safeArtifactFilename('shared-capture', 'json', { ...first, workerIndex: 9 }), names[0])
    await Promise.all(names.map((name, index) => writeFile(join(directory, name), JSON.stringify({ owner: index }), { flag: 'wx', mode: 0o600 })))
    for (const [index, name] of names.entries()) {
      assert.match(name, /^[a-z0-9][a-z0-9_.-]*\.json$/i)
      assert.equal(name.includes('PRIVATE_SENTINEL'), false)
      assert.deepEqual(JSON.parse(await readFile(join(directory, name), 'utf8')), { owner: index })
    }
    for (const name of ['../session', '/private/session', '', 'a/b']) assert.throws(() => safeArtifactFilename(name, 'png', first))
    for (const value of [-1, 1.5, undefined, 'PRIVATE_SENTINEL']) assert.throws(() => safeArtifactFilename('capture', 'png', { ...first, retry: value }))
    assert.throws(() => safeArtifactFilename('capture', 'txt', first))
  } finally { await rm(directory, { recursive: true, force: true }) }
})

// Run the real pinned scheduler without starting a browser or Go process. A
// failing first phase must not skip the independent serial cases or turn green.
for (const fail of [false, true]) test(`project ordering preserves every result when isolated failure is ${fail}`, { timeout: 30_000 }, async () => {
  const root = await mkdtemp(join(tmpdir(), 'soba-scheduler-regression-'))
  const events = join(root, 'events.jsonl')
  const output = join(root, 'evidence')
  const files = ['layouts.spec.mjs', 'routes.spec.mjs', 'port-proposals.spec.mjs', 'future-unreviewed.spec.mjs']
  const playwright = import.meta.resolve('@playwright/test')
  try {
    await mkdir(output, { mode: 0o700 })
    await Promise.all(files.map(file => writeFile(join(root, file), `
      import { test } from ${JSON.stringify(playwright)}
      import { access, appendFile, writeFile } from 'node:fs/promises'
      import { join } from 'node:path'
      import { safeArtifactFilename } from ${JSON.stringify(new URL('./fixture-safety.mjs', import.meta.url).href)}
      import { setTimeout as delay } from 'node:timers/promises'
      test('synthetic scheduler case', async ({}, testInfo) => {
        const event = type => appendFile(${JSON.stringify(events)}, JSON.stringify({ type, file: ${JSON.stringify(file)}, project: testInfo.project.name }) + '\\n')
        await event('start')
        try {
          const artifact = safeArtifactFilename('shared-capture', 'json', testInfo)
          await writeFile(join(${JSON.stringify(output)}, artifact), JSON.stringify({ owner: ${JSON.stringify(file)} }), { flag: 'wx', mode: 0o600 })
          testInfo.annotations.push({ type: 'safe-artifact', description: artifact })
          if (testInfo.project.name === 'isolated') {
            await writeFile(join(${JSON.stringify(root)}, ${JSON.stringify(file)} + '.started'), '')
            for (let attempt = 0; ; attempt++) {
              try {
                await Promise.all(['layouts.spec.mjs', 'routes.spec.mjs'].map(file => access(join(${JSON.stringify(root)}, file + '.started'))))
                break
              } catch {
                if (attempt === 999) throw new Error('Synthetic parallel workers did not overlap')
                await delay(10)
              }
            }
          }
          await delay(50)
          if (${fail && file === 'layouts.spec.mjs'}) throw new Error('Synthetic scheduler failure')
        } finally { await event('end') }
      })
    `)))
    const runner = join(root, 'playwright.config.mjs')
    await writeFile(runner, `export default ${JSON.stringify({
      testDir: root, testMatch: config.testMatch, fullyParallel: config.fullyParallel,
      workers: config.workers, retries: config.retries, projects: config.projects,
      outputDir: join(root, 'private'), preserveOutput: 'never',
      reporter: fileURLToPath(new URL('./reporter.mjs', import.meta.url)),
    })}`)
    const result = spawnSync(process.execPath, [fileURLToPath(import.meta.resolve('@playwright/test/cli')), 'test', '--config', runner], {
      env: { ...process.env, SOBA_SCREENSHOT_DIR: output, PLAYWRIGHT_NO_COPY_PROMPT: '1' },
      encoding: 'utf8', timeout: 25_000,
    })
    assert.equal(result.error, undefined)
    assert.equal(result.status, fail ? 1 : 0, 'The scheduler must preserve the actual failure status')
    const report = JSON.parse(await readFile(join(output, 'playwright-report.json'), 'utf8'))
    assert.equal(report.expectedTests, files.length)
    assert.equal(report.tests.length, files.length)
    assert.equal(report.requiredCoverageComplete, !fail)
    assert.equal(report.status, fail ? 'failed' : 'passed')
    assert.deepEqual(report.tests.map(record => record.file).sort(), [...files].sort())
    for (const record of report.tests) {
      assert.equal(record.name, `${record.file} > synthetic scheduler case`, 'Scheduling projects must not rename reported scenarios')
      assert.equal(record.status, fail && record.file === 'layouts.spec.mjs' ? 'failed' : 'passed')
      assert.equal(record.artifacts.length, 1)
      assert.deepEqual(JSON.parse(await readFile(join(output, record.artifacts[0]), 'utf8')), { owner: record.file })
    }
    const timeline = (await readFile(events, 'utf8')).trim().split('\n').map(line => JSON.parse(line))
    const active = new Set()
    let isolatedFinished = 0, peak = 0
    for (const event of timeline) {
      if (event.type === 'start') {
        if (event.project === serial.name) {
          assert.equal(isolatedFinished, 2, 'Serial cases must wait for the entire parallel phase, including failure cleanup')
          assert.equal(active.size, 0, 'A serial case must be the only running fixture')
        }
        active.add(event.file)
        peak = Math.max(peak, active.size)
      } else {
        assert.ok(active.delete(event.file))
        if (event.project === isolated.name) isolatedFinished++
      }
    }
    assert.equal(peak, 2, 'The audited phase must actually overlap two test files')
    assert.equal(active.size, 0)
  } finally { await rm(root, { recursive: true, force: true }) }
})
