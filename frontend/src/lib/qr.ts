// SPDX-License-Identifier: AGPL-3.0-or-later
//
// QR code rendering for the messenger pairing screens (qrcode-generator,
// MIT). Returns an SVG path of the dark modules so the component renders
// it without innerHTML.

import qrcode from 'qrcode-generator';

export interface QrMatrix {
  /** Modules per side (without quiet zone). */
  size: number;
  /** SVG path ("M x y h1 v1 h-1 z" per dark module), quiet zone included. */
  path: string;
  /** Modules per side including the quiet zone. */
  viewBox: number;
}

/** Encodes data (byte mode, error correction M) as QR code. */
export function qrMatrix(data: string, quiet = 4): QrMatrix {
  const qr = qrcode(0, 'M');
  qr.addData(data, 'Byte');
  qr.make();
  const n = qr.getModuleCount();
  const parts: string[] = [];
  for (let r = 0; r < n; r++) {
    for (let c = 0; c < n; c++) {
      if (qr.isDark(r, c)) parts.push(`M${c + quiet} ${r + quiet}h1v1h-1z`);
    }
  }
  return { size: n, path: parts.join(''), viewBox: n + 2 * quiet };
}
