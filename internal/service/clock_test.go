package service

import (
	"testing"
	"time"

	"github.com/Xoyeir/Memora/internal/repository"
)

func TestNewClockRejectsInvalidTimezone(t *testing.T) {
	if _, err := NewClock("Mars/Olympus"); err == nil {
		t.Fatal("非法时区应当返回错误")
	}
	if _, err := NewClock(""); err == nil {
		t.Fatal("空时区应当返回错误")
	}
}

func TestLocalDate(t *testing.T) {
	tests := []struct {
		name string
		tz   string
		at   time.Time
		want string
	}{
		{
			name: "本地当天中间时刻",
			tz:   "Asia/Shanghai",
			at:   time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC),
			want: "2026-10-08",
		},
		{
			name: "本地跨日：UTC 还是前一天，本地已经是新一天",
			tz:   "Asia/Shanghai",
			at:   time.Date(2026, 10, 8, 16, 30, 0, 0, time.UTC),
			want: "2026-10-09",
		},
		{
			name: "本地跨日：UTC 已进入次日，本地仍是当天",
			tz:   "Asia/Shanghai",
			at:   time.Date(2026, 10, 8, 15, 30, 0, 0, time.UTC),
			want: "2026-10-08",
		},
		{
			name: "UTC 与本地日期不同：本地区间内的最早一刻",
			tz:   "Asia/Shanghai",
			at:   time.Date(2026, 10, 8, 16, 0, 0, 0, time.UTC),
			want: "2026-10-09",
		},
		{
			name: "UTC 与本地日期不同：本地区间内的最后一刻",
			tz:   "Asia/Shanghai",
			at:   time.Date(2026, 10, 9, 15, 59, 59, 0, time.UTC),
			want: "2026-10-09",
		},
		{
			name: "西半球时区把 UTC 当日算作前一天",
			tz:   "America/New_York",
			at:   time.Date(2026, 10, 8, 3, 0, 0, 0, time.UTC),
			want: "2026-10-07",
		},
		{
			name: "夏令时开始前夜：纽约仍是前一天",
			tz:   "America/New_York",
			at:   time.Date(2026, 3, 8, 4, 30, 0, 0, time.UTC),
			want: "2026-03-07",
		},
		{
			name: "夏令时开始前夜：跨过零点",
			tz:   "America/New_York",
			at:   time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC),
			want: "2026-03-08",
		},
		{
			name: "夏令时生效当天本地时钟从 2 点跳到 3 点",
			tz:   "America/New_York",
			at:   time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC),
			want: "2026-03-08",
		},
		{
			name: "夏令时结束：回拨前后仍是同一天",
			tz:   "America/New_York",
			at:   time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC),
			want: "2026-11-01",
		},
		{
			name: "夏令时结束：回拨后同一堵墙上时间的第二次出现",
			tz:   "America/New_York",
			at:   time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC),
			want: "2026-11-01",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clock, err := NewClock(tc.tz)
			if err != nil {
				t.Fatalf("加载时区 %q 失败: %v", tc.tz, err)
			}
			if got := clock.LocalDate(tc.at); got != tc.want {
				t.Fatalf("LocalDate(%s) = %s, 期望 %s", tc.at.Format(time.RFC3339), got, tc.want)
			}
		})
	}
}

func TestLocalDateIgnoresInputLocation(t *testing.T) {
	clock, err := NewClock("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}

	instant := time.Date(2026, 10, 8, 16, 30, 0, 0, time.UTC)
	asLocal := instant.In(clock.Location())
	asNewYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}

	want := clock.LocalDate(instant)
	if got := clock.LocalDate(asLocal); got != want {
		t.Fatalf("同一时刻用本地 Location 表示后归属变了: %s != %s", got, want)
	}
	if got := clock.LocalDate(instant.In(asNewYork)); got != want {
		t.Fatalf("同一时刻用纽约 Location 表示后归属变了: %s != %s", got, want)
	}
}

// 缺省时区必须与数据库写入墙上时间用的时区一致：不一致会让同一批历史数据落到别的自然日。
func TestDefaultTimezoneOffsetMatchesWrites(t *testing.T) {
	clock, err := NewClock("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	if _, offset := at.In(clock.Location()).Zone(); offset != 8*60*60 {
		t.Fatalf("缺省时区偏移是 %d 秒，与数据库写入侧的 +08:00 不一致", offset)
	}
	if clock.Location().String() != repository.StorageTimezone {
		t.Fatalf("缺省时区 %q 与存储时区 %q 不是同一个", clock.Location().String(), repository.StorageTimezone)
	}
}

// 真实时刻由存储时区固定、日期随 APP_TIMEZONE 变化；上海覆盖跨日边界，纽约覆盖夏令时切换两侧。
func TestStoredRecordDateFollowsAppTimezone(t *testing.T) {
	tests := []struct {
		name      string
		wallUTC   time.Time // 库里这串墙上时间对应的真实时刻
		wantLocal string    // APP_TIMEZONE = 存储时区（上海）
		wantNY    string    // APP_TIMEZONE = America/New_York
	}{
		{
			name:      "跨日：上海还是当天，纽约已经退回前一天",
			wallUTC:   time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC), // 上海 10-08 08:00 / 纽约 10-07 20:00
			wantLocal: "2026-10-08",
			wantNY:    "2026-10-07",
		},
		{
			name:      "夏令时开始当天：切换前两小时半",
			wallUTC:   time.Date(2026, 3, 8, 4, 30, 0, 0, time.UTC), // 纽约此时还是 EST
			wantLocal: "2026-03-08",
			wantNY:    "2026-03-07",
		},
		{
			name:      "夏令时结束当天：回拨后的同一堵墙上时间",
			wallUTC:   time.Date(2026, 10, 31, 19, 30, 0, 0, time.UTC), // 纽约此时还是 EDT
			wantLocal: "2026-11-01",
			wantNY:    "2026-10-31",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			shanghai, err := NewClock(repository.StorageTimezone)
			if err != nil {
				t.Fatal(err)
			}
			newYork, err := NewClock("America/New_York")
			if err != nil {
				t.Fatal(err)
			}

			if got := shanghai.LocalDate(tc.wallUTC); got != tc.wantLocal {
				t.Fatalf("APP_TIMEZONE=%s 时归属是 %s，期望 %s", repository.StorageTimezone, got, tc.wantLocal)
			}
			if got := newYork.LocalDate(tc.wallUTC); got != tc.wantNY {
				t.Fatalf("APP_TIMEZONE=America/New_York 时归属是 %s，期望 %s", got, tc.wantNY)
			}

			// 归属变了，但真实时刻没有变——这正是两个时区职责分开的意义。
			if !tc.wallUTC.Equal(tc.wallUTC.UTC()) {
				t.Fatal("真实时刻被改动了")
			}
		})
	}
}
