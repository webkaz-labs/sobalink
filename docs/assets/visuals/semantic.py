"""Document-specific explanatory diagrams. Standard-library, deterministic SVG."""
from pathlib import Path
from html import escape
import math

D = Path(__file__).parent
PALETTES = {
 'light': dict(bg='#ffffff', panel='#f8f8fb', text='#272c38', muted='#596172', border='#e0e2ea', accent='#6550d8', soft='#f0edff', accenttext='#5844bf', line='#7f8799'),
 'dark': dict(bg='#1d202a', panel='#191c24', text='#e8e9f0', muted='#bdc5d8', border='#454959', accent='#a18bff', soft='#30294b', accenttext='#c0aeff', line='#9ba4b7')}

class Canvas:
 def __init__(self, lang, theme, narrow, title, desc, height):
  self.lang=lang; self.p=PALETTES[theme]; self.n=narrow; self.w=400 if narrow else 960; self.h=height
  self.z=[f'<svg xmlns="http://www.w3.org/2000/svg" width="{self.w}" height="{height}" viewBox="0 0 {self.w} {height}" role="img" aria-labelledby="title desc" xml:lang="{lang}">',f'<title id="title">{escape(title)}</title><desc id="desc">{escape(desc)}</desc>', '<style>text{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI","Hiragino Sans","Yu Gothic UI","Noto Sans CJK JP","Noto Sans JP",sans-serif}</style>']
  self.rect(0,0,self.w,height,'bg',0)
  self.text(24,33,'sobalink  /  '+('図解' if lang=='ja' else 'EXPLAINED'),13,'accenttext',600)
  self.text(24,73,title,22 if narrow else 28,'text',650)
 def pick(self,ja,en):return ja if self.lang=='ja' else en
 def rect(self,x,y,w,h,color='panel',r=12,stroke=None):
  self.z.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{r}" fill="{self.p.get(color,color)}"'+(f' stroke="{self.p[stroke]}"' if stroke else '')+'/>')
 def text(self,x,y,s,size=15,color='text',weight=400,anchor='start',limit=None):
  # The label ID records its explicit text-box width for librsvg verification.
  if limit is None:limit=self.w-x-20 if anchor=='start' else 2*min(x,self.w-x)-20
  self.z.append(f'<text x="{x}" y="{y}" font-size="{size}" fill="{self.p[color]}" font-weight="{weight}" text-anchor="{anchor}" id="label-{len(self.z)}-box-{int(limit)}">{escape(s)}</text>')
 def lines(self,x,y,ss,size=15,color='muted',gap=22,limit=None):
  for i,s in enumerate(ss):self.text(x,y+i*gap,s,size,color,limit=limit)
 def path(self,d,color='line',width=2,dash=None):
  self.z.append(f'<path d="{d}" fill="none" stroke="{self.p[color]}" stroke-width="{width}" stroke-linecap="round" stroke-linejoin="round"'+(f' stroke-dasharray="{dash}"' if dash else '')+'/>')
 def arrow(self,x,y,x2,y2,color='accent',dash=None):
  self.path(f'M{x} {y} L{x2} {y2}',color,2,dash); a=math.atan2(y2-y,x2-x)
  self.path(f'M{x2-8*math.cos(a-.5):.1f} {y2-8*math.sin(a-.5):.1f} L{x2} {y2} L{x2-8*math.cos(a+.5):.1f} {y2-8*math.sin(a+.5):.1f}',color,2)
 def circle(self,x,y,r=4,color='accent'):
  self.z.append(f'<circle cx="{x}" cy="{y}" r="{r}" fill="{self.p[color]}"/>')
 def device(self,x,y,label,w=112):
  self.rect(x,y,w,64,'panel',9,'border'); self.rect(x+10,y+10,w-20,34,'soft',3)
  self.path(f'M{x+w/2-12} {y+56}h24 M{x+w/2} {y+46}v10','line',1.5)
  self.text(x+w/2,y+88,label,15,'text',600,'middle',w+35)
 def badge(self,x,y,label,w=140):
  self.rect(x,y,w,30,'soft',15);self.text(x+w/2,y+21,label,13,'accenttext',600,'middle',w-16)
 def foot(self,y,lines):
  self.path(f'M24 {y-20}H{self.w-24}','border',1)
  self.lines(24,y,lines,13,gap=20)
 def finish(self):return '\n'.join(self.z+['</svg>'])


