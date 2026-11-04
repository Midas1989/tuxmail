// SPDX-License-Identifier: AGPL-3.0-or-later

package pdf

import (
	"bytes"
	"encoding/binary"
	"math"
	"sync"
)

var (
	iccOnce sync.Once
	iccData []byte
)

// SRGBProfile returns a minimal ICC v2 display profile for sRGB (D65 primaries
// adapted to D50, tone curve gamma 2.2), used as the PDF/A output intent.
func SRGBProfile() []byte {
	iccOnce.Do(func() { iccData = buildSRGB() })
	return iccData
}

func s15(v float64) uint32 { return uint32(int32(math.Round(v * 65536))) }

func xyzTag(x, y, z float64) []byte {
	var b bytes.Buffer
	b.WriteString("XYZ ")
	b.Write(make([]byte, 4))
	for _, v := range []float64{x, y, z} {
		binary.Write(&b, binary.BigEndian, s15(v))
	}
	return b.Bytes()
}

func curvTag(gamma float64) []byte {
	var b bytes.Buffer
	b.WriteString("curv")
	b.Write(make([]byte, 4))
	binary.Write(&b, binary.BigEndian, uint32(1))
	binary.Write(&b, binary.BigEndian, uint16(math.Round(gamma*256)))
	b.Write([]byte{0, 0}) // pad to 4 bytes
	return b.Bytes()
}

func descTag(s string) []byte {
	var b bytes.Buffer
	b.WriteString("desc")
	b.Write(make([]byte, 4))
	binary.Write(&b, binary.BigEndian, uint32(len(s)+1))
	b.WriteString(s)
	b.WriteByte(0)
	b.Write(make([]byte, 4+4)) // unicode language code + count
	b.Write(make([]byte, 2+1+67))
	for b.Len()%4 != 0 {
		b.WriteByte(0)
	}
	return b.Bytes()
}

func textTag(s string) []byte {
	var b bytes.Buffer
	b.WriteString("text")
	b.Write(make([]byte, 4))
	b.WriteString(s)
	b.WriteByte(0)
	for b.Len()%4 != 0 {
		b.WriteByte(0)
	}
	return b.Bytes()
}

func buildSRGB() []byte {
	type tag struct {
		sig  string
		data []byte
	}
	trc := curvTag(2.2)
	tags := []tag{
		{"desc", descTag("sRGB IEC61966-2.1")},
		{"cprt", textTag("No copyright, use freely")},
		{"wtpt", xyzTag(0.9642, 1.0, 0.8249)},
		{"rXYZ", xyzTag(0.4361, 0.2225, 0.0139)},
		{"gXYZ", xyzTag(0.3851, 0.7169, 0.0971)},
		{"bXYZ", xyzTag(0.1431, 0.0606, 0.7141)},
		{"rTRC", trc}, {"gTRC", trc}, {"bTRC", trc},
	}
	headerLen := 128
	tableLen := 4 + 12*len(tags)
	off := headerLen + tableLen
	var table, data bytes.Buffer
	binary.Write(&table, binary.BigEndian, uint32(len(tags)))
	// identical data (the TRCs) is shared
	shared := map[*byte]uint32{}
	for _, t := range tags {
		key := &t.data[0]
		o, ok := shared[key]
		if !ok {
			o = uint32(off + data.Len())
			shared[key] = o
			data.Write(t.data)
		}
		table.WriteString(t.sig)
		binary.Write(&table, binary.BigEndian, o)
		binary.Write(&table, binary.BigEndian, uint32(len(t.data)))
	}
	size := off + data.Len()
	var h bytes.Buffer
	binary.Write(&h, binary.BigEndian, uint32(size))
	h.WriteString("none")                                     // preferred CMM
	binary.Write(&h, binary.BigEndian, uint32(0x02100000))    // version 2.1
	h.WriteString("mntr")                                     // device class
	h.WriteString("RGB ")                                     // color space
	h.WriteString("XYZ ")                                     // PCS
	h.Write([]byte{0x07, 0xE6, 0, 1, 0, 1, 0, 0, 0, 0, 0, 0}) // date 2022-01-01
	h.WriteString("acsp")
	h.WriteString("APPL")
	h.Write(make([]byte, 4+4+4+8+4)) // flags, manufacturer, model, attributes, intent
	for _, v := range []float64{0.9642, 1.0, 0.8249} {
		binary.Write(&h, binary.BigEndian, s15(v))
	}
	h.WriteString("TUXM")
	for h.Len() < headerLen {
		h.WriteByte(0)
	}
	out := append(h.Bytes(), table.Bytes()...)
	return append(out, data.Bytes()...)
}
