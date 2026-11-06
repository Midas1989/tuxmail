// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Test vectors shared by webauthn.test.ts and loginjs.test.ts: the typed
// helpers and the plain-JS copy on the server-rendered login page must agree.

/** [bytes, unpadded base64url] */
export const B64_VECTORS: [number[], string][] = [
  [[], ''],
  [[0], 'AA'],
  [[0xfb, 0xff, 0xfe], '-__-'],
  [[0x00, 0x10, 0x83], 'ABCD'],
  [[1, 2, 3, 4], 'AQIDBA'],
  [[0xff, 0xff], '__8'],
  [[104, 101, 108, 108, 111], 'aGVsbG8'],
];