def pairing(lang,theme,n):
 title='依頼する側・受け付ける側。' if lang=='ja' else 'Request here. Approve there.'
 desc=('TOFUの主経路では受信側が範囲と待受を承認し、依頼側が相手の鍵と期限を確認して依頼。完全指紋照合では両端末で独立照合後に明示承認が必要です。受信側がその依頼を承認します。発見は本人確認ではなく、TOFUは初回のなりすましを防ぎません。ペア保存後も接続とアプリ権限は別です。' if lang=='ja' else 'In the TOFU flow the receiver opens a scoped request session. Full fingerprint verification additionally requires independent comparison and explicit approval on both devices. The requester reviews the exact key and deadline, then sends. The receiver approves that request. Discovery is not identity proof; TOFU does not prevent first-use impersonation. Saved pairing, connectivity and application permissions remain separate.')
 c=Canvas(lang,theme,n,title,desc,790 if n else 744); P=c.pick; left,right=(56,344) if n else (200,760)
 c.lines(24,105,[P('近くの接続 · v2 TOFU の流れ','Nearby setup · v2 TOFU flow')],14)
 c.device(left-42,133,P('依頼側','Requester'),84);c.device(right-42,133,P('受信側','Receiver'),84)
 c.path(f'M{left} 234V580','border',2,'4 6');c.path(f'M{right} 234V580','border',2,'4 6')
 # Labels sit between rails, not on the endpoint devices.
 mid=(left+right)/2
 def event(y,num,ja,en,reverse=False):
  c.text(mid,y,P(ja,en),14 if n else 17,'text',600,'middle',right-left-20)
  c.arrow(right if reverse else left,y+18,left if reverse else right,y+18)
  c.circle(right if reverse else left,y+18,12,'soft');c.text(right if reverse else left,y+23,str(num),13,'accenttext',650,'middle',20)
 c.badge(right-(140 if not n else 144),231,P('範囲・待受を承認','Approve scope/listeners'), 250 if not n else 180)
 c.text(mid,302,P('依頼側で、相手の候補を選ぶ','Requester selects a candidate'),14 if n else 17,'text',600,'middle',right-left-20)
 c.circle(left,320,12,'soft');c.text(left,325,'1',13,'accenttext',650,'middle',20)
 c.path(f'M{left+15} 320h12v13h-12','accent',1.5)
 c.text(mid,342,P('名前・IP は本人確認にならない','Name / IP is not identity proof'),12 if n else 14,'muted',anchor='middle',limit=right-left-15)
 event(390,2,'鍵・範囲・期限を確認し依頼','Review key, scope, expiry → request')
 event(465,3,'同じ依頼を確認して承認','Review that request → approve',True)
 c.text(mid,515,P('別の方法: 完全指紋照合を選ぶ場合','Alternative: full fingerprint verification'),13,'accenttext',600,'middle',c.w-48)
 c.text(mid,539,P('両端末で全指紋を独立照合してから、それぞれ承認','Both compare independently, then both approve'),12,'muted',anchor='middle',limit=c.w-48)
 c.rect(24,563,c.w-48,77,'soft',12)
 c.lines(40,589,[P('TOFU は受け入れた鍵を記憶します。','TOFU remembers the accepted key.'),P('初回のなりすましは防げません。','It cannot prevent first-use impersonation.')],14,'accenttext')
 foot=[P('ペア保存後も、接続とアプリ権限を別に確認。','Pair saved? Check connectivity and app permission.'),P('不明な結果は元のセッションで状態確認。','Unknown result? Inspect the original session.')]
 if n:foot += [P('発見は120秒で停止。依頼の期限は別。','Discovery stops at 120s; request expiry differs.'),P('期限は両端末の早い方を採用。','The earlier peer deadline wins.')]
 else:foot += [P('発見は120秒で停止。承認期限は両端末の早い方。','Discovery stops at 120s. The earlier peer review deadline wins.')]
 c.foot(676,foot); return c.finish()


def port_window(lang,theme,n):
 c=Canvas(lang,theme,n, '空きポートは、予約ではない。' if lang=='ja' else 'A free port is not reserved.', '停止中の定義に対する一時bindの観測。全ソケットを閉じて候補を返し、別の保存や起動で再確認します。' if lang=='ja' else 'A transient bind observes a stopped definition. All probe sockets close before results return. Saving checks the source revision without binding. Actual start binds every port again.',850 if n else 760);P=c.pick
 c.lines(24,105,[P('停止中の接続 · 元のリビジョンを維持','Stopped connection · preserve source revision')],14)
 x=24; w=c.w-48
 c.rect(x,132,w,104,'panel',12,'border')
 c.text(40,160,P('1  一時的にポートを開いて確認','1  Open ports briefly to check'),18,weight=600)
 c.lines(40,187,[P('元のポート → 使用中の場合だけ候補を調べる','Original port → search only on address-in-use'),P('TCP/UDP・loopback・リモート範囲は固定','Keep protocol, loopback and remote scope')],13 if n else 15)
 # Finite observation interval with open/close endpoints.
 c.path(f'M48 278H{c.w-48}','line',2)
 c.circle(48,278,5);c.circle(c.w-48,278,5)
 c.text(48,264,P('開く','Open'),13,'accenttext');c.text(c.w-48,264,P('全て閉じる','Close all'),13,'accenttext',anchor='end')
 c.badge((c.w-200)/2,293,P('候補 = 今この瞬間の観測','Candidate = observed now'),200)
 c.arrow(40,333,40,366,'line',dash='4 4')
 c.text(c.w/2,354,P('閉じた後は、他のアプリが取得する場合も','After closing, another app may take it'),12,'muted',anchor='middle')
 # Mapping inset, not a generic process card.
 yy=388
 c.text(24,yy,P('2  対応を確認して、別途保存／起動','2  Review mapping; save / start separately'),16,weight=600)
 if n:
  a,b=90,305; y=yy+52
 else:a,b=210,680;y=yy+54
 c.text(a,y-14,P('ローカル','Local'),13,'muted',anchor='middle');c.text(b,y-14,P('リモート','Remote'),13,'muted',anchor='middle')
 for i,(lp,rp) in enumerate([('49152','8000'),('49153','8002')]):
  row=y+20+i*42;c.badge(a-46,row-22,lp,92);c.badge(b-46,row-22,rp,92);c.arrow(a+54,row-7,b-54,row-7)
 c.text(b,y+106,P('8001 は除外','8001 excluded'),13,'muted',anchor='middle')
 fy=772 if n else 700
 start_y=624 if n else 608
 c.text(24,577,P('元の設定が変わったら、候補を再確認。','Changed source revision? Review the candidate again.'),13,'muted')
 c.rect(24,start_y-24,c.w-48,72,'soft',10)
 c.text(40,start_y,P('3  実際の起動で、もう一度開く','3  Real start: open the ports again'),17,'accenttext',600)
 c.text(40,start_y+25,P('保存時はリビジョン確認。起動時に全ポートを再確認。','Save checks revision. Start rechecks every port.'),12 if n else 15,'accenttext')
 c.foot(fy,[P('候補選択だけでは保存・共有・起動しません。','Selecting a candidate does not save, share or start.'),P('起動時に競合したら停止。別ポートへ勝手に変更しません。','A start conflict fails; no silent alternate port.')])
 return c.finish()

