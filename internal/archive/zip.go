package archive

import (
	"archive/zip"
	"bufio"
	"compress/flate"
	"encoding/binary"
	"errors"
	"hash"
	"hash/crc32"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
)

type zipName struct {
	name  string
	flags uint16
}

func decodeZIPNames(names []zipName) []string {
	result := make([]string, len(names))
	for i, name := range names {
		result[i] = name.name
	}
	bestScore := -1
	for _, candidate := range []struct {
		encoding encoding.Encoding
		korean   bool
	}{{korean.EUCKR, true}, {japanese.ShiftJIS, false}} {
		decoded := make([]string, len(names))
		score, valid := 0, true
		for i, name := range names {
			decoded[i] = name.name
			if name.flags&0x800 != 0 || utf8.ValidString(name.name) {
				continue
			}
			value, err := candidate.encoding.NewDecoder().String(name.name)
			if err != nil || strings.ContainsRune(value, utf8.RuneError) {
				valid = false
				break
			}
			decoded[i] = value
			if candidate.korean {
				for pos := 0; pos < len(name.name); pos++ {
					lead := name.name[pos]
					if lead < 0x80 {
						continue
					}
					if pos+1 >= len(name.name) {
						valid = false
						break
					}
					pair, _ := candidate.encoding.NewDecoder().String(name.name[pos : pos+2])
					for _, r := range pair {
						if r >= 0xAC00 && r <= 0xD7A3 {
							if lead >= 0xB0 && lead <= 0xC8 {
								score += 4
							} else {
								score++
							}
						}
					}
					pos++
				}
			} else {
				for _, r := range value {
					if r >= 0x3040 && r <= 0x30FF || r >= 0x4E00 && r <= 0x9FFF {
						score += 4
					}
				}
			}
		}
		if valid && score > bestScore {
			bestScore, result = score, decoded
		}
	}
	if bestScore < 0 {
		for i, name := range names {
			if name.flags&0x800 == 0 && !utf8.ValidString(name.name) {
				result[i] = strings.ToValidUTF8(name.name, "\uFFFD")
			}
		}
	}
	return result
}

type zipLocal struct {
	name                     string
	flags, method            uint16
	offset, compressed, size int64
	crc                      uint32
}

func (e *engine) openZIP(size int64) error {
	var err error
	e.zip, err = zip.NewReader(e, size)
	if errors.Is(err, zip.ErrInsecurePath) && e.zip != nil {
		err = nil
	}
	names := []zipName{}
	if err == nil {
		for _, f := range e.zip.File {
			names = append(names, zipName{f.Name, f.Flags})
		}
	} else {
		if !errors.Is(err, zip.ErrFormat) && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return err
		}
		e.zip = nil
		e.zipOpenError = &Error{Kind: Code(err), Err: err, message: "invalid ZIP archive"}
		e.metadataRemaining = -1
		if err = e.scanZIP(size); err != nil {
			return err
		}
		if len(e.salvaged) == 0 {
			return e.zipOpenError
		}
		e.caps.Salvaged = true
		for _, f := range e.salvaged {
			names = append(names, zipName{f.name, f.flags})
		}
	}
	e.zipNames = decodeZIPNames(names)
	return nil
}

func (e *engine) scanZIP(size int64) error {
	budget, metadata := e.budget(), int64(64<<20)
	for offset := int64(0); offset < size; {
		var header [30]byte
		if _, err := e.ReadAt(header[:], offset); err != nil {
			break
		}
		if binary.LittleEndian.Uint32(header[:4]) != 0x04034b50 {
			break
		}
		if len(e.salvaged) >= e.limits.MaxEntries {
			return limited("archive entry count")
		}
		f := zipLocal{
			flags:      binary.LittleEndian.Uint16(header[6:8]),
			method:     binary.LittleEndian.Uint16(header[8:10]),
			crc:        binary.LittleEndian.Uint32(header[14:18]),
			compressed: int64(binary.LittleEndian.Uint32(header[18:22])),
			size:       int64(binary.LittleEndian.Uint32(header[22:26])),
		}
		if f.method != zip.Store && f.method != zip.Deflate {
			break
		}
		nameSize := int64(binary.LittleEndian.Uint16(header[26:28]))
		extraSize := int64(binary.LittleEndian.Uint16(header[28:30]))
		f.offset = offset + 30 + nameSize + extraSize
		if nameSize == 0 || f.offset > size {
			break
		}
		metadata -= 30 + nameSize + extraSize
		if metadata < 0 {
			return limited("archive metadata read budget")
		}
		name := make([]byte, nameSize)
		if _, err := e.ReadAt(name, offset+30); err != nil {
			break
		}
		f.name = string(name)
		if f.compressed == 0xffffffff || f.size == 0xffffffff {
			break
		}
		if f.flags&8 == 0 {
			if f.compressed > size-f.offset {
				break
			}
			if f.method == zip.Store && f.compressed != f.size {
				break
			}
			offset = f.offset + f.compressed
		} else {
			if f.method != zip.Deflate || f.flags&1 != 0 {
				break
			}
			input := &zipInput{Reader: bufio.NewReader(io.NewSectionReader(e, f.offset, size-f.offset))}
			reader := flate.NewReader(input)
			n, err := io.Copy(io.Discard, io.LimitReader(reader, min(e.limits.MaxEntryBytes, budget)+1))
			reader.Close()
			budget -= n
			if budget < 0 {
				return limited("cumulative decompression byte budget")
			}
			if err != nil || n > e.limits.MaxEntryBytes {
				break
			}
			f.compressed, f.size = input.consumed, n
			offset = f.offset + f.compressed
			var descriptor [12]byte
			if _, err := e.ReadAt(descriptor[:4], offset); err != nil {
				break
			}
			if binary.LittleEndian.Uint32(descriptor[:4]) == 0x08074b50 {
				offset += 4
			}
			if _, err := e.ReadAt(descriptor[:], offset); err != nil {
				break
			}
			f.crc = binary.LittleEndian.Uint32(descriptor[:4])
			if int64(binary.LittleEndian.Uint32(descriptor[4:8])) != f.compressed || int64(binary.LittleEndian.Uint32(descriptor[8:12])) != f.size {
				break
			}
			offset += 12
		}
		e.salvaged = append(e.salvaged, f)
	}
	return nil
}

type zipInput struct {
	*bufio.Reader
	consumed int64
}

func (r *zipInput) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.consumed += int64(n)
	return n, err
}
func (r *zipInput) ReadByte() (byte, error) {
	b, err := r.Reader.ReadByte()
	if err == nil {
		r.consumed++
	}
	return b, err
}

func (e *engine) openLocalZIP(f zipLocal) io.ReadCloser {
	section := io.NewSectionReader(e, f.offset, f.compressed)
	var reader io.ReadCloser = io.NopCloser(section)
	if f.method == zip.Deflate {
		reader = flate.NewReader(section)
	}
	return &zipCRCReader{ReadCloser: reader, hash: crc32.NewIEEE(), crc: f.crc}
}

type zipCRCReader struct {
	io.ReadCloser
	hash hash.Hash32
	crc  uint32
}

func (r *zipCRCReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.hash.Write(p[:n])
	if err == io.EOF && r.hash.Sum32() != r.crc {
		err = zip.ErrChecksum
	}
	return n, err
}
