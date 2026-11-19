package install

import (
	"slices"
	"testing"
)

// R-353：ks install --hub --hub-files-dir 把附件目录写进 hub 服务的启动参数
func TestHubArgv(t *testing.T) {
	o := Options{KS: "/bin/ks", HubAddr: "0.0.0.0:7310"}
	if got := hubArgv(o); !slices.Equal(got, []string{"/bin/ks", "hub", "serve", "--addr", "0.0.0.0:7310"}) {
		t.Fatalf("默认：%v", got)
	}
	o.HubFilesDir = "/Volumes/DataSSD/ks files"
	if got := hubArgv(o); !slices.Equal(got, []string{"/bin/ks", "hub", "serve", "--addr", "0.0.0.0:7310", "--files-dir", "/Volumes/DataSSD/ks files"}) {
		t.Fatalf("指定附件目录：%v", got)
	}
}