def topology(lang,theme,n):
 c=Canvas(lang,theme,n, '3つの経路、異なる到達条件。' if lang=='ja' else 'Three routes to a peer.', 'Tailnetは外部調整と選択された経路、relay-based LANは共通の承認済みrelay、direct LANは正確なTCP/UDP端点の到達性が必要です。Mixedは承認済み経路を関連付け、無関係なネットワークのgatewayにはなりません。' if lang=='ja' else 'Tailnet uses external coordination and an available direct or relay route. Relay-based LAN needs a common approved reachable relay. Direct LAN needs exact TCP/UDP endpoint reachability. Mixed associates approved routes; it is not a gateway between unrelated networks.',920 if n else 700);P=c.pick
 c.lines(24,105,[P('線は必要な経路。接続成功の証明ではありません。','Paths show requirements, not proof of connectivity.')],13)
 rowh=224 if n else 166
 for i in range(3):
  y=137+i*rowh
  heads_ja=['外部サービスでつなぐ','中継点を使ってつなぐ','LAN内で直接つなぐ']
  c.text(24,y,heads_ja[i] if lang=='ja' else ['Tailnet','Relay-based LAN','Direct LAN'][i],19,'text',650)
  if lang=='ja':c.text(24,y+20,['Tailnet','Relay-based LAN','Direct LAN'][i],12,'muted')
  if n:
   ax,bx=30,286; ay=y+42; devw=84
  else:ax,bx=245,800;ay=y+12;devw=90
  c.device(ax,ay,'A',devw);c.device(bx,ay,'B',devw)
  x1=ax+devw+8;x2=bx-8;cy=ay+32;mid=(x1+x2)/2
  if i==0:
   c.arrow(x1,cy,x2,cy);c.badge(mid-67,cy-15,'Direct / Relay',134)
   if n:
    c.text(200,ay+107,P('外部の調整サービス','External coordination'),13,'muted',anchor='middle')
    c.path(f'M72 {ay+96}v12h58 M328 {ay+96}v12h-58','line',1.5,'3 4')
   else:
    c.text(mid,ay-5,P('外部の調整サービス','External coordination'),13,'muted',anchor='middle')
    c.path(f'M{x1} {ay+8}L{mid-90} {ay-8} M{mid+90} {ay-8}L{x2} {ay+8}','line',1.5,'3 4')
  elif i==1:
   c.arrow(x1,cy+12,mid-42,cy+12);c.arrow(mid+42,cy+12,x2,cy+12)
   c.path(f'M{x1} {cy-8}Q{mid} {cy-64} {x2} {cy-8}','line',1.5,'4 4')
   c.rect(mid-83,cy-48,166,21,'bg',4)
   c.text(mid,cy-33,P('直接経路も利用可能','Direct path possible'),11,'muted',anchor='middle',limit=x2-x1)
   c.rect(mid-40,cy-13,80,50,'soft',8,'border');c.text(mid,cy+17,'Relay',16,'accenttext',650,'middle',68)
   for j in range(3):c.circle(mid-15+j*15,cy+28,2)
  else:
   c.arrow(x1,cy-5,x2,cy-5);c.arrow(x2,cy+9,x1,cy+9)
   c.text(mid,cy-21,'TCP + UDP',13,'accenttext',anchor='middle')
  yy=y+170 if n else y+111
  notes=[P('参加認証と外部調整が必要','Enrollment + external coordination'),P('両端末から同じ relay に到達','Both peers reach the same relay'),P('正確な私設アドレス・端点へ到達','Reach the exact private endpoints')]
  c.text(24,yy,notes[i],14,'accenttext',600)
  sub=[P('選ばれる経路はネットワーク条件次第。','The usable path depends on network conditions.'),P('中継点は承認した基点。自動では移転しません。','Approved anchor; hosting never moves automatically.'),P('中継・外部探索なし。OSの経路は変えません。','No relay, STUN or OS route changes.')]
  c.text(24,yy+23,sub[i],12 if n else 14,'muted')
  if i<2:c.path(f'M24 {y+rowh-20}H{c.w-24}','border',1)
 c.foot(850 if n else 650,[P('Mixed は承認した経路を関連付けます。','Mixed associates explicitly approved routes.'),P('既存TCPの移行やネットワーク間の橋渡しはしません。','Existing TCP does not migrate. This is not a gateway.')])
 return c.finish()


