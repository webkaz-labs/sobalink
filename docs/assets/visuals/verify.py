from pathlib import Path
import ctypes as C
import xml.etree.ElementTree as ET
D=Path(__file__).parent
r=C.CDLL('librsvg-2.so.2');g=C.CDLL('libgobject-2.0.so.0')
class Rect(C.Structure):_fields_=[('x',C.c_double),('y',C.c_double),('width',C.c_double),('height',C.c_double)]
r.rsvg_handle_new_from_data.argtypes=[C.c_char_p,C.c_size_t,C.c_void_p];r.rsvg_handle_new_from_data.restype=C.c_void_p
r.rsvg_handle_get_geometry_for_layer.argtypes=[C.c_void_p,C.c_char_p,C.POINTER(Rect),C.POINTER(Rect),C.POINTER(Rect),C.c_void_p];r.rsvg_handle_get_geometry_for_layer.restype=C.c_int
g.g_object_unref.argtypes=[C.c_void_p]
failures=[];count=0
for f in D.glob('*.svg'):
 root=ET.parse(f).getroot();w=float(root.attrib['width']);h=float(root.attrib['height']);texts=root.findall('{http://www.w3.org/2000/svg}text')
 for i,t in enumerate(texts):t.set('id',f'text-{i}')
 data=ET.tostring(root);handle=r.rsvg_handle_new_from_data(data,len(data),None);assert handle
 for i,t in enumerate(texts):
  ink=Rect();logical=Rect();viewport=Rect(0,0,w,h)
  assert r.rsvg_handle_get_geometry_for_layer(handle,f'#text-{i}'.encode(),C.byref(viewport),C.byref(ink),C.byref(logical),None)
  if ink.x<0 or ink.x+ink.width>w or ink.y<0 or ink.y+ink.height>h:failures.append((f.name,t.text,'canvas overflow'))
  if not f.name.startswith('connection-'):
   tx=float(t.attrib['x']);ty=float(t.attrib['y']);right=w
   if w==1040 and 188<ty<426:right=32+int((tx-32)//336)*336+304-16
   if w==360 and 192<ty<724:right=328
   if ink.x+ink.width>right:failures.append((f.name,t.text,'card overflow',round(ink.x+ink.width-right,2)))
  count+=1
 g.g_object_unref(handle)
print(f'Checked {count} text bounds across {len(list(D.glob("*.svg")))} SVGs.')
print('Failures:',failures)
assert not failures
