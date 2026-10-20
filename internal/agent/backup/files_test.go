package backup

import (
	"testing"
	"time"

	"fleetwatch/internal/proto"
)

func TestKeepSet(t *testing.T) {
	// One copy a day for 90 days, newest first, plus a second copy today.
	start := time.Date(2026, 10, 21, 3, 0, 0, 0, time.Local)
	var list []proto.BackupSnapshot
	list = append(list, proto.BackupSnapshot{ID: "today-2", Time: start.Add(time.Hour).UnixMilli()})
	for i := range 90 {
		d := start.AddDate(0, 0, -i)
		list = append(list, proto.BackupSnapshot{ID: d.Format(stampLayout), Time: d.UnixMilli()})
	}
	keep := keepSet(list, 7, 4, 3)
	if !keep["today-2"] || keep[start.Format(stampLayout)] {
		t.Fatal("the newest copy of a day is the one kept")
	}
	if !keep[start.AddDate(0, 0, -6).Format(stampLayout)] || keep[start.AddDate(0, 0, -7).Format(stampLayout)] && !isSunday(start.AddDate(0, 0, -7)) {
		t.Fatal("7 daily")
	}
	// 7 days + up to 4 weeks + 3 months, overlapping: far fewer than 90.
	if n := len(keep); n < 9 || n > 14 {
		t.Fatalf("kept %d", n)
	}
	if !keep[time.Date(2026, 8, 31, 3, 0, 0, 0, time.Local).Format(stampLayout)] {
		t.Fatal("the last copy of August is a monthly one")
	}
}

func isSunday(t time.Time) bool { return t.Weekday() == time.Sunday }