def durable_compare(lang,theme,n):
 if n:return durable_narrow(lang,theme)
 c=Canvas(lang,theme,n,'文字とファイル、保存までの違い。' if lang=='ja' else 'Text and files: save gates.', '文字は信頼された相手から耐久保存、ファイルは受信承認または別途承認済みautosaveを経て保存します。双方ともローカル受付は配達完了ではなく、保存ACK喪失時は元のIDで状態確認します。' if lang=='ja' else 'Text is durably saved under peer trust. Files additionally require receiver acceptance or explicitly configured autosave. Local acceptance is not delivery; after a lost save receipt, inspect the original ID.',1020 if n else 690);P=c.pick
 c.lines(24,105,[P('永続キュー · 両端末の稼働・接続・有効な権限が必要','Durable queues · both devices must be reachable')],12 if n else 15)
 for file in (False,True):
  ox=24 if n else 24+int(file)*472;oy=138+int(file)*365 if n else 142;ww=352 if n else 440
  c.rect(ox,oy,ww,335,'panel',14,'border')
  # Text sheet / file-folder visual markers.
  c.rect(ox+18,oy+18,28,32,'soft',3,'border')
  if file:c.path(f'M{ox+23} {oy+27}h9l3 4h7v12h-19z','accent',1.5)
  else:
   for dy in (27,33,39):c.path(f'M{ox+24} {oy+dy}h16','accent',1.5)
  c.text(ox+58,oy+40,P('ファイル・フォルダー','Files / folders') if file else P('文字メッセージ','Text message'),19,weight=650)
  a,b=ox+45,ox+ww-45
  c.text(a,oy+77,P('送信','Send'),13,'muted',anchor='middle');c.text(b,oy+77,P('受信','Receive'),13,'muted',anchor='middle')
  c.path(f'M{a} {oy+91}V{oy+300} M{b} {oy+91}V{oy+300}','border',2,'4 5')
  c.circle(a,oy+97,5)
  c.text(a+12,oy+102,P('この端末で受付','Accepted locally'),12,'muted',limit=ww-80)
  c.text((a+b)/2,oy+123,P('申込み / 送信試行','Offer / send attempt'),12,'text',anchor='middle',limit=ww-45)
  c.arrow(a,oy+130,b,oy+130)
  c.text((a+b)/2,oy+158,P('受信承認／設定済み自動保存','Accept batch / configured autosave') if file else P('現在の信頼・送信権限を確認','Check current peer trust'),13,'accenttext',anchor='middle',limit=ww-32)
  if file:
   c.arrow(a,oy+182,b,oy+182)
   c.text((a+b)/2,oy+177,P('承認後にファイル本体','File payload after consent'),11,'muted',anchor='middle',limit=ww-50)
  c.rect(b-25,oy+195,50,35,'soft',4,'border');c.path(f'M{b-16} {oy+205}h32 M{b-16} {oy+212}h32 M{b-16} {oy+219}h22','accent',1.5)
  c.text((a+b)/2-18,oy+218,P('相手で耐久保存','Durably saved'),14,'text',600,'middle',ww-145)
  c.arrow(b,oy+240,a,oy+240,'line','4 5')
  mid=(a+b)/2;c.rect(mid-18,oy+225,36,30,'panel',0);c.path(f'M{mid-7} {oy+233}l14 14 M{mid+7} {oy+233}l-14 14','accent',2)
  c.text(mid,oy+276,P('ACK が届かないことも','Save receipt may be lost'),14,'muted',anchor='middle',limit=ww-45)
  c.badge(a-28,oy+294,P('元ID','ID'),56)
 y=901 if n else 526
 c.rect(24,y-25,c.w-48,74,'soft',12)
 c.lines(40,y,[P('不明でも、保存済みの場合があります。','Unknown can still mean saved.'),P('元のIDで確認。安易に新規送信を作り直さない。','Inspect the original ID; do not blindly create a new send.')],13 if n else 16,'accenttext')
 c.foot(y+85,[P('保存確認は既読や、今も残ることの保証ではありません。','Save confirmation is not reading or continued retention.'),P('取り消しは追加送信を止めます。保存済み内容は回収不可。','Cancel stops sending, not copies already saved.')])
 return c.finish()

def durable_narrow(lang,theme):
 c=Canvas(lang,theme,True,'文字とファイル、保存までの違い。' if lang=='ja' else 'Text and files: save gates.','文字とファイルは現在の相手の信頼が必要。ファイルは受信承認または別途設定済み自動保存が追加で必要。ローカル受付と相手保存と保存ACK確認を分けます。' if lang=='ja' else 'Both need current peer trust. Files also need batch acceptance or separately configured autosave. Local acceptance, receiver saving and save confirmation are separate.',800);P=c.pick
 c.lines(24,105,[P('両方とも、現在の相手の信頼が必要です。','Both need current peer trust.')],14)
 c.text(24,126,P('元の期限内・両端末の稼働と接続が必要','Original deadline; both devices running and reachable'),12,'muted')
 # Compact comparison of the only differing gate; evidence path is shared.
 c.text(24,149,P('文字','Text'),18,weight=650);c.text(125,149,P('文字を耐久保存','Save the text durably'),14,'accenttext')
 c.path('M24 167H376','border',1)
 c.text(24,202,P('ファイル','Files'),18,weight=650)
 c.lines(125,196,[P('受信承認／設定済み自動保存','Batch consent / autosave'),P('→ 本体を転送して保存','→ transfer and save')],13,'accenttext',limit=245)
 c.path('M24 238H376','border',1)
 c.text(24,272,P('ここからは共通: 保存の証拠を確認','Shared: confirm the saved evidence'),17,weight=650)
 a,b=60,340
 c.text(a,313,P('送信側','Sender'),14,'muted',anchor='middle');c.text(b,313,P('受信側','Receiver'),14,'muted',anchor='middle')
 c.path(f'M{a} 330V585 M{b} 330V585','border',2,'4 5')
 c.circle(a,345,5);c.text(a+14,350,P('この端末で受付','Accepted locally'),14)
 c.text(200,386,P('上の条件で送信・保存へ','Send and save under the gates above'),13,'accenttext',anchor='middle',limit=350)
 c.arrow(a,403,b,403)
 c.rect(b-24,425,48,40,'soft',5,'border')
 for y in (435,444,453):c.path(f'M{b-15} {y}h30','accent',1.5)
 c.text(200,450,P('相手で保存済み','Saved on receiver'),15,weight=600,anchor='middle',limit=220)
 c.arrow(b,496,a,496,'line','4 5');c.rect(183,481,34,30,'bg',0);c.path('M193 489l14 14 M207 489l-14 14','accent',2)
 c.text(200,535,P('保存ACKが届かない場合も','The save receipt may be lost'),14,'muted',anchor='middle')
 c.badge(a-35,563,P('元のID','Same ID'),70)
 c.rect(24,621,352,74,'soft',12)
 c.lines(40,647,[P('結果不明でも、保存済みの可能性があります。','Unknown can still mean saved.'),P('新規送信を作らず、元のIDで確認。','Inspect the original ID before a new send.')],13,'accenttext',limit=320)
 c.foot(733,[P('保存確認は既読や現在の存在の保証ではありません。','Save evidence is not reading or continued retention.'),P('取り消しても、保存済みの内容は回収できません。','Cancellation cannot recall saved content.')])
 return c.finish()


