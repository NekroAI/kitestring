package protocol

import "testing"

func TestDevicePaths(t *testing.T) {
	for p, want := range map[string]bool{"/usr/bin/gh": true, `C:\x\gh.exe`: true, "d:/x/gh.exe": true, `\\srv\share\x`: true, "gh": false, `.\gh`: false, "./gh": false, "1:/x": false} {
		if IsAbsPath(p) != want {
			t.Errorf("IsAbsPath(%q) 应为 %v", p, want)
		}
	}
	for p, want := range map[string]string{"/a/b/proj": "proj", "/a/b/proj/": "proj", `D:\Projects\kite`: "kite", `C:\`: "C:", "proj": "proj"} {
		if got := PathBase(p); got != want {
			t.Errorf("PathBase(%q) = %q，应为 %q", p, got, want)
		}
	}
}
