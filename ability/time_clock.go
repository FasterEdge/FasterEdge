// FasterEdge 开源项目 - Github: https://github.com/FasterEdge - Gitee: https://gitee.com/FasterEdge
package ability

import "time"

type timeClock interface {
	Now() time.Time
	Monotonic() time.Duration
}
type systemTimeClock struct{ origin time.Time }

func newSystemTimeClock() *systemTimeClock          { return &systemTimeClock{origin: time.Now()} }
func (c *systemTimeClock) Now() time.Time           { return time.Now() }
func (c *systemTimeClock) Monotonic() time.Duration { return time.Since(c.origin) }
