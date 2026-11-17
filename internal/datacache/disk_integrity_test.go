package datacache

import (
	"os"
	"testing"
	"time"
)

func TestDisk_ATruncatedPayloadIsDropped(t *testing.T) {
	// A cut file still parses as a header and a payload; only a check of the
	// payload against what was written tells it from a whole one.
	d := &disk{dir: t.TempDir()}
	h := header{Version: formatVersion, Key: "k", Written: time.Now()}
	if err := d.write(h, []byte("a payload long enough to cut")); err != nil {
		t.Fatal(err)
	}
	path := d.path("k")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, info.Size()-5); err != nil {
		t.Fatal(err)
	}
	records, err := d.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Errorf("load returned %d records, want the truncated one dropped", len(records))
	}
}

func TestDisk_AWholePayloadSurvivesLoad(t *testing.T) {
	d := &disk{dir: t.TempDir()}
	for _, payload := range []string{"", "x", "two\nlines"} {
		h := header{Version: formatVersion, Key: "k" + payload, Written: time.Now()}
		if err := d.write(h, []byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	records, err := d.load()
	if err != nil || len(records) != 3 {
		t.Fatalf("load = %d records, %v; want 3", len(records), err)
	}
}

func TestDisk_AFileWithoutAChecksumIsStillRead(t *testing.T) {
	// Files written before the checksum existed must stay readable.
	d := &disk{dir: t.TempDir()}
	line := `{"v":1,"key":"k","written":"2026-01-01T00:00:00Z"}` + "\npayload"
	if err := os.WriteFile(d.path("k"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	records, err := d.load()
	if err != nil || len(records) != 1 || string(records[0].payload) != "payload" {
		t.Fatalf("load = %v, %v", records, err)
	}
}
