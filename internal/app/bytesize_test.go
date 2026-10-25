package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestByteSize(t *testing.T) {
	tests := []struct {
		input string
		want  ByteSize
	}{
		{`0`, 0}, {`123`, 123}, {`123.0`, 123}, {`1e3`, 1000},
		{`9223372036854775807`, 9223372036854775807},
		{`"9223372036854775807B"`, 9223372036854775807},
		{`"42"`, 42}, {`"  42 B  "`, 42}, {`"1.0"`, 1},
		{`"1KiB"`, 1 << 10}, {`"1MiB"`, 1 << 20}, {`"1GiB"`, 1 << 30}, {`"1TiB"`, 1 << 40},
		{`"1K"`, 1 << 10}, {`"1M"`, 1 << 20}, {`"1G"`, 1 << 30}, {`"1T"`, 1 << 40},
		{`"1KB"`, 1000}, {`"1MB"`, 1000000}, {`"1GB"`, 1000000000}, {`"1TB"`, 1000000000000},
		{`"1.5GiB"`, 1610612736}, {`"1.5GB"`, 1500000000}, {`"0.001KB"`, 1},
		{`" 1.5 mIb "`, 1572864}, {`"2 kb"`, 2000}, {`"2 g"`, 2 << 30},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			var got ByteSize
			if err := json.Unmarshal([]byte(tt.input), &got); err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %d, want %d", got, tt.want)
			}
			data, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var number int64
			if err := json.Unmarshal(data, &number); err != nil || number != int64(tt.want) {
				t.Fatalf("numeric marshal = %s, %v", data, err)
			}
		})
	}
}

func TestByteSizeErrors(t *testing.T) {
	tests := []struct{ input, message string }{
		{`-1`, "non-negative"}, {`"-1MiB"`, "non-negative"},
		{`1.5`, "whole bytes"}, {`"1.5B"`, "whole bytes"}, {`"0.1KiB"`, "whole bytes"},
		{`9223372036854775808`, "int64"}, {`"9223372036854775808"`, "int64"}, {`"8388608TiB"`, "int64"},
		{`"1PB"`, "unknown unit"}, {`"1KI"`, "unknown unit"}, {`"1MBps"`, "unknown unit"},
		{`""`, "non-negative"}, {`"+1"`, "non-negative"}, {`"1e3"`, "non-negative"},
		{`null`, "number or string"}, {`true`, "number or string"}, {`[]`, "number or string"}, {`{}`, "number or string"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ByteSize(7)
			err := json.Unmarshal([]byte(tt.input), &got)
			if err == nil || !strings.Contains(err.Error(), tt.message) {
				t.Fatalf("error = %v, want %q", err, tt.message)
			}
			if got != 7 {
				t.Fatal("failed parsing changed value")
			}
		})
	}
}

func TestLoadConfigByteSizes(t *testing.T) {
	for _, ext := range []string{"yaml", "json"} {
		t.Run(ext, func(t *testing.T) {
			content := "memory_bytes: 64MiB\ndisk_bytes: 1.5GB\npage_bytes: 64MiB\n"
			if ext == "json" {
				content = `{"memory_bytes":"64MiB","disk_bytes":"1.5GB","page_bytes":"64MiB"}`
			}
			path := filepath.Join(t.TempDir(), "config."+ext)
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if c.MemoryBytes != 64<<20 || c.DiskBytes != 1500000000 || c.PageBytes != 64<<20 {
				t.Fatalf("unexpected byte sizes: %d, %d, %d", c.MemoryBytes, c.DiskBytes, c.PageBytes)
			}
			if c.MaxPixels != DefaultConfig().MaxPixels {
				t.Fatal("max_pixels default changed")
			}
		})
	}
}
