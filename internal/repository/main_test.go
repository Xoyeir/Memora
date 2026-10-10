// 放在外部测试包：建库逻辑在 testsupport，内部包 import 会形成循环。
package repository_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/Xoyeir/Memora/internal/testsupport"
)

func TestMain(m *testing.M) {
	if err := testsupport.Prepare(); err != nil {
		fmt.Fprintln(os.Stderr, "准备测试库失败:", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}