def restart_matrix(lang,theme,n):
 c=Canvas(lang,theme,n,'再起動で、何が戻る？' if lang=='ja' else 'What returns after restart?', '通常起動とoffline起動の差。通常の保存済みサービスは停止したまま。承認済み起動選択だけが別条件で試行し、永続キューは元のIDと期限を維持します。' if lang=='ja' else 'Saved-network and offline startup differ. Ordinary saved services remain stopped. Separately approved startup selections may attempt launch; durable queues retain original IDs and deadlines.',800 if n else 730);P=c.pick
 c.lines(24,105,[P('同じプロファイル · 停止してから、起動方法を選択','Same profile · stop first, then choose startup mode')],13)
 mid=c.w/2;c.badge(mid-92,130,P('停止 → プロセス再起動','Stop → new process'),184)
 col1,col2=(226,330) if n else (540,800)
 c.path(f'M{mid} 164v14H{col1}v17 M{mid} 178H{col2}v17','line',1.5)
 c.text(col1,211,P('通常起動','Saved network'),13,'accenttext',650,'middle',100);c.text(col2,211,P('管理のみ','Local only'),13,'accenttext',650,'middle',100)
 c.text(col1,232,'saved',12,'muted',anchor='middle',limit=100);c.text(col2,232,'offline',12,'muted',anchor='middle',limit=100)
 c.text(24,219,P('対象','State'),15,'muted',600)
 rows=[('管理UI / CLI','Local UI / CLI','起動','Starts','起動','Starts'),('保存したネットワーク','Saved network','起動試行','Attempts','起動せず','Off'),('受信・自動保存の許可','Receive / autosave','条件付き','Conditional','保存のみ','Stored'),('通常の保存サービス','Saved services','停止','Stopped','停止','Stopped'),('承認済み起動選択','Startup approvals','条件付き1回','Eligible once','抑止','Suppressed'),('永続文字・ファイル','Durable queues','条件付き','Conditional','配達せず','No delivery'),('即時転送の途中状態','Immediate transfer','再開せず','No resume','再開せず','No resume')]
 rh=56 if n else 49
 for i,r in enumerate(rows):
  y=240+i*rh
  if i%2==0:c.rect(16,y,c.w-32,rh,'panel',0)
  labels_ja=['管理UI / CLI','ネットワーク','受信 / 自動保存','通常の保存定義','別承認の自動起動','永続キュー','即時転送の途中']
  labels_en=['Local UI / CLI','Saved network','Receive / autosave','Saved services','Startup opt-in','Durable queues','Immediate progress']
  label=P(labels_ja[i],labels_en[i]) if n else P(r[0],r[1])
  c.text(24,y+33,label,14 if n else 16,limit=165 if n else 390)
  c.text(col1,y+33,P(r[2],r[3]),13 if n else 16,'accenttext' if i in (0,1,4) else 'muted',anchor='middle',limit=100 if n else 220)
  c.text(col2,y+33,P(r[4],r[5]),13 if n else 16,'muted',anchor='middle',limit=100 if n else 220)
 c.foot(672 if n else 624,[P('条件付き: 現在の接続・権限・停止設定を確認。','Conditional: current connection, permission and pause.'),P('永続キューは元のID・期限内で試行。','Durable attempts keep the original ID and deadline.'),P('offline は保存済みの許可を消しません。','Offline does not erase saved approvals.'),P('soba status で接続状態を別に確認。','Check connectivity separately with soba status.')])
 return c.finish()


def api_boundary(lang,theme,n):
 c=Canvas(lang,theme,n,'管理APIは、この端末の内側。' if lang=='ja' else 'Management stays local.', 'ローカルbrowserはsessionとsame-origin/CSRF検査を経て管理APIへ。peerは認証済みpeer protocolと個別のscope確認を経る。peer protocolは/api/commandを転送しない。' if lang=='ja' else 'The local browser uses session and same-origin/CSRF checks for management. A peer uses the authenticated peer protocol and explicit scoped permission. Peer protocols never forward /api/command.',820 if n else 680);P=c.pick
 c.lines(24,105,[P('同じGo実装でも、入口と権限は別です。','Shared Go core; separate entrances and authority.')],14)
 bx,by,bw,bh=24,139,(352 if n else 650),(467 if n else 400)
 c.rect(bx,by,bw,bh,'panel',16,'border');c.text(40,167,P('この端末','THIS DEVICE'),13,'muted',650)
 cx=200 if n else 350
 c.badge(cx-106,188,P('ローカルのブラウザー','Browser on this device'),212)
 c.path(f'M{cx} 225V232','accent',2)
 c.arrow(cx,254,cx,262)
 c.text(cx,247,P('セッション + Host / Origin + CSRF','Session + Host / Origin + CSRF'),12,'muted',anchor='middle',limit=320)
 c.rect(cx-146,268,292,66,'soft',10);c.text(cx,295,'/api/command',19,'accenttext',650,'middle',270);c.text(cx,319,P('数値loopbackのみ','Numeric loopback only'),13,'accenttext',anchor='middle',limit=270)
 c.arrow(cx,340,cx,371)
 c.rect(cx-146,380,292,66,'bg',10,'border');c.text(cx,406,P('Go: 現在の状態・権限を確認','Go: current state + authority'),16,weight=600,anchor='middle',limit=270)
 c.text(cx,429,P('操作ごとの許可された範囲へ','Operation-specific permitted scope'),13,'muted',anchor='middle',limit=270)
 if n:
  c.path('M321 543H340V514','accent',2);c.arrow(340,514,340,451);c.badge(cx-121,528,P('peer認証 + 明示した範囲','Peer identity + explicit scope'),242)
  c.path('M40 489H360','border',1,'4 5')
  c.text(40,511,P('別の入口: peer protocol','Separate entrance: peer protocol'),13,'accenttext')
  c.device(158,635,P('相手の端末','Peer device'),84);c.arrow(200,629,200,564)
 else:
  c.device(772,366,P('相手の端末','Peer device'),110)
  c.arrow(764,404,cx+152,404)
  c.text(738,343,'Peer protocol',15,'accenttext',600,'middle',175)
  c.badge(579,387,P('鍵と範囲を確認','Identity + scope check'),179)
  c.path('M820 355V301H510','line',1.5,'4 5')
  c.path('M694 294l14 14 M708 294l-14 14','line',2)
  c.text(738,271,P('管理APIへの転送なし','No management forwarding'),12,'muted',anchor='middle',limit=205)
  c.lines(40,493,[P('リソースの検査・管理も別途、限定した許可が必要です。','Resource inspection / management needs separate limited grants.')],14)
 c.foot(770 if n else 594,[P('受信口があっても、権限の確認は省略しません。','An open endpoint never bypasses authorization.'),P('ローカルAPIを共有・転送しないでください。','Do not expose or forward the local management API.'),P('peer protocol から /api への転送はありません。','Peer protocols never forward requests into /api.')])
 return c.finish()


