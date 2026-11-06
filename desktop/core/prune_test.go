package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestPruneAutoBackups(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.Local)
	// 30 days x 10 hourly copies + a manual archive + an old-style AUTO_ file
	for d := 0; d < 30; d++ {
		for h := 8; h < 18; h++ {
			n := fmt.Sprintf("NOCTURNE_auto_%s_%02d.zip", now.AddDate(0, 0, -d).Format("2006-01-02"), h)
			os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600)
		}
	}
	os.WriteFile(filepath.Join(dir, "NOCTURNE_2026-11-01_17-17-08.zip"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(dir, "AUTO_2026-10-29_44cd.zip"), []byte("x"), 0o600)
	removed := pruneAutoBackups(dir, now)
	entries, _ := os.ReadDir(dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	sort.Strings(left)
	autos := 0
	for _, n := range left {
		if autoNameRe.MatchString(n) {
			autos++
		}
	}
	t.Logf("removed %d, left %d (autos %d): first %v ... last %v", len(removed), len(left), autos, left[:3], left[len(left)-3:])
	if autos != 24+12 { // 24 newest (today 10 + yesterday 10 + 4 of day-2) + one per day for days -2..-14 that are not already kept (13 days, day-2 already has copies => 12)
		t.Fatalf("unexpected number of auto copies kept: %d", autos)
	}
	for _, must := range []string{"NOCTURNE_2026-11-01_17-17-08.zip", "AUTO_2026-10-29_44cd.zip"} {
		if _, err := os.Stat(filepath.Join(dir, must)); err != nil {
			t.Fatalf("%s was removed", must)
		}
	}
}
