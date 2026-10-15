from pathlib import Path
from html import escape
import json
D=Path(__file__).parent
PALETTES={'light':dict(bg='#ffffff',panel='#f8f8fb',text='#272c38',muted='#596172',border='#e6e7ed',accent='#6550d8',soft='#f0edff',accenttext='#5844bf',line='#7f8799'), 'dark':dict(bg='#1d202a',panel='#191c24',text='#e8e9f0',muted='#bdc5d8',border='#343846',accent='#a18bff',soft='#30294b',accenttext='#c0aeff',line='#818ba0')}
COPY={'ja':dict(title='選んだサービスを、もう一方の端末へ。',sub='sobalink 同士でサービスを共有する場合の接続イメージ',local='この端末',remote='相手の端末',app='使うアプリ',service='共有サービス',route='接続経路',route2='Direct / Relay',localnote='アプリには実際の接続先を設定',remotenote='相手・ポート・期限を指定して許可',foot1='選んだネットワークに、到達可能な経路が必要です。',foot2='アプリの起動・認証は別途必要です。接続の自動成功を保証する図ではありません。',desc='この端末のアプリからローカルの sobalink に接続し、選択したネットワークの経路を通り、相手の sobalink が許可を確認して共有サービスへ転送する例。通信は双方向です。Direct と Relay は経路を表し、利用できる経路は設定とネットワーク条件に依存します。'), 'en':dict(title='The service you choose. The connection you need.',sub='A service-sharing example between two sobalink devices',local='This device',remote='Peer device',app='Your app',service='Shared service',route='Network path',route2='Direct / Relay',localnote='Use the actual endpoint in your app',remotenote='Allow specific peers, ports and lifetime',foot1='Your selected network needs a reachable path.',foot2='Run and authenticate to the application separately. Connectivity is not guaranteed.',desc='An application connects to the local sobalink listener. Traffic crosses the selected network path to the peer sobalink, which checks sharing permissions and forwards to the service. Traffic is bidirectional. Direct and Relay describe routes; availability depends on configuration and network conditions.')}
def make(lang,theme,narrow):
 c=COPY[lang]; p=PALETTES[theme]; W,H=(360,816) if narrow else (1040,520); z=[]
 z.append(f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {W} {H}" width="{W}" height="{H}" role="img" aria-labelledby="title desc" xml:lang="{lang}">')
 z.append(f'<title id="title">{escape(c["title"])}</title><desc id="desc">{escape(c["desc"])}</desc>')
 z.append('<style>text{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","Hiragino Sans","Yu Gothic UI","Noto Sans CJK JP","Noto Sans JP",sans-serif} </style>')
 def rect(x,y,w,h,fill,rx=12,stroke=None): z.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{rx}" fill="{fill}"'+(f' stroke="{stroke}"' if stroke else '')+'/>')
 def text(x,y,s,size=16,color=None,weight=400,anchor='start'): z.append(f'<text x="{x}" y="{y}" font-size="{size}" fill="{color or p["text"]}" font-weight="{weight}" text-anchor="{anchor}">{escape(s)}</text>')
 def line(x1,y1,x2,y2):
  z.append(f'<path d="M{x1} {y1} L{x2} {y2}" fill="none" stroke="{p["line"]}" stroke-width="1.7"/>')
  if y1==y2:
   z.append(f'<path d="m{x1+5} {y1-4}-5 4 5 4 M{x2-5} {y2-4} l5 4-5 4" fill="none" stroke="{p["line"]}" stroke-width="1.7"/>')
  else: z.append(f'<path d="m{x1-4} {y1+5} 4-5 4 5 M{x2-4} {y2-5} l4 5 4-5" fill="none" stroke="{p["line"]}" stroke-width="1.7"/>')
 def mark(x,y):
  z.append(f'<g transform="translate({x} {y}) rotate(-23 12 12)" fill="{p["accent"]}" aria-hidden="true"><rect x="0" y="0" width="6" height="24" rx="3"/><rect x="9" y="5" width="6" height="16" rx="3"/><rect x="18" y="2" width="6" height="22" rx="3" opacity=".6"/></g>')
 def node(x,y,w,label,brand=False):
  rect(x,y,w,100,p['soft'] if brand else p['bg'],10,None if brand else p['border'])
  if brand: mark(x+w/2-12,y+18)
  else:
   # Reuse the product's existing monitor glyph geometry, not a new icon family.
   z.append(f'<g transform="translate({x+w/2-12} {y+17})" fill="none" stroke="{p["muted"]}" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M3 4h18v13H3zM8 21h8M12 17v4"/></g>')
  text(x+w/2,y+74,label,16,p['accenttext'] if brand else p['text'],600,'middle')
 rect(0,0,W,H,p['bg'],0)
 pad=24 if narrow else 32
 mark(pad,25);text(pad+36,45,'sobalink',22,weight=650)
 if narrow:
  titlelines=['選んだサービスを、','もう一方の端末へ。'] if lang=='ja' else ['The service you choose.','The connection you need.']
  for i,s in enumerate(titlelines):text(24,100+i*33,s,24,weight=650)
  text(24,168,'サービス共有の接続イメージ' if lang=='ja' else 'Service sharing between two devices',14,p['muted'])
  for y,title,reverse,note in [(196,c['local'],False,c['localnote']),(508,c['remote'],True,c['remotenote'])]:
   rect(16,y,328,204,p['panel'],16)
   text(32,y+32,title,16,p['muted'],600)
   node(32,y+51,132,'sobalink' if reverse else c['app'],reverse)
   node(196,y+51,132,c['service'] if reverse else 'sobalink',not reverse)
   line(171,y+101,189,y+101)
   text(32,y+181,note,13,p['muted'])
  line(180,407,180,430);line(180,478,180,501)
  rect(61,437,238,34,p['soft'],16)
  text(180,460,c['route']+' · '+c['route2'],14,p['accenttext'],500,'middle')
  text(24,749,'到達可能な経路が必要です。' if lang=='ja' else 'A reachable network path is required.',14,p['muted'])
  text(24,776,'アプリの起動・認証は別途必要です。' if lang=='ja' else 'Run and authenticate to your app separately.',13,p['muted'])
 else:
  text(32,108,c['title'],30,weight=650);text(32,144,c['sub'],17,p['muted'])
  for x,title,reverse,note in [(32,c['local'],False,c['localnote']),(656,c['remote'],True,c['remotenote'])]:
   rect(x,186,352,216,p['panel'],16)
   text(x+20,221,title,17,p['muted'],600)
   node(x+20,240,136,'sobalink' if reverse else c['app'],reverse)
   node(x+196,240,136,c['service'] if reverse else 'sobalink',not reverse)
   line(x+164,290,x+188,290)
   text(x+20,377,note,15,p['muted'])
  line(393,290,416,290);line(624,290,647,290)
  rect(424,240,192,100,p['soft'],12)
  text(520,281,c['route'],18,p['accenttext'],600,'middle');text(520,312,c['route2'],16,p['muted'],400,'middle')
  z.append(f'<path d="M32 432H1008" stroke="{p["border"]}"/>')
  text(32,464,c['foot1'],16,p['muted']);text(32,492,c['foot2'],14,p['muted'])
 z.append('</svg>');return '\n'.join(z)
for lang in COPY:
 for theme in PALETTES:
  for narrow in [False,True]:
   name=f'connection-{lang}-{theme}-'+('narrow' if narrow else 'wide')
   (D/(name+'.svg')).write_text(make(lang,theme,narrow))

# Spatial overview for wide layouts; retain the approved narrow composition above.
from pathlib import Path
from html import escape
D=Path(__file__).parent
palettes={'light':dict(bg='#ffffff',surface='#f8f8fb',ink='#272c38',muted='#596172',line='#7f8799',border='#e6e7ed',purple='#6550d8',soft='#f0edff',ptext='#5844bf'),'dark':dict(bg='#1d202a',surface='#191c24',ink='#e8e9f0',muted='#bdc5d8',line='#818ba0',border='#343846',purple='#a18bff',soft='#30294b',ptext='#c0aeff')}
copy={'ja':dict(title='使いたいサービスだけを、もう一方の端末へ。',sub='sobalink 同士でサービスを共有する仕組みの例',left='この端末',right='相手の端末',app='使うアプリ',service='共有するサービス',route='通信の経路',permit='共有の許可',l1='実際の接続先を',l2='アプリに設定',r1='相手・ポート・期限を',r2='指定して許可',foot1='到達可能な経路が必要です。アプリの起動・認証は別途行います。',foot2='線は通信の仕組みを示します。実際の接続状態や自動接続の保証ではありません。',desc='概念図。左の端末のアプリがローカルのsobalinkに接続します。選んだネットワークのDirectまたはRelay経路を通り、右の端末のsobalinkが現在の相手・ポート・期限の共有許可を確認して、共有サービスへ通信を転送します。線上の盾はこの許可確認を示します。通信は双方向です。到達可能な経路とアプリの起動・認証は別途必要です。'), 'en':dict(title='Bring the service you choose within reach.',sub='An illustrated service-sharing example between two sobalink devices',left='This device',right='Peer device',app='Your app',service='Shared service',route='Network path',permit='Sharing permission',l1='Use the actual endpoint',l2='in your app',r1='Allow specific peers,',r2='ports and lifetime',foot1='A reachable path is required. Run and authenticate to the application separately.',foot2='Lines explain the communication model, not live status or guaranteed connectivity.',desc='Conceptual diagram. An app on the left device connects to local sobalink. Traffic crosses a Direct or Relay path in the selected network. The right device’s sobalink checks the current peer, port and lifetime permissions before forwarding traffic to the shared service. The shield represents this permission check. Traffic is bidirectional. A reachable path and separate application setup and authentication are required.')}
for lang,c in copy.items():
 for theme,p in palettes.items():
  z=[]
  def emit(s):z.append(s)
  def text(x,y,t,size=18,color='ink',weight=400,anchor='start'):emit(f'<text x="{x}" y="{y}" font-size="{size}" fill="{p[color]}" font-weight="{weight}" text-anchor="{anchor}">{escape(t)}</text>')
  def rect(x,y,w,h,fill,rx=12,stroke=None,sw=1):emit(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{rx}" fill="{p[fill]}"'+(f' stroke="{p[stroke]}" stroke-width="{sw}"' if stroke else '')+'/>')
  def path(d,color='purple',sw=2,fill='none'):emit(f'<path d="{d}" stroke="{p[color]}" stroke-width="{sw}" fill="{p.get(fill,fill)}" stroke-linecap="round" stroke-linejoin="round"/>')
  def brand(x,y,scale=1):emit(f'<g transform="translate({x} {y}) scale({scale}) rotate(-23 12 12)" fill="{p["purple"]}"><rect width="6" height="24" rx="3"/><rect x="9" y="5" width="6" height="16" rx="3"/><rect x="18" y="2" width="6" height="22" rx="3" opacity=".6"/></g>')
  emit(f'<svg xmlns="http://www.w3.org/2000/svg" width="1100" height="650" viewBox="0 0 1100 650" xml:lang="{lang}" role="img" aria-labelledby="title desc"><title id="title">{escape(c["title"])}</title><desc id="desc">{escape(c["desc"])}</desc><style>text{{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","Hiragino Sans","Noto Sans CJK JP",sans-serif}}</style>')
  rect(0,0,1100,650,'bg',0);brand(40,28);text(76,48,'sobalink',23,weight=650)
  text(40,112,c['title'],32,weight=650);text(40,148,c['sub'],18,'muted')
  # Device islands group the app and local agent without card-like process boxes.
  emit(f'<ellipse cx="234" cy="432" rx="196" ry="32" fill="{p["surface"]}"/><ellipse cx="866" cy="432" rx="196" ry="32" fill="{p["surface"]}"/>')
  text(205,207,c['left'],19,weight=650,anchor='middle');text(895,207,c['right'],19,weight=650,anchor='middle')
  # Left laptop: a schematic app window, deliberately not a product screenshot.
  rect(69,230,272,164,'surface',15,'line',2);rect(81,242,248,140,'bg',8)
  path('M54 394H356L344 409H66Z','line',2,'surface');path('M168 395h74','line',2)
  # Right desktop screen, visually different device silhouette.
  rect(759,230,272,164,'surface',15,'line',2);rect(771,242,248,140,'bg',8)
  path('M895 395v20m-35 4h70','line',3)
  # Continuous traffic path: within each device, across the selected network, through scope checks.
  path('M205 365v72q0 15 15 15h660q15 0 15-15V365','purple',3)
  # Direction markers are paired: the diagram does not imply one-way traffic.
  for x in [476,624]:path(f'M{x-5} 447l5 5-5 5 M{x-17} 447l-5 5 5 5','purple',1.8)
  # App glyph, a simple generic window with code-like application mark.
  rect(164,265,82,59,'soft',9);path('M164 280h82','purple',1.7);path('M190 292l-7 7 7 7m30-14 7 7-7 7','purple',2)
  text(205,354,c['app'],17,weight=600,anchor='middle')
  # A stack evokes a service without depicting a real product or an installed application.
  rect(854,265,82,20,'soft',6,'purple',1.7);rect(854,291,82,20,'soft',6,'purple',1.7)
  for y in [275,301]:path(f'M864 {y}h4m11 0h25','purple',1.7)
  text(895,354,c['service'],17,weight=600,anchor='middle')
  # The two sobalink instances sit on the same traffic line beside their own device.
  for x in [361,739]:
   emit(f'<circle cx="{x}" cy="452" r="40" fill="{p["bg"]}" stroke="{p["border"]}" stroke-width="2"/>');brand(x-15,437,1.25);text(x,515,'sobalink',18,'ptext',650,'middle')
  text(550,348,c['route'],19,'ptext',650,'middle');text(550,378,'Direct または Relay' if lang=='ja' else 'Direct or Relay',17,'muted',anchor='middle')
  path('M550 394v42','line',1.5)
  # Permission gate on peer-local forwarding, rather than a lock over the entire network.
  emit(f'<circle cx="895" cy="413" r="23" fill="{p["soft"]}"/>')
  path('M895 398l12 5v9c0 8-12 15-12 15s-12-7-12-15v-9Z','purple',1.8)
  path('M889 412l4 4 8-9','purple',1.8)
  text(1006,452,c['permit'],14,'ptext',500,'middle');path('M939 442l-24-19','line',1.2)
  text(80,492,c['l1'],16,'muted');text(80,517,c['l2'],16,'muted')
  text(842,492,c['r1'],16,'muted');text(842,517,c['r2'],16,'muted')
  path('M40 555H1060','border',1)
  text(40,589,c['foot1'],17,'muted');text(40,619,c['foot2'],15,'muted')
  emit('</svg>');(D/f'connection-{lang}-{theme}-wide.svg').write_text('\n'.join(z))
