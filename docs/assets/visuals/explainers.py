from pathlib import Path
from html import escape
import runpy
D=Path(__file__).parent
base=runpy.run_path(str(D/'build.py'));PALETTES=base['PALETTES']
DATA={
'permissions':{
'ja':{'title':'つながることと、許可すること。','narrow_title':['つながることと、','許可すること。'],'sub':'接続・受信・サービス共有は、それぞれ確認します。','cards':[
 ['接続','ネットワークへ参加',['選んだ経路で','相手と通信する準備'],['信頼・共有の許可とは別']],
 ['受信','相手を信頼',['メッセージの受信と','ファイルの申込みを許可'],['通常はファイルをバッチごとに承認']],
 ['共有','サービスを許可',['相手・ポート・期限を','指定して共有'],['メッセージ・ファイルの信頼とは別']]],
 'foot':['受信の許可は、受け取る側が決めます。','自動保存は、相手と保存先を選んで別途有効にします。'],
 'desc':'接続の準備、メッセージとファイルの送信元に対する信頼、サービスの共有許可は別の判断です。ネットワークへの参加だけでは信頼やサービス共有は許可されません。ファイル保存は通常バッチごとの受信同意が必要です。自動保存は別途明示的に設定します。'},
'en':{'title':'Connectivity and permission are separate choices.','narrow_title':['Connectivity and permission','are separate choices.'],'sub':'Review network access, receiving and service sharing independently.','cards':[
 ['CONNECT','Join a network',['Prepare a selected path','to communicate with a peer'],['Separate from trust and sharing']],
 ['RECEIVE','Trust a peer',['Allow this sender’s','messages and file offers'],['Accept each file batch by default']],
 ['SHARE','Allow a service',['Choose the peers, ports','and permission lifetime'],['Separate from message/file trust']]],
 'foot':['The receiving device controls who may send to it.','Autosave is a separate choice tied to a peer and destination.'],
 'desc':'Network connectivity, trust for message and file offers, and service-sharing permission are independent choices. Joining a network does not authorize trust or service sharing. File batches require receiver acceptance by default. Autosave requires a separate explicit configuration.'}},
'saved-and-running':{
'ja':{'title':'保存した設定は、起動して使う。','narrow_title':['保存した設定は、','起動して使う。'],'sub':'保存済みサービスを手動で開始する場合','cards':[
 ['1 · 設定','保存済み',['相手・サービス・期限を','設定として保存'],['保存だけでは開始しない']],
 ['2 · 操作','内容を確認して起動',['現在のネットワークと','相手の識別情報を再確認'],['共有は許可内容も確認']],
 ['3 · 状態','開始済み',['許可された範囲で','通信の準備ができた状態'],['アプリで接続・認証を確認']]],
 'foot':['開始済みは、アプリの成功や処理完了の証明ではありません。','起動時の自動接続は、別途確認・承認する設定です。'],
 'desc':'保存したサービス定義は、それだけでは待受や以前の許可を再開しません。手動で起動すると、現在のネットワークと相手の識別情報を再確認します。共有は許可内容を確認します。開始済みはアプリのログインや遠隔処理の完了を示しません。個別に承認した起動時接続は別の設定です。'},
'en':{'title':'Save the setup. Start it when you need it.','narrow_title':['Save the setup.','Start it when you need it.'],'sub':'Manually starting a saved service','cards':[
 ['1 · CONFIGURATION','Saved definition',['Keep the peer, service','and lifetime settings'],['Saving alone starts nothing']],
 ['2 · ACTION','Review and start',['Recheck the current network','and peer identity'],['Review sharing permissions too']],
 ['3 · STATE','Started',['Ready to communicate','within the approved scope'],['Check your app’s login and use']]],
 'foot':['A started service does not prove application success or remote job completion.','Automatic outbound startup uses separately reviewed approvals.'],
 'desc':'Saving a service definition does not open listeners or restore a previous permission. A manual start rechecks the current network and peer identity, with review for shares. A started service does not prove an application login or remote job completed. Separately approved outbound startup is a different setting.'}},
'delivery-evidence':{
'ja':{'title':'受付から、相手の保存確認へ。','narrow_title':['受付から、','相手の保存確認へ。'],'sub':'永続ファイル送信待ちの例：期限内・両端末が起動し接続可能な場合','cards':[
 ['送信側','この端末で受付',['確認した送信を','この端末に記録'],['相手の保存は、まだ未確認']],
 ['受信側','相手の端末に保存',['受信の同意・転送後に','相手の保存先へ保存'],['送信側に結果が届かない場合も']],
 ['送信側の確認','保存の記録を確認',['相手の永続的な記録で','保存を確認'],['既読を示すものではない']]],
 'foot':['到着状況が不明でも、すでに保存済みの場合があります。','元の送信記録を確認します。新しい送信を安易に作り直しません。'],
 'desc':'永続送信待ちへの受付は送信側での記録であり、相手への配達完了ではありません。受信承認または明示設定済みの自動保存により転送後のファイルが保存され、相手の永続的な記録によって送信側が保存を確認します。結果が不明でも保存済みの可能性があるため、元の要求を確認し、新しい要求で安易に再送しません。保存確認は既読や現在もファイルが残ることの保証ではありません。'},
'en':{'title':'From local acceptance to a confirmed save.','narrow_title':['From local acceptance','to a confirmed save.'],'sub':'Durable file queue: within the deadline, with both devices running and reachable','cards':[
 ['SENDING DEVICE','Accepted locally',['Record the reviewed send','on this device'],['Remote save is not yet confirmed']],
 ['RECEIVING DEVICE','Saved on the peer',['After receiver consent','and transfer, save the files'],['The reply may not reach the sender']],
 ['SENDER’S CONFIRMATION','Save confirmed',['Verify the receiver’s','durable saved evidence'],['This is not a read receipt']]],
 'foot':['An unknown delivery result may still mean the files were saved.','Check the original send record. Do not blindly create a replacement send.'],
 'desc':'Acceptance into the durable outbox records a send on the sending device; it does not prove delivery. After receiver acceptance or explicitly configured autosave, files are transferred and saved. Durable receiver evidence confirms the save to the sender. An unknown result may already be saved, so query the original request rather than creating a replacement. Saved evidence is not a read receipt or a guarantee that the file still exists.'}}
}
def make(kind,lang,theme,narrow):
 c=DATA[kind][lang];p=PALETTES[theme];W,H=(360,872) if narrow else (1040,540);z=[]
 z.append(f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" width="{W}" height="{H}" role="img" aria-labelledby="title desc" xml:lang="{lang}"><title id="title">{escape(c["title"])}</title><desc id="desc">{escape(c["desc"])}</desc>')
 z.append('<style>text{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","Hiragino Sans","Yu Gothic UI","Noto Sans CJK JP","Noto Sans JP",sans-serif}</style>')
 def rect(x,y,w,h,fill,rx=12):z.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{rx}" fill="{fill}"/>')
 def text(x,y,s,size=16,color=None,weight=400):z.append(f'<text x="{x}" y="{y}" font-size="{size}" fill="{color or p["text"]}" font-weight="{weight}">{escape(s)}</text>')
 def arrow(x,y,vertical=False):
  d=f'M{x} {y}v16m-4-5 4 5 4-5' if vertical else f'M{x} {y}h16m-5-4 5 4-5 4'
  z.append(f'<path d="{d}" fill="none" stroke="{p["line"]}" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"/>')
 rect(0,0,W,H,p['bg'],0)
 pad=24 if narrow else 32
 z.append(f'<g transform="translate({pad} 25) rotate(-23 12 12)" fill="{p["accent"]}" aria-hidden="true"><rect width="6" height="24" rx="3"/><rect x="9" y="5" width="6" height="16" rx="3"/><rect x="18" y="2" width="6" height="22" rx="3" opacity=".6"/></g>')
 text(pad+36,45,'sobalink',22,weight=650)
 if narrow:
  for i,s in enumerate(c['narrow_title']):text(24,100+i*33,s,22,weight=650)
  # Small-screen subtitle is intentionally shorter, not a shrunk long line.
  subs={'permissions':{'ja':['接続・受信・共有は、個別に確認。'],'en':['Review each permission independently.']},'saved-and-running':{'ja':['保存済みサービスを手動で開始する場合'],'en':['Manually starting a saved service']},'delivery-evidence':{'ja':['ファイル・フォルダーの永続送信待ち','期限内・両端末が起動し接続可能な場合'],'en':['Durable queue · Within the deadline','Both devices must be running and reachable']}}
  for j,sub in enumerate(subs[kind][lang]):text(24,160+j*22 if kind=='delivery-evidence' else 168,sub,13 if kind=='delivery-evidence' else 14,p['muted'])
  for i,(eyebrow,title,body,tail) in enumerate(c['cards']):
   x=16;y=(210 if kind=='delivery-evidence' else 192)+i*185;rect(x,y,328,162,p['panel'],16)
   text(32,y+27,eyebrow,13,p['accenttext'],600);text(32,y+58,title,20,weight=650)
   for j,s in enumerate(body):text(32,y+86+j*22,s,15,p['muted'])
   text(32,y+140,tail[0],13,p['accenttext'],500)
   if i<2 and kind!='permissions':arrow(180,y+165,True)
  # Hand-wrapped localizations keep footnotes readable at 320px.
  foot={
   'permissions':{'ja':['受信の許可は、受け取る側が決めます。','自動保存は、相手と保存先を選んで','別途有効にします。'],'en':['The receiving device controls its permissions.','Autosave is a separate choice tied to','a peer and destination.']},
   'saved-and-running':{'ja':['開始済みは、アプリの成功や','処理完了の証明ではありません。','起動時の自動接続は別途承認する設定です。'],'en':['A started service does not prove app success','or completion of a remote job.','Outbound startup needs separate approval.']},
   'delivery-evidence':{'ja':['結果が不明でも、保存済みの場合があります。','元の送信記録を確認します。','新しい送信を安易に作り直しません。'],'en':['An unknown result may already be saved.','Check the original send record.','Do not blindly create a replacement send.']}}
  for i,s in enumerate(foot[kind][lang]):text(24,772+i*25,s,13,p['muted'])
 else:
  text(32,108,c['title'],30,weight=650);text(32,144,c['sub'],17,p['muted'])
  for i,(eyebrow,title,body,tail) in enumerate(c['cards']):
   x=32+i*336;y=188;rect(x,y,304,238,p['panel'],16)
   text(x+20,y+34,eyebrow,14,p['accenttext'],600);text(x+20,y+78,title,22,weight=650)
   for j,s in enumerate(body):text(x+20,y+118+j*27,s,17,p['muted'])
   rect(x+20,y+175,264,41,p['soft'],8);text(x+28,y+201,tail[0],14,p['accenttext'],500)
   if i<2 and kind!='permissions':arrow(x+312,y+118)
  z.append(f'<path d="M32 455H1008" stroke="{p["border"]}"/>')
  for i,s in enumerate(c['foot']):text(32,485+i*27,s,15,p['muted'])
 z.append('</svg>');return '\n'.join(z)
for kind in DATA:
 for lang in ['ja','en']:
  for theme in PALETTES:
   for narrow in [False,True]:
    name=f'{kind}-{lang}-{theme}-'+('narrow' if narrow else 'wide')
    (D/(name+'.svg')).write_text(make(kind,lang,theme,narrow))
print('Generated 24 localized and themed explainer SVGs.')
# Keep the embeddable previews and descriptions derived from the same copy source.
import json
manifest=[]
for kind in DATA:
 for lang in ['ja','en']:
  c=DATA[kind][lang]
  html=f'''<!doctype html><html lang="{lang}"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{escape(c['title'])}</title><style>body{{margin:0;padding:16px;background:#fff;color:#272c38;font-family:system-ui,sans-serif}}main{{max-width:1040px;margin:auto}}picture,img{{display:block;width:100%;height:auto}}@media(max-width:839px){{picture{{max-width:440px;margin:auto}}}}@media(prefers-color-scheme:dark){{body{{background:#1d202a;color:#e8e9f0}}}}</style><main><picture><source media="(max-width:839px) and (prefers-color-scheme:dark)" srcset="{kind}-{lang}-dark-narrow.svg"><source media="(max-width:839px)" srcset="{kind}-{lang}-light-narrow.svg"><source media="(prefers-color-scheme:dark)" srcset="{kind}-{lang}-dark-wide.svg"><img src="{kind}-{lang}-light-wide.svg" alt="{escape(c['desc'])}"></picture></main></html>'''
  (D/f'{kind}-preview-{lang}.html').write_text(html)
  manifest.append({'topic':kind,'language':lang,'title':c['title'],'alt':c['desc'],'variants':[f'{kind}-{lang}-{theme}-{layout}.svg' for theme in PALETTES for layout in ['wide','narrow']]})
(D/'explainer-manifest.json').write_text(json.dumps(manifest,ensure_ascii=False,indent=2))
