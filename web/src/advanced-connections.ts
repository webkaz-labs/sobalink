import type { ProxyReview, ProxyScope, Service } from './api'
import { loopbackEndpoint, validLifetime, validServiceName } from './service-form'
export function validPort(port: number) { return Number.isInteger(port) && port >= 1 && port <= 65535 }
export function reservedPort(port: number, reserved: number[] = []) { return port >= 54543 && port <= 54545 || reserved.includes(port) }
export function servicePorts(service: Service) { return service.ports || String(service.remotePort || '') }
export function includesServicePort(service: Service, port: number) {
  if (!validPort(port)) return false
  const intervals = servicePorts(service).split(',').map(value => value.trim().split('-').map(Number))
  if (!intervals.length || intervals.some(parts => parts.length > 2 || parts.some(p => !validPort(p)) || parts.length === 2 && parts[0] > parts[1])) return false
  return intervals.some(([first, last = first]) => port >= first && port <= last)
}
export function singleServicePort(service: Service) {
  const text = servicePorts(service)
  const port = Number(text)
  return /^\d+$/.test(text) && validPort(port) ? String(port) : ''
}
export function validProxyScope(scope: ProxyScope, peerIDs: string[], reserved: number[] = []) {
  return validServiceName(scope.name) && ['tailnet', 'lan'].includes(scope.backend) && ['127.0.0.1', '::1'].includes(scope.loopbackHost) && validPort(scope.localPort) && scope.localPort >= 1024 && !reservedPort(scope.localPort, reserved) && validLifetime(scope.lifetime, scope.ttlSeconds, 'connect') && scope.targets.length > 0 && scope.targets.every(target => peerIDs.includes(target.peerId) && validPort(target.port) && !reservedPort(target.port)) && new Set(scope.targets.map(target => `${target.peerId}:${target.port}`)).size === scope.targets.length
}
export const proxyScopeKey = (scope: ProxyScope) => JSON.stringify([scope.name, scope.backend, scope.loopbackHost, scope.localPort, scope.lifetime, scope.ttlSeconds, [...scope.targets].sort((a, b) => a.peerId.localeCompare(b.peerId) || a.port - b.port).map(target => [target.peerId, target.port])])
export function readProxyReview(value: unknown, requested: ProxyScope): ProxyReview {
  const review = value as ProxyReview
  if (!review?.scope || !Array.isArray(review.scope.targets) || !/^[0-9a-f]{64}$/.test(review.revision) || review.authentication !== 'username-password-required' || review.application !== 'unverified' || review.endpoint !== loopbackEndpoint(requested.loopbackHost, String(requested.localPort)) || proxyScopeKey(review.scope) !== proxyScopeKey(requested) || !Array.isArray(review.targets) || review.targets.length !== requested.targets.length || review.targets.some(target => typeof target.host !== 'string' || !target.host || !requested.targets.some(item => item.peerId === target.peerId && item.port === target.port)) || new Set(review.targets.map(target => `${target.peerId}:${target.port}`)).size !== requested.targets.length) throw new Error('invalid_response')
  return review
}