def resource_gates(lang,theme,n):
 c=Canvas(lang,theme,n,'「無制限」でも、資源は有限。' if lang=='ja' else 'Policy ≠ available resources.', '論理上の許可、有限の資源予算、実際のOS資源は別です。容量を上げても権限や期限は変わりません。' if lang=='ja' else 'Logical allowance, finite resource budgets and actual OS capacity are independent constraints. Raising capacity does not change authority or lifetime.',740 if n else 740);P=c.pick
 c.lines(24,105,[P('受付には、すべての条件が必要です。','Admission needs every applicable condition.')],14)
 # Three nested gates communicate intersection, not a permission ladder.
 cx=c.w/2
 layers=[(24,139,c.w-48,414,'panel'),(44,237,c.w-88,316,'soft'),(64,335,c.w-128,218,'bg')]
 for x,y,w,h,fill in layers:c.rect(x,y,w,h,fill,16,'border')
 c.text(40,168,P('1  論理上の選択','1  Logical choice'),18,weight=650)
 c.lines(40,196,[P('例: ファイルサイズ・件数','Example: file size / entry count'),P('default / limited / unlimited','default / limited / unlimited')],14)
 c.text(60,267,P('2  有限の資源予算','2  Finite resource budgets'),18,weight=650)
 c.lines(60,294,[P('送信側の一時保存、受信側の予約領域','Sender spool, receiver reservations'),P('無制限は選べません。待受・同時転送も有限。','No unlimited option; listeners / flows stay finite.')],13 if n else 15)
 c.text(80,365,P('3  実際に使える資源','3  Actual available resources'),16 if n else 18,weight=650,limit=c.w-160)
 c.lines(80,395,[P('ディスク・メモリ・OSの上限','Disk, memory and OS limits'),P('予算以下でも失敗する場合があります。','Work may fail below the budget.')],12 if n else 15,limit=c.w-160)
 # A finite disk ledger, explicitly schematic.
 c.text(80,449,P('ディスク余裕の考え方（模式図）','Disk headroom (schematic)'),12,'muted')
 barw=c.w-160
 c.rect(80,464,barw,22,'panel',4,'border');c.rect(80,464,barw*.55,22,'soft',4);c.rect(80+barw*.55,464,barw*.45,22,'border',0)
 c.text(80,510,P('転送と予約','Data + reservations'),12,'accenttext');c.text(c.w-80,510,P('安全余白','Free-space margin'),12,'muted',anchor='end')
 c.arrow(cx,562,cx,595)
 c.badge(cx-120,605,P('現在の使用量と実効値を比較','Compare usage / effective limits'),240)
 c.foot(681 if n else 669,[P('容量の変更は、権限や有効期限を広げません。','Capacity changes do not expand permission or lifetime.'),P('論理上の無制限も、有限の資源予算に従います。','Logical unlimited still obeys finite resource budgets.'),P('default は既定値。0や無制限ではありません。','Default resolves to a value, not zero or unlimited.')])
 return c.finish()

def permissions_map(lang,theme,n):
 c=Canvas(lang,theme,n,'つながることと、許可すること。' if lang=='ja' else 'Separate gates, separate choices.','接続、文字とファイル申込みへの信頼、ファイル保存同意、自動保存、サービスの相手・port・期限は独立した許可です。' if lang=='ja' else 'Connectivity, sender trust, file-save consent, autosave and scoped service sharing are independent permissions.',850 if n else 690);P=c.pick
 c.lines(24,105,[P('接続できても、受信や共有は別に選びます。','A connection does not approve receiving or sharing.')],13 if n else 15)
 c.badge(24,136,P('接続済みの経路','Connected transport'),205)
 c.path('M40 171V551' if n else 'M40 171V197H710','border',2)
 if not n:c.path('M240 197V232 M710 197V232','border',2)
 for service in (False,True):
  x=56 if n else (24 if not service else 500);y=(207 if not service else 526) if n else 232;w=320 if n else 436
  if n:c.path(f'M40 {y+25}H56','border',2)
  c.rect(x,y,w,278 if not service else 194,'panel',12,'border')
  c.text(x+16,y+30,P('B  サービス共有','B  Share a service') if service else P('A  文字・ファイルを受信','A  Receive messages / files'),17,weight=650,limit=w-32)
  c.badge(x+16,y+50,P('相手・ポート・期限を許可','Allow peers, ports and lifetime') if service else P('この送信元を信頼','Trust this sender'),w-32)
  if service:
   c.arrow(x+w/2,y+89,x+w/2,y+115)
   c.text(x+w/2,y+143,P('指定したサービスだけを公開','Expose only the scoped service'),14,'accenttext',600,'middle',w-30)
   c.text(x+w/2,y+171,P('アプリの起動・認証も別途必要','App setup / login remains separate'),12,'muted',anchor='middle',limit=w-30)
  else:
   mid=x+w/2;c.path(f'M{mid} {y+87}v15H{x+77}v17 M{mid} {y+102}H{x+w-82}v17','accent',1.5)
   c.text(x+77,y+140,P('文字を受信','Receive text'),14,'accenttext',anchor='middle',limit=140)
   c.text(x+w-82,y+140,P('ファイル申込み','File offer'),14,'accenttext',anchor='middle',limit=140)
   c.arrow(x+w-82,y+152,x+w-82,y+175)
   c.text(x+w-82,y+200,P('保存の同意','Consent to save'),14,'text',600,'middle',160)
   c.lines(x+16,y+233,[P('通常: バッチごとに受信承認','Default: accept each batch'),P('自動保存: 相手と保存先を別途承認','Autosave: separately approve peer + folder')],12 if n else 14,limit=w-32)
 c.foot(778 if n else 610,[P('A と B は独立。片方の承認では他方は有効になりません。','A and B require independent approvals.'),P('受信側が、その端末への許可を決めます。','The receiving device controls its own permissions.'),P('逆方向は、もう一方の端末で別に許可します。','Reverse direction needs the other receiver’s approval.')])
 return c.finish()


