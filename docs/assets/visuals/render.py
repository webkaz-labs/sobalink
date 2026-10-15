from pathlib import Path
import ctypes as C
import xml.etree.ElementTree as ET
D=Path(__file__).parent
r=C.CDLL('librsvg-2.so.2'); c=C.CDLL('libcairo.so.2'); g=C.CDLL('libgobject-2.0.so.0')
r.rsvg_handle_new_from_file.argtypes=[C.c_char_p,C.c_void_p];r.rsvg_handle_new_from_file.restype=C.c_void_p
r.rsvg_handle_render_cairo.argtypes=[C.c_void_p,C.c_void_p];r.rsvg_handle_render_cairo.restype=C.c_int
c.cairo_image_surface_create.argtypes=[C.c_int,C.c_int,C.c_int];c.cairo_image_surface_create.restype=C.c_void_p
c.cairo_create.argtypes=[C.c_void_p];c.cairo_create.restype=C.c_void_p
c.cairo_surface_write_to_png.argtypes=[C.c_void_p,C.c_char_p];c.cairo_surface_write_to_png.restype=C.c_int
c.cairo_destroy.argtypes=[C.c_void_p];c.cairo_surface_destroy.argtypes=[C.c_void_p];g.g_object_unref.argtypes=[C.c_void_p]
for f in D.glob('*.svg'):
 root=ET.parse(f).getroot();w,h=int(root.attrib['width']),int(root.attrib['height'])
 handle=r.rsvg_handle_new_from_file(str(f).encode(),None);assert handle
 surface=c.cairo_image_surface_create(0,w,h);cr=c.cairo_create(surface)
 assert r.rsvg_handle_render_cairo(handle,cr)
 assert c.cairo_surface_write_to_png(surface,str(f.with_suffix('.png')).encode())==0
 c.cairo_destroy(cr);c.cairo_surface_destroy(surface);g.g_object_unref(handle)
print('Rendered all SVG variants with librsvg and Cairo.')
from PIL import Image,ImageDraw
for kind in ['permissions','saved-and-running','delivery-evidence']:
 if not (D/f'{kind}-ja-light-wide.png').exists(): continue
 out=Image.new('RGB',(1440,1456),'#ddd');draw=ImageDraw.Draw(out)
 for lang in ['ja','en']:
  for theme in ['light','dark']:
   wide=Image.open(D/f'{kind}-{lang}-{theme}-wide.png');wide.thumbnail((696,362))
   small=Image.open(D/f'{kind}-{lang}-{theme}-narrow.png');small.thumbnail((144,349))
   x=0 if theme=='light' else 720;y=0 if lang=='ja' else 728
   out.paste(wide,(x+12,y+24));out.paste(small,(x+288,y+374));draw.text((x+12,y+7),lang+' '+theme,fill='black')
 out.save(D/f'{kind}-review-sheet.png')
