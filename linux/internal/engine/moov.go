package engine

import (
	"encoding/binary"
	"io"
	"os"
)

// walkTopLevelBoxes calls fn for each top-level MP4 box in order,
// passing its type and total size (including the header). It seeks
// past each payload by its declared size, so the usually huge mdat
// box is skipped without being read. fn returns false to stop
// early. The walk ends cleanly (nil) at EOF; a truncated header or
// an impossible size is an error.
func walkTopLevelBoxes(path string, fn func(typ string, size uint64) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var hdr [8]byte
	for {
		_, err := io.ReadFull(f, hdr[:])
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		size := binary.BigEndian.Uint32(hdr[0:4])
		typ := string(hdr[4:8])
		var total uint64
		switch size {
		case 0:
			// Box extends to end of file: report it, then stop.
			fn(typ, 0)
			return nil
		case 1:
			// 64-bit largesize follows the 8-byte header.
			var ext [8]byte
			if _, err := io.ReadFull(f, ext[:]); err != nil {
				return err
			}
			total = binary.BigEndian.Uint64(ext[:])
			if total < 16 {
				return io.ErrUnexpectedEOF
			}
			if !fn(typ, total) {
				return nil
			}
			if _, err := f.Seek(int64(total)-16, io.SeekCurrent); err != nil {
				return err
			}
		default:
			if size < 8 {
				return io.ErrUnexpectedEOF
			}
			total = uint64(size)
			if !fn(typ, total) {
				return nil
			}
			if _, err := f.Seek(int64(total)-8, io.SeekCurrent); err != nil {
				return err
			}
		}
	}
}

// hasMoov reports whether the MP4 file at path contains a moov box.
// ffmpeg writes the moov atom only when it finalizes (closes) a
// segment, so a segment that is still being recorded — or was
// orphaned when the app stopped mid-segment — has no moov, and no
// browser can play it. It returns false on any read/parse error or
// if EOF is reached before a moov box.
func hasMoov(path string) bool {
	found := false
	_ = walkTopLevelBoxes(path, func(typ string, _ uint64) bool {
		if typ == "moov" {
			found = true
			return false
		}
		return true
	})
	return found
}

// parseMP4Duration reads the clip length in seconds from the mvhd
// box inside moov (timescale/duration). It only reads the moov
// payload — the huge mdat box is skipped by seeking — so it stays
// cheap on ~100 MB segments. The second return is false when the
// file has no moov/mvhd or is unreadable (duration unknown).
func parseMP4Duration(path string) (float64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	// Walk top-level boxes, tracking offsets, until moov.
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(f, hdr[:]); err != nil {
			return 0, false
		}
		size32 := binary.BigEndian.Uint32(hdr[0:4])
		typ := string(hdr[4:8])
		var total uint64
		var headerLen int64 = 8
		switch size32 {
		case 0:
			// Box to EOF: moov here would be the rest of the
			// file; anything else ends the walk.
			if typ != "moov" {
				return 0, false
			}
			total = 0 // read to EOF below
		case 1:
			var ext [8]byte
			if _, err := io.ReadFull(f, ext[:]); err != nil {
				return 0, false
			}
			total = binary.BigEndian.Uint64(ext[:])
			headerLen = 16
			if total < 16 {
				return 0, false
			}
		default:
			if size32 < 8 {
				return 0, false
			}
			total = uint64(size32)
		}
		if typ == "moov" {
			var payloadLen int64
			if total == 0 {
				if st, err := f.Stat(); err == nil {
					payloadLen = st.Size()
					if pos, err := f.Seek(0, io.SeekCurrent); err == nil {
						payloadLen -= pos
					}
				}
			} else {
				payloadLen = int64(total) - headerLen
			}
			if payloadLen <= 0 || payloadLen > 16<<20 {
				return 0, false
			}
			payload := make([]byte, payloadLen)
			if _, err := io.ReadFull(f, payload); err != nil {
				return 0, false
			}
			return parseMVHDDuration(payload)
		}
		// Skip this box's payload.
		skip := int64(total) - headerLen
		if skip < 0 {
			return 0, false
		}
		if _, err := f.Seek(skip, io.SeekCurrent); err != nil {
			return 0, false
		}
	}
}

// parseMVHDDuration scans a moov payload for the mvhd child box
// and returns duration/timescale in seconds.
func parseMVHDDuration(moov []byte) (float64, bool) {
	off := 0
	for off+8 <= len(moov) {
		size := int(binary.BigEndian.Uint32(moov[off : off+4]))
		typ := string(moov[off+4 : off+8])
		if size < 8 || off+size > len(moov) {
			return 0, false
		}
		if typ == "mvhd" {
			p := moov[off+8 : off+size]
			if len(p) < 4 {
				return 0, false
			}
			version := p[0]
			var timescale uint32
			var duration uint64
			if version == 1 {
				if len(p) < 32 {
					return 0, false
				}
				timescale = binary.BigEndian.Uint32(p[20:24])
				duration = binary.BigEndian.Uint64(p[24:32])
			} else {
				if len(p) < 24 {
					return 0, false
				}
				timescale = binary.BigEndian.Uint32(p[12:16])
				duration = uint64(binary.BigEndian.Uint32(p[16:20]))
			}
			if timescale == 0 {
				return 0, false
			}
			return float64(duration) / float64(timescale), true
		}
		off += size
	}
	return 0, false
}
