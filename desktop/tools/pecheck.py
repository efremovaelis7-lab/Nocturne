#!/usr/bin/env python3
"""Проверка exe: машина, подсистема и дерево ресурсов (иконки, манифест)."""
import struct, sys
for f in sys.argv[1:]:
    b = open(f, 'rb').read()
    pe = struct.unpack_from('<I', b, 0x3c)[0]
    assert b[pe:pe + 4] == b'PE\0\0'
    machine, nsec, _, _, _, optsz, _ = struct.unpack_from('<HHIIIHH', b, pe + 4)
    opt = pe + 24
    magic = struct.unpack_from('<H', b, opt)[0]
    subsystem = struct.unpack_from('<H', b, opt + 68)[0]
    dd = opt + (96 if magic == 0x10b else 112)
    rva, size = struct.unpack_from('<II', b, dd + 8 * 2)
    secs = []
    so = opt + optsz
    for i in range(nsec):
        name, vsize, va, rsize, rptr = struct.unpack_from('<8sIIII', b, so + 40 * i)
        secs.append((name.rstrip(b'\0'), va, max(vsize, rsize), rptr))
    def off(r):
        for n, va, sz, rp in secs:
            if va <= r < va + sz: return rp + (r - va)
        raise ValueError(hex(r))
    base = off(rva)
    def walk(o, depth, path):
        _, _, _, _, nn, ni = struct.unpack_from('<IIHHHH', b, base + o)
        for i in range(nn + ni):
            nid, od = struct.unpack_from('<II', b, base + o + 16 + 8 * i)
            if od & 0x80000000: walk(od & 0x7fffffff, depth + 1, path + [nid])
            else:
                drva, dsz, _, _ = struct.unpack_from('<IIII', b, base + od)
                data = b[off(drva):off(drva) + dsz]
                yield_.append((path + [nid], dsz, data[:12]))
    yield_ = []
    walk(0, 0, [])
    print(f, 'machine', hex(machine), 'subsystem', subsystem, '(2=GUI)', 'sections', [s[0].decode() for s in secs])
    for p, sz, head in yield_:
        print('   type %-3d id %-3d lang %-5d size %-6d %r' % (p[0], p[1], p[2], sz, head))
