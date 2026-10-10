package service

import (
	"fmt"
	"time"

	// 内嵌 IANA 时区库，运行环境缺 tzdata 时 LoadLocation 也能成功。
	_ "time/tzdata"
)

// Clock 只把绝对时刻换算成配置时区下的自然日，不解析也不改写已存的值。
type Clock struct {
	loc *time.Location
}

func NewClock(tz string) (*Clock, error) {
	// 空值必须拒绝：LoadLocation("") 静默返回 UTC，算出的自然日会整批偏一天。
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

func (c *Clock) LocalDate(t time.Time) string {
	return t.In(c.loc).Format(time.DateOnly)
}

func (c *Clock) Now() time.Time { return time.Now().In(c.loc) }