def saved_lifecycle(lang,theme,n):
 c=Canvas(lang,theme,n,'設定を保存。開始は、別の操作。' if lang=='ja' else 'Save settings. Start separately.','保存は停止した定義を作ります。別途内容を確認して開始すると実行状態になり、停止は設定を保って稼働だけを止めます。通常の保存定義はプロセス再起動しても停止したままです。' if lang=='ja' else 'Saving creates a stopped definition. A separately reviewed start creates live runtime. Stopping preserves the settings. Ordinary saved services remain stopped after process restart.',800 if n else 690);P=c.pick
 c.lines(24,105,[P('通常の保存サービスを手動で開始する場合','Ordinary saved services · manual start')],14)
 x=c.w/2
 c.rect(24,138,c.w-48,102,'panel',12,'border')
 c.rect(42,158,36,50,'soft',4,'border');c.path('M50 172h20 M50 181h20 M50 190h14','accent',1.5)
 c.text(94,170,P('保存済みの定義','Saved definition'),19,weight=650)
 c.lines(94,196,[P('相手・ポート・期限などの設定','Peer, ports and lifetime'),P('保存だけでは待受や許可を開始しない','Saving opens no listeners or grants')],12 if n else 14,limit=c.w-120)
 c.path(f'M{x} 247V278','line',1.5,'4 5')
 if n:
  sx,sy,rx,ry=200,314,200,508
 else:sx,sy,rx,ry=230,332,730,332
 for xx,yy,ja,en in [(sx,sy,'停止中','Stopped'),(rx,ry,'実行中','Started')]:
  c.rect(xx-90,yy-24,180,60,'soft' if yy==ry and xx==rx else 'panel',30,'border');c.text(xx,yy+13,P(ja,en),20,'accenttext',650,'middle',160)
 if n:
  c.arrow(sx,sy+44,rx,ry-32)
  c.rect(40,398,320,43,'bg',0);c.text(200,415,P('内容を確認して、明示的に開始','Review the scope, then explicitly start'),13,'accenttext',600,'middle',320)
  c.text(200,436,P('ネットワーク・相手の識別情報を再確認','Recheck network and peer identity'),12,'muted',anchor='middle',limit=320)
  c.path('M295 525H368V314H297','line',1.5);c.arrow(313,314,297,314,'line');c.text(352,474,P('停止','Stop'),13,'muted',anchor='middle',limit=50)
 else:
  c.arrow(sx+100,sy+6,rx-100,ry+6)
  c.text(480,304,P('内容を確認して開始','Review scope, then start'),16,'accenttext',600,'middle',300)
  c.text(480,376,P('ネットワークと相手を再確認','Recheck network + peer identity'),13,'muted',anchor='middle',limit=330)
  c.path('M730 377V428H230V377','line',1.5);c.arrow(230,392,230,375,'line')
  c.text(480,453,P('停止: 稼働を終了。定義は残る。','Stop: runtime ends, definition stays.'),15,'muted',anchor='middle',limit=430)
 y=607 if n else 518
 c.badge((c.w-292)/2,y,P('実アプリの接続・認証は別に確認','Check the actual app and its login'),292)
 c.foot(y+81,[P('通常の定義は、プロセス再起動後も停止したまま。','Ordinary definitions stay stopped after process restart.'),P('自動の起動選択は、別途確認・承認する設定です。','Automatic startup uses separately reviewed approvals.'),P('停止で終了するのは稼働と許可。定義は残ります。','Stop ends live authority; the saved definition remains.')])
 return c.finish()


