import type { CommandPayloads, Service, ServiceLifetime } from './api'
export type CatalogViewSelector = CommandPayloads['resource.catalog.view.list']['request']
export type CatalogViewObservation = Readonly<{ selector: CatalogViewSelector; peerKey: string; checkedAt: string; reviewRevision: string; services: readonly { service: Service; connectAllowed: boolean }[] }>
export function readCatalogView(value: unknown, peerKey: string, selector: CatalogViewSelector): CatalogViewObservation {
  const v = value as Record<string, unknown> | null
  const expected = v?.selector as CatalogViewSelector | undefined
  if (!v || v.peerKey !== peerKey || !expected || expected.protocolVersion !== 1 || expected.target?.schemaVersion !== 1 || expected.target.resourceId !== selector.target.resourceId || expected.grantId !== selector.grantId || expected.grantRevision !== selector.grantRevision || typeof v.reviewRevision !== 'string' || !/^[0-9a-f]{32}$/.test(v.reviewRevision) || typeof v.checkedAt !== 'string' || !Number.isFinite(Date.parse(v.checkedAt)) || !Array.isArray(v.services) || v.services.length > 16) throw new Error('invalid_response')
  let previous = ''
  const services = v.services.map(row => {
    const s = row?.service
    if (!s || typeof row.connectAllowed !== 'boolean' || typeof s.id !== 'string' || !/^[0-9a-f]{32}$/.test(s.id) || s.id <= previous || !['tcp', 'udp'].includes(s.network) || typeof s.ports !== 'string' || !s.ports || s.ports.length > 1024 || s.purpose !== undefined && (typeof s.purpose !== 'string' || s.purpose.length > 1024) || !['finite', 'until-revoked'].includes(s.lifetime) || s.application !== 'unverified' || s.expiresAt !== undefined && (typeof s.expiresAt !== 'string' || !Number.isFinite(Date.parse(s.expiresAt)))) throw new Error('invalid_response')
    previous = s.id
    const service: Service = { id: s.id, peerId: peerKey, name: s.purpose || 'generic', network: s.network, ports: s.ports, purpose: s.purpose, lifetime: s.lifetime as ServiceLifetime, expiresAt: s.expiresAt, application: 'unverified', status: 'active', checkedAt: v.checkedAt as string, revision: v.reviewRevision as string }
    return { service, connectAllowed: row.connectAllowed }
  })
  return { selector, peerKey, checkedAt: v.checkedAt, reviewRevision: v.reviewRevision, services }
}
export function catalogViewSelector(resourceId: string, grantId: string, revision: string): CatalogViewSelector | null {
  const grantRevision = Number(revision)
  return /^[0-9a-f]{32}$/.test(resourceId) && /^[0-9a-f]{32}$/.test(grantId) && /^[1-9][0-9]*$/.test(revision) && Number.isSafeInteger(grantRevision) ? { protocolVersion: 1, target: { schemaVersion: 1, resourceId }, grantId, grantRevision } : null
}

export function catalogViewError(value: unknown, ja: boolean): string | null {
  const code = (value as { code?: string } | null)?.code || ''
  const state = code.replace(/^(?:resource_)?catalog_view_/, '')
  const messages: Record<string, [string, string]> = {
    stale: ['The peer, shared service or approval has changed. Fetch the list and review again.', '相手・共有サービス・許可が変更されました。一覧を再取得してレビューしてください。'],
    revoked: ['Viewing permission was revoked. Ask the sharing device to approve it again.', '閲覧許可が取り消されました。提供側で再承認してください。'],
    expired: ['Viewing permission has expired. Ask the sharing device for a new approval.', '閲覧許可の期限が切れました。提供側で新しい許可を承認してください。'],
    unavailable: ['Current permission could not be checked. Check the connection and fetch again.', '現在の許可を確認できません。接続を確認して再取得してください。'],
    limited: ['The list exceeds the current limit. It is not a complete list.', '一覧が現在の上限を超えています。完全な一覧ではありません。'],
    invalid: ['Check the exact resource ID, grant ID and revision from the sharing device.', '提供側の正確な対象 ID・許可 ID・変更番号を確認してください。'],
  }
  return messages[state]?.[ja ? 1 : 0] || null
}
