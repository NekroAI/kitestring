package main

import (
	"flag"
	"fmt"

	"github.com/NekroAI/kitestring/internal/proto"
)

// runProto：ks proto check <目录|.html|.zip>，交可交互原型之前在本机自检（R-197）。
func runProto(args []string) error {
	usage := fmt.Errorf("用法：ks proto check <目录|.html|.zip> [--entry 入口页面] [--max-mb 上限]（交原型用 ks req evidence / ks decision raise 的 --prototype）")
	if len(args) < 1 || args[0] != "check" {
		return usage
	}
	fs := flag.NewFlagSet("proto check", flag.ContinueOnError)
	entry := fs.String("entry", "", "入口页面在包里的路径；不写时找 index.html")
	maxMB := fs.Int("max-mb", proto.DefaultMaxMB, "原型包上限（MB），按 Hub 设置里的值检查")
	if err := parseAnywhere(fs, args[1:]); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return usage
	}
	r, err := proto.Check(fs.Arg(0), *entry, *maxMB)
	if err != nil {
		return err
	}
	fmt.Printf("可以交：入口 %s，%d 个文件，打包后 %.1f MB，解压后 %.1f MB\n", r.Entry, r.Files, float64(r.Size)/(1<<20), float64(r.Unpacked)/(1<<20))
	if len(r.External) > 0 {
		fmt.Printf("引用了 %d 个外部地址（能加载，但离线时打不开；链接在原型里点了不跳转）：\n", len(r.External))
		for _, u := range r.External {
			fmt.Println("  " + u)
		}
	}
	return nil
}
