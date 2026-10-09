package service

import (
	"fmt"
	"time"

	// 内嵌 IANA 时区数据库，避免在缺少 tzdata 的运行环境里 LoadLocation 失败。
	_ "time/tzdata"
)

// Clock 把绝对时刻换算成配置时区下的自然日。
//
// 它只负责「属于哪一天」，不参与历史时间的解析，也不修改任何已存储的值。
type Clock struct {
	loc *time.Location
}

func NewClock(tz string) (*Clock, error) {
	// time.LoadLocation("") 会静默返回 UTC，而数据库写的是 +08:00 的墙上时间，
	// 那样算出来的自然日会整批偏一天，所以空值必须显式拒绝。
	if tz == "" {
		return nil, fmt.Errorf("时区不能为空")
	}

	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("加载时区 %q 失败: %w", tz, err)
	}
	return &Clock{loc: loc}, nil
}

func (c *Clock) Location() *time.Location { return c.loc }

// LocalDate 返回 t 在配置时区下所属的自然日，格式 2006-01-02。
//
// 采用该时区的自然日边界（00:00 切日），没有额外的凌晨偏移规则。
func (c *Clock) LocalDate(t time.Time) string {
	return t.In(c.loc).Format(time.DateOnly)
}

func (c *Clock) Now() time.Time { return time.Now().In(c.loc) }
