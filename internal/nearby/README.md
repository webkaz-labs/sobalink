# Scoped nearby setup hints

[日本語](#日本語)

This package implements an opt-in, finite mDNS discovery adapter. A discovered
candidate is an **unauthenticated hint**, never a peer identity, permission,
certificate pin, trust grant, or evidence of connectivity. Core must consent to
and own the bootstrap listener, look up the exact selected candidate ID again,
and authenticate that endpoint independently.

## Current provider selection

Core uses `StartNative` only after scope and advertisement consent. Linux and
Windows use scoped raw IPv4 packets. Darwin+cgo uses the selected-interface
system DNS-SD API; see [its exact contract](DNSSD.en.md) ([日本語](DNSSD.ja.md)).
Darwin without cgo reports unavailable. The older explicit raw Darwin factory
and loopback fixture remain diagnostic code, not the selected product provider.
OS hints carry no invented packet metadata and may come from a daemon cache.

## Contract

- `Start` requires an explicit transport factory, selected interface index,
  numeric local address and exact private prefix. There is no implicit native
  discovery, interface expansion, unicast probe, DNS lookup, or fallback scan.
- The local advertisement includes only a fresh 256-bit setup session ID,
  explicitly supplied alias, fixed protocol/capability, and numeric high-port
  bootstrap endpoint. DNS instance/target labels derive only from the ephemeral
  session ID. No OS hostname, permanent identity, pin, invitation or files are
  included. The alias is visible to the selected scope during discovery.
- Raw packet sources and advertised addresses must independently lie in the
  selected prefix. Receive interface, destination, source port and TTL are checked.
  Record bundles bind their actual observation provenance; this does **not**
  authenticate an advertised endpoint or equate its ownership with packet source.
  OS observations instead bind the exact selected interface and live queries.
- A candidate ID binds the entire advertisement, observed source, selected scope
  a fresh local discovery generation and an observation revision. A changed advertisement invalidates its old
  ID. Replaying an older advertisement, or returning after expiry, cannot revive
  a previously selected ID, including across local discovery restarts. Raw
  observations never merge cross-source session collisions. OS bundles bind the
  selected interface and session and reject conflicting records; they cannot
  attribute records to distinct packet sources. An alias never establishes identity.
- Sessions last at most 120 seconds; raw candidate TTL is at most 30 seconds from
  each matching live observation. OS cached hints stay within active exact queries
  and the immutable session deadline. Neither path revives invalidated or expired
  IDs; at most 64 candidates are retained. Raw datagrams are at most
  1,200 bytes. DNS counts/types/TXT fields are bounded before interpretation.
  The 512th received datagram stops the session, including malformed traffic;
  at most 16 packets per second are parsed. At most 64 datagrams are sent.
- Initial announcements/queries and periodic advertisements (5 seconds),
  queries (15 seconds), and multicast responses (at most one per second) all
  share the finite send budget. No response is sent to an advertised endpoint.
- Cancel and expiry immediately invalidate candidates and request socket closure,
  including during blocked I/O. Successful shutdown joins all owned goroutines
  and stops timers. `Close` waits at most two seconds and retains the first close
  failure; `ErrCloseUncertain`/`ErrCloseUnresolved` require retaining the owner
  and blocking replacement. `Start` may return a nonnil Session with an error
  after uncertain constructor rollback; callers must preserve that owner. No
  candidate data is persisted. Transport `Close` must unblock its reads/writes.

## Native platform boundary

Advertised/bootstrap endpoints are IPv4-only. Raw transport is IPv4-only; the
Darwin system API does not attest the daemon packet family or wire metadata.
It does not change interface flags, routing, firewall rules, credentials or
permissions. Every startup option/bind failure fails closed. Interface binding
metadata, local address and exact prefix are rechecked before sending and after
receiving; changed bindings stop the session.

- Linux binds exactly `224.0.0.251:5353` through a native socket, disables `IP_MULTICAST_ALL`, joins only
  the selected interface, and uses x/net IPv4 packet metadata for ingress and
  exact local source/interface selection on every send.
- The diagnostic-only raw Darwin factory keeps an exact-group receive socket and a separate sender bound to
  the exact selected local address on port 5353. Both use `IP_BOUND_IF`; the
  sender also fixes the multicast interface, TTL 255 and multicast loopback.
  Source-only pktinfo preserves the exact source, while the sticky bound
  interface confines egress. Any partial setup failure closes both resources;
  uncertainty retains the complete owner and prevents fallback or replacement.
  Physical close workers must join before `Done`; finite waits at Session and
  constructor boundaries retain unfinished close attempts without retrying them.
- Windows binds exactly the selected local address on port 5353. It uses the
  socket-local `IP_IFLIST` with one interface, one group membership, and
  `IP_PKTINFO`/TTL metadata. `ReadMsgUDP` retains strict receive metadata;
  `WriteToUDP` uses the exact bound source with selected-index `IP_MULTICAST_IF`
  and TTL 255. Both multicast egress and membership use the documented 24-bit
  network-order interface index, including when interfaces share a local IP.
  Zero or unrepresentable indices are unsupported. Each send revalidates binding, has a one-second deadline, and
  requires the full payload count without retry. The ancillary codec uses bounded byte operations,
  without `unsafe`. Required options may be unavailable on older Windows.
- Raw Unix sockets use `net.FilePacketConn` only after the exact native bind because Go's
  ordinary UDP listener rewrites multicast bind addresses to a wildcard before
  its control callback. The original descriptor is close-on-exec/nonblocking;
  both the original and Go-owned duplicate have explicit rollback ownership.
  A failed original close prevents successful startup and retains uncertainty.
- No raw path retries a wildcard bind, global membership or another NIC.
  The Darwin system provider does not bind an application UDP5353 socket.
  Unsupported or blocked discovery must leave Core's manual/QR setup available.

The protocol/store/lifecycle tests use synthetic transports and a fake clock;
they do not discover surrounding devices. Platform test-binary compilation is
not native multicast acceptance. Native socket delivery and confinement,
physical-device discovery, real pairing, and application compatibility remain
unverified until separately recorded tests run on the exact release source.

Platform implementation evidence:

- Pinned x/net IPv4 source: `sys_darwin.go`, `payload_cmsg.go`,
  `control_windows.go`, `sys_windows.go`. Windows x/net does not supply the
  required controls, so the Windows adapter uses public Go UDP I/O.
- Pinned Go Windows poll implementation: `internal/poll/fd_windows.go`.
- Microsoft [IPPROTO_IP options](https://learn.microsoft.com/en-us/windows/win32/winsock/ipproto-ip-socket-options),
  [IP_MREQ interface selection](https://learn.microsoft.com/en-us/windows/win32/api/ws2ipdef/ns-ws2ipdef-ip_mreq),
  [IN_PKTINFO](https://learn.microsoft.com/en-us/windows/win32/api/ws2ipdef/ns-ws2ipdef-in_pktinfo),
  and [SDK definitions](https://github.com/microsoft/win32metadata/blob/main/generation/WinSDK/RecompiledIdlHeaders/shared/ws2ipdef.h).

## 日本語

このパッケージは、明示的に選んだ一つの範囲で、短時間だけ mDNS 探索を行う。
候補は未認証の手掛かりであり、相手の identity、権限、証明書 pin、信頼、接続成功
の証明ではない。Core が bootstrap 待受への同意と寿命を管理し、選択した候補 ID
を直前に再確認してから、その正確な端点を別途認証する。

- 広告は一時的な 256-bit session ID、利用者が選んだ alias、固定 protocol と
  capability、数値 bootstrap 端点だけ。alias は探索中に選択範囲へ公開される。
  OS hostname、恒久 identity、招待、pin、ファイル情報は含まない。
- raw packet の送信元と広告端点は、それぞれ選択 prefix 内である必要がある。
  interface、宛先、送信元 port、TTL と record の観測由来を検証するが、広告端点の
  所有者や送信元の真正性は保証しない。OS 観測は選択 interface と継続中の正確な
  query に結び付け、packet metadata を捏造しない。
- 候補の変更・失効で古い選択 ID は無効になる。古い広告が再送されても ID は
  復活しない。raw 観測は送信元の違う同一 session を統合しない。OS bundle は
  選択 interface と session に結び付けて矛盾する record を拒否するが、異なる
  packet 送信元を識別できない。alias の一致を identity の証明にしない。
- session は最大120秒、raw 候補は同じ有効な広告の最終観測から最大30秒、最大64件。
  OS cache の候補は正確な query の継続中かつ元の session 期限内だけ保持する。
  どちらも失効・無効化した ID を復活させない。以下の packet 上限は raw 経路のもの。
  datagram は最大 1,200 bytes、受信 512 件で停止、解析は毎秒最大 16 件、
  送信は session 全体で最大 64 件。応答は multicast のみで毎秒最大 1 回。
- Cancel・期限切れは候補を即座に無効化して I/O の終了を要求する。正常終了は
  goroutine と timer の終了まで確認する。Close は最大 2 秒で、不確定なら owner を
  保持して再開始を止める。Start が owner と error の両方を返した場合も owner を
  捨てない。候補は保存せず、能動 bootstrap 接続はこの package の対象外。

Core は同意後に StartNative を使う。Linux は正確な multicast group bind と選択
interface、Windows は選択したローカル数値 address bind と interface 制限を用いる。
macOS+cgo は選択 interface のシステム DNS-SD API を使う。広告端点は IPv4 だが、
API は daemon の packet family や wire metadata を証明しない。詳細は
[API 契約](DNSSD.ja.md)を参照。cgo なしは unavailable とする。
raw 経路を wildcard bind、別 NIC、全 IP/port scan、OS/FW/経路変更へ拡張しない。
安全な socket を作れない場合は停止し、Core の手動/QR 導線を利用できる状態にする。
Unix では Go の通常 UDP listener が multicast bind を wildcard に変更するため、
native socket を正確な group に bind した後だけ `net.FilePacketConn` に渡す。
診断専用の旧 macOS raw factory は受信用の正確な group bind と送信用の選択 address:5353 bind を分離し、
両方に `IP_BOUND_IF` を設定する。送信は固定 multicast interface・TTL255 と、
source-only pktinfo を用い、選択 address と interface を維持する。片方の準備失敗も
両方を終了し、不確定なら全 owner を保持して代替・再開始を止める。
実際の close worker が終了する前に `Done` を閉じない。Session と constructor の
有限待機を超えた close は owner に保持し、同じ descriptor の終了を再試行しない。
元の descriptor と Go が複製した descriptor を個別に管理し、元の終了が不確定なら
起動成功とせず owner を保持する。close-on-exec・nonblocking を必須とする。

模擬 transport・偽時計の試験や他 OS 向けコンパイルは、native multicast の実動
確認ではない。native 通信と範囲制限、実機の探索・ペアリング・アプリ互換性は、
別途その source の試験結果が記録されるまで未検証として扱う。

## Opt-in raw native loopback capability test

The default suite skips `TestNativeLoopbackDiscoveryCapability` without opening
sockets. A separately authorized native runner can opt in with:

```sh
SOBALINK_NATIVE_NEARBY_LOOPBACK=1 go test -count=1 -run '^TestNativeLoopbackDiscoveryCapability$' -v ./internal/nearby
```

This test selects only an up loopback interface owning exactly `127.0.0.1`, with
a test scope of `127.0.0.1/32`. It exercises the same socket adapter, bounded
wire parser and session owners using a private test-only construction seam.
The exported production API still rejects loopback. No interface flags, routes,
firewall rules or global network settings are changed; a missing multicast
interface flag is not changed to make the test work. No other NIC is tried.
The two synthetic advertisements carry distinct random sessions and high-port
endpoint hints; no bootstrap TCP listener or connection is started.

Linux retains its production exact-group mDNS socket. Darwin retains the
exact-group receiver plus exact-selected-address sender, and Windows retains
its exact selected-address socket. The required mDNS port remains 5353; bind
conflicts or unsupported native options produce `unavailable`, not a wildcard
fallback. Both native transports are fully opened before announcements (Darwin owns
four sockets across the two transports); observation is
limited to eight seconds and owner cleanup/join to two seconds.

A `NEARBY_NATIVE_CAPABILITY` JSON marker reports `verified`, `unavailable`,
`not_run`, or `failed`. Only `verified`, with bidirectional current candidates
and both owners joined, counts as capability evidence. A skipped test can still
leave the enclosing Go package with a `PASS` exit status: **a runner must check
the marker, not that status**. Foreign/missing packet metadata and unresolved
cleanup fail the test. A bind/receive limitation is explicitly unavailable. The additional
`socketFailureStage` field is a fixed enum (`none` when there was no socket
failure); it contains no raw platform errors, interface identifiers or addresses.

Even a verified result proves only exact loopback adapter behavior on that
native target. It does not prove physical-NIC multicast, multi-device discovery,
bootstrap authentication, application compatibility or the manual fallback.
Those acceptance results remain separate.

### native loopback 試験について

通常の試験では socket を開かず、この試験を skip する。別途許可された native
runner だけが上の環境変数で実行する。対象は実際に `127.0.0.1` を持つ up 状態の
loopback interface 一つと `/32` の試験範囲だけ。製品 API の loopback 拒否は変えず、
interface flag・経路・FW・全体設定を変更しない。別 NIC への代替や TCP 接続はない。

専用 JSON marker の `verified` と、双方向の候補確認・両 owner の終了確認だけを
能力試験の根拠にする。`unavailable` / `not_run` による skip は、Go package 全体が
`PASS` と表示されても合格証拠ではない。metadata の範囲不一致や終了不能は失敗。
`socketFailureStage` は固定 enum で失敗段階を示し、失敗がなければ `none` とする。
OS の生 error、interface の識別情報、address は出力しない。
loopback 上の合格は、実 NIC・別端末の探索、bootstrap 認証、手動導線の合格とは別。

## Platform diagnosis boundary

The diagnostic-only raw Darwin sender follows the published
[XNU 12377.1.9 UDP source](https://github.com/apple-oss-distributions/xnu/blob/xnu-12377.1.9/bsd/netinet/udp_usrreq.c#L1401-L1739):
bound-source validation precedes temporary source selection, and a nonzero
pktinfo interface index overrides the requested source address. This source
analysis justifies the correction; it does not prove the exact hosted error or
establish native acceptance by itself.

Darwin bind failures additionally carry fixed `bindSocketRole` and
`bindFailureKind` values, separating the group receiver from the local sender
without printing an address, identifier or raw errno. Address construction uses
the selected IPv4 bytes and port 5353 with checked ownership. The pinned
[XNU bind rules](https://github.com/apple-oss-distributions/xnu/blob/xnu-12377.1.9/bsd/netinet/in_pcb.c#L948-L999)
can reject nonmulticast exact-local port sharing across different socket-owner
UIDs despite reuse options. This is a possible coexistence limit, not an
established cause of a particular run. A confirmed conflict is a platform/design
blocker; it does not authorize changing services, ports or bind scope.

On Windows, the pinned Go 1.27.1 `WriteMsgInet4` passes no synchronous byte-count
pointer to `WSASendMsg`. Its immediate-success path can therefore report zero
bytes; this does not establish whether that message was transmitted. A bounded
test-only `WriteToUDP` probe verified exact metadata and cleanup on the same
loopback scope. Production now uses that exact-bound send primitive, retaining
full-byte-count checks. Only a new reciprocal Session result can verify the
corrected production path; the probe alone cannot.

The fixed `sendFailureKind` distinguishes known OS error categories from short
payload/control counts without emitting raw errors or numeric codes. The closed
`windowsNoAncillaryProbe` schema retains the earlier test-only observation and
its non-upgrade/safety checks. Current runs emit null: the extra probe was retired
when production adopted the same send primitive, avoiding a redundant retry.
Its historical result never establishes production discovery acceptance.

A Linux result cannot establish Darwin or Windows support. A target reporting
`unavailable` has no automatic-discovery proof, even if its diagnostic job is
green. Loopback discovery, manual-contact Core/browser setup and physical-LAN
automatic setup remain distinct acceptance results.

### プラットフォーム別の診断範囲

診断専用の旧 macOS raw 経路の送受信分離は上記の固定 XNU source の bound-source 検証順序と、pktinfo の
interface 指定が source 指定に優先する動作に基づく修正である。source 調査だけで
実行環境の error 原因や native 動作確認の完了を断定しない。

bind 失敗は `bindSocketRole` と `bindFailureKind` の固定値で受信 group / 送信 local
socket と error 分類を区別し、address・識別子・生の errno は出力しない。
選択 IPv4 と port 5353、既存の所有権確認は維持する。上記 XNU bind 規則では、
所有 UID が異なる既存 socket との正確な local address の port 共有が reuse 設定下
でも拒否され得る。これは共存制限の候補であり、個別実行の原因を断定するものではない。
競合が確認された場合はプラットフォーム・設計上の blocker とし、service・port・bind
範囲の変更で代替しない。

Windows の固定 Go 1.27.1 `WriteMsgInet4` は `WSASendMsg` に同期 byte count の出力先を
渡さず、即時成功時に零 byte を返し得る。実際に未送信だったとは断定できない。
同じ loopback 範囲の `WriteToUDP` 補助試験では正確な metadata と終了を確認したため、
製品経路もこの exact bind の送信 primitive を使用する。送信直前の binding 確認、
送信・membership とも network byte order の24-bit interface index で指定し、同じ
local IP を持つ別 interface と区別する。零・表現範囲外の index は unsupported とする。
TTL 255、一秒 deadline、全 byte count 必須、retry なしを維持する。
修正後の製品確認には新しい双方向 Session 結果が必要で、補助試験だけでは完了としない。

`sendFailureKind` は既知の OS error 分類と payload/control の不足だけを固定値で示す。
従来の試験専用結果を扱う `windowsNoAncillaryProbe` の閉じた schema と、製品確認へ
昇格させない検証は維持する。製品が同じ送信 primitive を採用したため、追加 probe は
廃止し、現在の実行では null とする。同一経路の冗長な再送は行わない。
過去の補助結果を製品探索の動作確認として扱わない。

Linux の結果を macOS/Windows の対応証拠として流用しない。`unavailable` は job が
緑でも自動探索未確認である。loopback 探索、手動 contact の Core/browser 接続、
実 LAN の自動セットアップはそれぞれ別の確認結果として扱う。
