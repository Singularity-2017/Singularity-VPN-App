package appicon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
)

// Icon_group_id is the resource id of the application icon. Wails loads
// the window's icon from id 3, and Explorer shows the first icon group, so
// the one group the executables carry has this id.
const Icon_group_id = 3

// sizes are the images the application icon carries: the small ones
// Explorer and the taskbar ask for at each scaling, and 256 for large views.
var sizes = []int{16, 20, 24, 32, 40, 48, 64, 256}

// Setup_manifest is the application manifest the setup carries. Windows
// takes an executable named like a setup for an installer, and one without
// a manifest naming the Windows versions it supports gets the Program
// Compatibility Assistant's "did this program install correctly?" after it
// exits. asInvoker: the setup asks for elevation itself, once.
const Setup_manifest = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">
    <security>
      <requestedPrivileges>
        <requestedExecutionLevel level="asInvoker" uiAccess="false"/>
      </requestedPrivileges>
    </security>
  </trustInfo>
  <compatibility xmlns="urn:schemas-microsoft-com:compatibility.v1">
    <application>
      <supportedOS Id="{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}"/>
      <supportedOS Id="{1f676c76-80e1-4239-95bb-83d0f6d0da78}"/>
      <supportedOS Id="{4a2f28e3-53b9-4441-ba9c-d69d4a4a6e38}"/>
      <supportedOS Id="{35138b9a-5d96-4fbd-8e2d-a2440225f93a}"/>
    </application>
  </compatibility>
