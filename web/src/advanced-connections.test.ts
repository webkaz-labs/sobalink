import { expect, it } from 'vitest'
import type { ProxyScope, Service } from './api'
import { includesServicePort, readProxyReview, validProxyScope } from './advanced-connections'
const scope: ProxyScope = { name: 'example-proxy', backend: 'tailnet', loopbackHost: '127.0.0.1', localPort: 1080, lifetime: 'until-stopped', ttlSeconds: 0, targets: [{ peerId: 'fixture-studio', port: 443 }] }
it('checks exact effective port intervals without broadening malformed scope', () => {
  const service = { ports: '443,8443-8445' } as Service
  for (const port of [443, 8443, 8444, 8445]) expect(includesServicePort(service, port)).toBe(true)
  for (const port of [0, 444, 8446, 65536, 443.5]) expect(includesServicePort(service, port)).toBe(false)
  for (const ports of ['443,', '1-0', '1-2-3', '0-65535', 'not-a-port']) expect(includesServicePort({ ports } as Service, 443)).toBe(false)
})
it('rejects target identity drift, duplicate targets, reserved ports and invalid lifetime before preview', () => {
  expect(validProxyScope(scope, ['fixture-studio'])).toBe(true)
  expect(validProxyScope(scope, [])).toBe(false)
  expect(validProxyScope({ ...scope, targets: [...scope.targets, ...scope.targets] }, ['fixture-studio'])).toBe(false)
  expect(validProxyScope({ ...scope, localPort: 54543 }, ['fixture-studio'])).toBe(false)
  expect(validProxyScope(scope, ['fixture-studio'], [443])).toBe(true)
  expect(validProxyScope({ ...scope, targets: [{ peerId: 'fixture-studio', port: 54543 }] }, ['fixture-studio'])).toBe(false)
  expect(validProxyScope({ ...scope, ttlSeconds: 3600 }, ['fixture-studio'])).toBe(false)
  expect(validProxyScope({ ...scope, lifetime: 'finite', ttlSeconds: 259200 }, ['fixture-studio'])).toBe(true)
})
it('requires preview to match exact requested scope and revision, independent of JSON key order', () => {
  const review = { scope: { targets: scope.targets, ttlSeconds: 0, lifetime: 'until-stopped', localPort: 1080, loopbackHost: '127.0.0.1', backend: 'tailnet', name: 'example-proxy' }, revision: 'a'.repeat(64), endpoint: '127.0.0.1:1080', targets: [{ peerId: 'fixture-studio', port: 443, host: 'studio.example' }], authentication: 'username-password-required', application: 'unverified' }
  expect(readProxyReview(review, scope).scope).toEqual(scope)
  expect(() => readProxyReview({ ...review, scope: { ...scope, localPort: 1081 } }, scope)).toThrow('invalid_response')
  expect(() => readProxyReview({ ...review, revision: '' }, scope)).toThrow('invalid_response')
  expect(() => readProxyReview({ ...review, targets: [{ peerId: 'other-peer', port: 443, host: 'studio.example' }] }, scope)).toThrow('invalid_response')
})