def file_evidence(lang,theme,n):
 c=Canvas(lang,theme,n,'送信の停止と、保存の証拠は別。' if lang=='ja' else 'Stopped ≠ undelivered.','永続ファイル送信の作業状態と配達証拠は別です。取消や期限切れは追加送信を止めますが、相手の保存を取り消しません。不明な結果は元のIDで保存証拠を確認します。' if lang=='ja' else 'Durable-file work status and delivery evidence are independent. Cancellation or expiry stops further sending but cannot recall saved files. Inspect the original ID and exact saved evidence after an unknown result.',800 if n else 690);P=c.pick
 c.lines(24,105,[P('永続ファイル送信 · 失われた保存ACKの例','Durable files · example of a lost save receipt')],14)
 # Two axes, with different terminal meanings.
 ys=[185,340] if not n else [185,382]
 for i,y in enumerate(ys):
  c.text(24,y-35,P('作業の状態','Work status') if i==0 else P('配達の証拠','Delivery evidence'),18,weight=650)
  c.path(f'M40 {y+30}H{c.w-40}','border',2)
  positions=[64,c.w/2,c.w-64]
  labels=([P('受付','Queued'),P('送信試行','Attempt'),P('取消','Cancel')] if i==0 else [P('未試行','Not tried'),P('不明','Unknown'),P('保存確認','Confirmed')])
  for j,(xx,label) in enumerate(zip(positions,labels)):
   c.circle(xx,y+30,6,'accent' if j<2 else 'line');c.text(xx,y+61,label,13 if n else 17,'accenttext' if j<2 else 'muted',600,'middle',110 if n else 240)
  if i==0:
   c.text(c.w/2,y+98,P('取消後は追加送信を止める','Cancellation stops further sends'),13,'muted',anchor='middle',limit=c.w-48)
  else:
   c.path(f'M{positions[1]} {y+12}Q{(positions[1]+positions[2])/2} {y-38} {positions[2]} {y+12}','line',1.5,'4 4')
   c.text(c.w/2,y+101,P('元IDで、取得できた正確な保存証拠を照合','Original ID + exact receiver-save evidence'),12 if n else 15,'muted',anchor='middle',limit=c.w-48)
 c.text(c.w/2,ys[1]+128,P('証拠が得られなければ、不明のままです。','Without exact evidence, the result stays unknown.'),12,'muted',anchor='middle',limit=c.w-48)
 # Link the ambiguous attempt to evidence, deliberately not cancellation to failure.
 c.path(f'M{c.w/2} {ys[0]+113}V{ys[1]-57}','accent',1.5,'4 4')
 y=590 if n else 512
 c.rect(24,y-22,c.w-48,75,'soft',12)
 c.lines(40,y,[P('「取消済み」「期限切れ」でも保存済みの場合があります。','Canceled or expired can still mean saved.'),P('結果不明を「届いていない」と扱わない。','Unknown does not mean undelivered.')],12 if n else 16,'accenttext',limit=c.w-80)
 c.foot(y+103,[P('保存確認は既読でも、現在の存在の保証でもありません。','Saved evidence is not reading or continued retention.'),P('元のIDを保持。安易に新規送信を作り直さない。','Keep the original ID; do not blindly create another send.'),P('取り消しで、既知の保存証拠は消えません。','Cancellation does not erase known save evidence.')])
 return c.finish()

def connection_narrow(lang,theme):
 c=Canvas(lang,theme,True,'使いたいサービスだけをつなぐ。' if lang=='ja' else 'Connect the service you choose.','2台のsobalink間のサービス共有例。アプリはローカル入口を使い、相手の現在の共有許可を経て共有サービスへ届きます。線は構造であり接続確認ではありません。' if lang=='ja' else 'Service sharing between two sobalink devices. The app uses a local entrance; the peer checks its current sharing grant before forwarding to the service. Lines show structure, not confirmed connectivity.',800);P=c.pick
 c.lines(24,105,[P('sobalink 同士で共有する例','A sharing example between sobalink devices')],13)
 # A laptop, with the application and userspace agent physically grouped.
 c.text(24,144,P('この端末: 使う側','THIS DEVICE · client'),16,weight=650)
 c.rect(36,160,328,162,'panel',14,'border');c.rect(48,172,304,138,'bg',7,'border')
 c.path('M24 322H376L361 336H39Z','line',1.5)
 c.rect(66,198,90,62,'soft',8);c.path('M66 211h90 M95 225l-7 8 7 8 M126 225l7 8-7 8','accent',1.6)
 c.text(111,286,P('アプリ','Your app'),15,weight=600,anchor='middle',limit=100)
 c.circle(282,230,34,'soft');c.text(282,235,'sobalink',14,'accenttext',650,'middle',80)
 c.arrow(163,230,241,230);c.text(200,211,P('入口','Entry'),12,'muted',anchor='middle',limit=70)
 c.text(24,364,P('1  実際のローカル接続先をアプリに設定','1  Set the app’s actual local endpoint'),13,'muted')
 c.path('M282 337V400H16V545','accent',2)
 c.badge(158,386,P('2  選んだネットワーク','2  Selected network'),218)
 c.text(24,458,P('相手の端末: 共有する側','PEER DEVICE · shared service'),16,weight=650)
 # Desktop monitor, with an explicit permission gate before the service.
 c.rect(36,477,328,162,'panel',14,'border');c.rect(48,489,304,138,'bg',7,'border')
 c.path('M200 640v18 M165 660h70','line',2)
 c.circle(112,545,34,'soft');c.text(112,550,'sobalink',14,'accenttext',650,'middle',80)
 c.arrow(16,545,72,545)
 c.arrow(153,545,247,545)
 c.rect(179,527,34,36,'soft',8);c.path('M196 532l10 4v9c0 6-10 12-10 12s-10-6-10-12v-9Z','accent',1.5)
 c.rect(257,522,76,17,'soft',4,'border');c.rect(257,546,76,17,'soft',4,'border')
 c.text(280,598,P('サービス','Service'),15,weight=600,anchor='middle',limit=120)
 c.text(24,689,P('3  相手・ポート・期限に合う共有だけ許可','3  Share only approved peers, ports and lifetime'),12,'muted')
 c.foot(745,[P('アプリの起動・認証と、経路の到達性は別に確認。','Check app setup/login and network reachability.'),P('線は実際の接続状態を表示していません。','Lines do not display live connection status.'),P('ローカル入口と相手の共有先は別の接続先です。','Local entry and peer service are different endpoints.')])
 return c.finish()

GENERATORS={'pairing-exchange':pairing,'port-observation':port_window,'network-topologies':topology,'durable-save-gates':durable_compare,'restart-state':restart_matrix,'api-boundary':api_boundary,'resource-gates':resource_gates}
def main():
 for kind,fn in GENERATORS.items():
  for lang in ('ja','en'):
   for theme in PALETTES:
    for n in (False,True):
     (D/f'{kind}-{lang}-{theme}-{"narrow" if n else "wide"}.svg').write_text(fn(lang,theme,n))
if __name__=='__main__':main()
