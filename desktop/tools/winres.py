#!/usr/bin/env python3
"""Иконка и манифест для NOCTURNE.exe без инструментов Windows.

winres.py Nocturne.icns OUT_DIR
  OUT_DIR/icon.png                    — 256×256 для favicon
  OUT_DIR/nocturne.ico                — значок 16…256
  OUT_DIR/rsrc_windows_amd64.syso     — COFF-объект с ресурсами (RT_ICON, RT_GROUP_ICON, RT_MANIFEST);
                                        go build сам подхватывает его для GOOS=windows GOARCH=amd64.
"""
import io, struct, sys, os
from PIL import Image

src, out = sys.argv[1], sys.argv[2]
os.makedirs(out, exist_ok=True)

# ---- самая крупная PNG-картинка из .icns
data = open(src, 'rb').read()
assert data[:4] == b'icns'
pos, best = 8, None
while pos < len(data):
    typ, ln = data[pos:pos + 4], struct.unpack('>I', data[pos + 4:pos + 8])[0]
    body = data[pos + 8:pos + ln]
    if body[:8] == b'\x89PNG\r\n\x1a\n':
        im = Image.open(io.BytesIO(body))
        if not best or im.size[0] > best.size[0]:
            best = im.convert('RGBA')
    pos += ln
assert best, 'в .icns нет PNG'
best.resize((256, 256), Image.LANCZOS).save(os.path.join(out, 'icon.png'))

# ---- .ico: каждая картинка — PNG (Windows Vista+ понимает PNG внутри ICO)
sizes = [16, 20, 24, 32, 40, 48, 64, 128, 256]
images = []
for s in sizes:
    b = io.BytesIO(); best.resize((s, s), Image.LANCZOS).save(b, 'PNG'); images.append((s, b.getvalue()))
ico = io.BytesIO()
ico.write(struct.pack('<HHH', 0, 1, len(images)))
off = 6 + 16 * len(images)
for s, b in images:
    ico.write(struct.pack('<BBBBHHII', s % 256, s % 256, 0, 0, 1, 32, len(b), off)); off += len(b)
for _, b in images:
    ico.write(b)
open(os.path.join(out, 'nocturne.ico'), 'wb').write(ico.getvalue())

manifest = b'''<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <assemblyIdentity type="win32" name="NOCTURNE" version="1.0.0.0"/>
  <description>NOCTURNE</description>
  <dependency><dependentAssembly><assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0" processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/></dependentAssembly></dependency>
  <application xmlns="urn:schemas-microsoft-com:asm.v3"><windowsSettings>
    <dpiAware xmlns="http://schemas.microsoft.com/SMI/2005/WindowsSettings">true/pm</dpiAware>
    <dpiAwareness xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">PerMonitorV2</dpiAwareness>
    <activeCodePage xmlns="http://schemas.microsoft.com/SMI/2019/WindowsSettings">UTF-8</activeCodePage>
  </windowsSettings></application>
  <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3"><security><requestedPrivileges><requestedExecutionLevel level="asInvoker" uiAccess="false"/></requestedPrivileges></security></trustInfo>
</assembly>
'''

# ---- ресурсы: тип -> [(id, bytes)]
RT_ICON, RT_GROUP_ICON, RT_MANIFEST = 3, 14, 24
group = io.BytesIO(); group.write(struct.pack('<HHH', 0, 1, len(images)))
for i, (s, b) in enumerate(images, 1):
    group.write(struct.pack('<BBBBHHIH', s % 256, s % 256, 0, 0, 1, 32, len(b), i))
res = {RT_ICON: [(i, b) for i, (_, b) in enumerate(images, 1)], RT_GROUP_ICON: [(1, group.getvalue())], RT_MANIFEST: [(1, manifest)]}
LANG = 0x0409


def build_rsrc():
    """Дерево каталогов ресурсов: тип → id → язык → запись данных; затем сами данные."""
    types = sorted(res)
    # размеры: каталог 16 + 8 на запись
    root_size = 16 + 8 * len(types)
    off = root_size
    type_dirs = {}
    for t in types:
        type_dirs[t] = off; off += 16 + 8 * len(res[t])
    lang_dirs = {}
    for t in types:
        for rid, _ in res[t]:
            lang_dirs[(t, rid)] = off; off += 16 + 8
    entries = {}
    for t in types:
        for rid, _ in res[t]:
            entries[(t, rid)] = off; off += 16
    datas = {}
    for t in types:
        for rid, b in res[t]:
            off = (off + 7) & ~7
            datas[(t, rid)] = off; off += len(b)
    buf = bytearray(off)
    relocs = []

    def dir_at(o, n):
        struct.pack_into('<IIHHHH', buf, o, 0, 0, 0, 0, 0, n)

    dir_at(0, len(types))
    for i, t in enumerate(types):
        struct.pack_into('<II', buf, 16 + 8 * i, t, 0x80000000 | type_dirs[t])
        dir_at(type_dirs[t], len(res[t]))
        for j, (rid, b) in enumerate(res[t]):
            struct.pack_into('<II', buf, type_dirs[t] + 16 + 8 * j, rid, 0x80000000 | lang_dirs[(t, rid)])
            dir_at(lang_dirs[(t, rid)], 1)
            struct.pack_into('<II', buf, lang_dirs[(t, rid)] + 16, LANG, entries[(t, rid)])
            e = entries[(t, rid)]
            struct.pack_into('<IIII', buf, e, datas[(t, rid)], len(b), 0, 0)
            relocs.append(e)  # OffsetToData — адрес относительно начала образа
            buf[datas[(t, rid)]:datas[(t, rid)] + len(b)] = b
    return bytes(buf), relocs


def coff(machine, reloc_type):
    raw, relocs = build_rsrc()
    hdr, sect = 20, 40
    p_raw = hdr + sect
    p_rel = p_raw + len(raw)
    p_sym = p_rel + 10 * len(relocs)
    o = bytearray()
    o += struct.pack('<HHIIIHH', machine, 1, 0, p_sym, 1, 0, 0)
    o += struct.pack('<8sIIIIIIHHI', b'.rsrc', 0, 0, len(raw), p_raw, p_rel, 0, len(relocs), 0, 0x40000040)
    o += raw
    for r in relocs:
        o += struct.pack('<IIH', r, 0, reloc_type)
    o += struct.pack('<8sIhHBB', b'.rsrc', 0, 1, 0, 3, 0)  # символ секции, класс STATIC
    o += struct.pack('<I', 4)  # пустая таблица строк
    return bytes(o)


open(os.path.join(out, 'rsrc_windows_amd64.syso'), 'wb').write(coff(0x8664, 3))   # IMAGE_REL_AMD64_ADDR32NB
open(os.path.join(out, 'rsrc_windows_arm64.syso'), 'wb').write(coff(0xAA64, 2))   # IMAGE_REL_ARM64_ADDR32NB
print('ok', best.size, len(images), 'icons')
