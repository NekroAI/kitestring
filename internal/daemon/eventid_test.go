package daemon

import "testing"

// 上送事件的 ID 按生成先后严格递增，同一毫秒内也是（上送队列按 ID 排序，R-108）。
func TestEventIDMonotonic(t *testing.T) {
	prev := eventID()
	for range 10000 {
		id := eventID()
		if id <= prev {
			t.Fatalf("ID 应递增：%s 之后是 %s", prev, id)
		}
		prev = id
	}
}
