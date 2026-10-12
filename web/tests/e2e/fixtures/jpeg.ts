import { readFileSync } from 'node:fs';
import path from 'node:path';

// gps.jpg is written by `go run ./tools/e2e-fixtures web/tests/e2e/fixtures`;
// it carries EXIF GPS coordinates, XMP and IPTC segments.
export function buildJpegWithGps(): Buffer {
  return readFileSync(path.join(import.meta.dirname, 'gps.jpg'));
}

// hasGpsMetadata reports whether a JPEG still holds an EXIF GPS IFD (tag
// 0x8825 in IFD0) or any XMP/IPTC segment.
export function hasGpsMetadata(jpeg: Buffer): boolean {
  if (jpeg.length < 4 || jpeg[0] !== 0xff || jpeg[1] !== 0xd8) throw new Error('not a JPEG');
  let pos = 2;
  while (pos + 4 <= jpeg.length) {
    if (jpeg[pos] !== 0xff) break;
    const marker = jpeg[pos + 1] ?? 0;
    if (marker === 0xda || marker === 0xd9) break;
    const len = jpeg.readUInt16BE(pos + 2);
    const seg = jpeg.subarray(pos + 4, pos + 2 + len);
    if (marker === 0xe1) {
      if (seg.subarray(0, 6).toString('latin1') === 'Exif\0\0' && exifHasGps(seg.subarray(6))) {
        return true;
      }
      if (seg.subarray(0, 28).toString('latin1').startsWith('http://ns.adobe.com/xap')) {
        return true;
      }
    }
    if (marker === 0xed) return true;
    pos += 2 + len;
  }
  return false;
}

function exifHasGps(tiff: Buffer): boolean {
  if (tiff.length < 8) return false;
  const little = tiff.subarray(0, 2).toString('latin1') === 'II';
  const u16 = (o: number) => (little ? tiff.readUInt16LE(o) : tiff.readUInt16BE(o));
  const u32 = (o: number) => (little ? tiff.readUInt32LE(o) : tiff.readUInt32BE(o));
  const ifd = u32(4);
  if (ifd + 2 > tiff.length) return false;
  const n = u16(ifd);
  for (let i = 0; i < n; i++) {
    const o = ifd + 2 + i * 12;
    if (o + 12 <= tiff.length && u16(o) === 0x8825) return true;
  }
  return false;
}

/** A 120x80 plain PNG written next to gps.jpg by the same generator. */
export function buildPng(): Buffer {
  return readFileSync(path.join(import.meta.dirname, 'swatch.png'));
}