</assembly>
`

// Syso is a COFF object for amd64 holding, as a resource section, the
// application icon when icon is set, the version Explorer shows when one is
// given, and the manifest when one is given. Placed beside a main package as
// *_windows_amd64.syso, the Go linker links it in.
func Syso(icon bool, manifest string, version *Version_info) ([]byte, error) {
	type resource struct {
		kind, id uint32
		data     []byte
	}
	const rt_icon, rt_group_icon, rt_version = 3, 14, 16

	var resources []resource
	var group bytes.Buffer
	gw := func(v any) { _ = binary.Write(&group, binary.LittleEndian, v) }
	gw(uint16(0))          // reserved
	gw(uint16(1))          // type: icon
	gw(uint16(len(sizes))) // count
	for i, size := range sizes {
		if !icon {
			break
		}
		img := Draw(size, Look_up, true)
		var data []byte
		if size >= 256 {
			var encoded bytes.Buffer
			_ = png.Encode(&encoded, img)
			data = encoded.Bytes()
		} else {
			data = dib(img)
		}
		id := uint32(i + 1)
		resources = append(resources, resource{rt_icon, id, data})
		dimension := byte(size)
		if size >= 256 {
			dimension = 0 // 0 stands for 256
		}
		group.WriteByte(dimension)
		group.WriteByte(dimension)
		group.WriteByte(0) // no palette
		group.WriteByte(0) // reserved
		gw(uint16(1))      // planes
		gw(uint16(32))     // bits per pixel
		gw(uint32(len(data)))
		gw(uint16(id))
	}
	if icon {
		resources = append(resources, resource{rt_group_icon, Icon_group_id, group.Bytes()})
	}
	if version != nil {
		data, err := version_resource(*version)
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource{rt_version, 1, data})
	}
	if manifest != "" {
		const rt_manifest, create_process_manifest = 24, 1
		resources = append(resources, resource{rt_manifest, create_process_manifest, []byte(manifest)})
	}

	// The resource tree: type, then id, then language, each a directory
	// of one level, and a data entry per resource. Types are sorted
	// ascending, as the loader's binary search expects; RT_ICON (3), then
	// RT_GROUP_ICON (14), RT_VERSION (16), then RT_MANIFEST (24), the ids
	// within a type ascending.
	type kind_group struct {
		kind  uint32
		items []resource
	}
	var kinds []kind_group
	for _, r := range resources {
		if len(kinds) == 0 || kinds[len(kinds)-1].kind != r.kind {
			kinds = append(kinds, kind_group{kind: r.kind})
		}
		kinds[len(kinds)-1].items = append(kinds[len(kinds)-1].items, r)
	}

	const dir_size, entry_size, data_entry_size = 16, 8, 16
	// Lay out: root, type directories, language directories, data entries,
	// then the data itself.
	offset := dir_size + entry_size*len(kinds)
	type_dir := make([]int, len(kinds))
	for i, k := range kinds {
		type_dir[i] = offset
		offset += dir_size + entry_size*len(k.items)
	}
	lang_dir := make([]int, len(resources))
	for i := range resources {
		lang_dir[i] = offset
		offset += dir_size + entry_size
	}
	data_entry := make([]int, len(resources))
	for i := range resources {
		data_entry[i] = offset
		offset += data_entry_size
	}
	data_at := make([]int, len(resources))
	for i, r := range resources {
		offset = (offset + 7) &^ 7
		data_at[i] = offset
		offset += len(r.data)
	}
	section := make([]byte, (offset+7)&^7)

	put32 := func(at int, v uint32) { binary.LittleEndian.PutUint32(section[at:], v) }
	put16 := func(at int, v uint16) { binary.LittleEndian.PutUint16(section[at:], v) }
	directory := func(at, ids int) { put16(at+14, uint16(ids)) }
	const subdirectory = 0x80000000

	directory(0, len(kinds))
	index := 0
	for i, k := range kinds {
		put32(dir_size+entry_size*i, k.kind)
		put32(dir_size+entry_size*i+4, uint32(type_dir[i])|subdirectory)
		directory(type_dir[i], len(k.items))
		for j, r := range k.items {
			put32(type_dir[i]+dir_size+entry_size*j, r.id)
			put32(type_dir[i]+dir_size+entry_size*j+4, uint32(lang_dir[index])|subdirectory)
			directory(lang_dir[index], 1)
			put32(lang_dir[index]+dir_size, 0x0409) // en-US
			put32(lang_dir[index]+dir_size+4, uint32(data_entry[index]))
			// OffsetToData is an address in the image; it is written as
			// the offset in the section and relocated against it.
			put32(data_entry[index], uint32(data_at[index]))
			put32(data_entry[index]+4, uint32(len(r.data)))
			copy(section[data_at[index]:], r.data)
			index++
		}
	}

	// COFF: header, one section header, the section, its relocations, the
	// symbol table with the section's symbol, and an empty string table.
	const header_size, section_header_size, relocation_size, symbol_size = 20, 40, 10, 18
	raw_at := header_size + section_header_size
	relocations_at := raw_at + len(section)
	symbols_at := relocations_at + relocation_size*len(resources)

	var out bytes.Buffer
	w := func(v any) { _ = binary.Write(&out, binary.LittleEndian, v) }
	w(uint16(0x8664)) // machine: amd64
	w(uint16(1))      // sections
	w(uint32(0))      // timestamp
	w(uint32(symbols_at))
	w(uint32(1)) // symbols
	w(uint16(0)) // no optional header
	w(uint16(0)) // characteristics

	out.WriteString(".rsrc\x00\x00\x00")
	w(uint32(0)) // virtual size
	w(uint32(0)) // virtual address
	w(uint32(len(section)))
	w(uint32(raw_at))
	w(uint32(relocations_at))
	w(uint32(0)) // line numbers
	w(uint16(len(resources)))
	w(uint16(0))
	w(uint32(0x40000040)) // initialised data, readable

	out.Write(section)
	for i := range resources {
		w(uint32(data_entry[i])) // where the address is
		w(uint32(0))             // symbol 0, the section
		w(uint16(3))             // IMAGE_REL_AMD64_ADDR32NB: an image-relative address
	}

	out.WriteString(".rsrc\x00\x00\x00")
	w(uint32(0)) // value
	w(uint16(1)) // section number
	w(uint16(0)) // type
	out.WriteByte(3) // storage class: static
	out.WriteByte(0) // no auxiliary records
	w(uint32(4))     // string table: only its own size
	return out.Bytes(), nil
}

// dib is an icon image in the classic form: a BITMAPINFOHEADER of twice
// the height, the 32-bit pixels bottom-up, then the one-bit AND mask, which
// the alpha channel makes redundant and so is left clear.
func dib(img *image.NRGBA) []byte {
	size := img.Bounds().Dx()
	var out bytes.Buffer
	w := func(v any) { _ = binary.Write(&out, binary.LittleEndian, v) }
	w(uint32(40))
	w(int32(size))
	w(int32(size * 2))
	w(uint16(1))
	w(uint16(32))
	w(uint32(0)) // BI_RGB
	w(uint32(0))
	w(int32(0))
	w(int32(0))
	w(uint32(0))
	w(uint32(0))
	for y := size - 1; y >= 0; y-- {
		for x := 0; x < size; x++ {
			c := img.NRGBAAt(x, y)
			out.Write([]byte{c.B, c.G, c.R, c.A})
		}
	}
	mask_row := ((size + 31) / 32) * 4
	out.Write(make([]byte, mask_row*size))
	return out.Bytes()
}
