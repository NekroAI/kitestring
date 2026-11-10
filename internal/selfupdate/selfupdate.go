// Package selfupdate 实现 ks update：从 GitHub Release 取最新版，校验 SHA256SUMS 后原子替换当前 ks，
// 再重启用这个 ks 的常驻服务（R-80）。发版产物的命名与 scripts/install.sh、.github/workflows/release.yml 一致：
// kitestring-<版本>-<darwin|linux>-<arm64|amd64>.tar.gz（包内 ./ks）、kitestring-<版本>-windows-<arm64|amd64>.zip（包内 ks.exe）
// 与 SHA256SUMS.txt。
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultBase 与 DefaultRepo 是下载源；KS_UPDATE_BASE 可覆盖 DefaultBase（试跑 / 镜像用）。
const (
	DefaultBase = "https://github.com"
	DefaultRepo = "NekroAI/kitestring"
	sumsName    = "SHA256SUMS.txt"
	maxDownload = 256 << 20
)

// Options 控制一次更新。
type Options struct {
	Base    string // 默认 $KS_UPDATE_BASE，再默认 DefaultBase
	Repo    string // 默认 DefaultRepo
	Current string // 当前版本（version.Version）
	Exe     string // 要替换的 ks 绝对路径（已解析软链接）
	GOOS    string
	GOARCH  string
	Check   bool // 只查不装
	Force   bool // 开发构建也换成发布版
	Client  *http.Client
	Out     io.Writer
	// Restart 在替换成功后重启常驻服务；为空则不重启
	Restart func(exe string) error
}

// Run 执行 ks update。
func Run(ctx context.Context, o Options) error {
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.Base == "" {
		o.Base = os.Getenv("KS_UPDATE_BASE")
	}
	if o.Base == "" {
		o.Base = DefaultBase
	}
	o.Base = strings.TrimRight(o.Base, "/")
	if o.Repo == "" {
		o.Repo = DefaultRepo
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 5 * time.Minute}
	}
	if o.GOOS != "darwin" && o.GOOS != "linux" && o.GOOS != "windows" {
		return fmt.Errorf("暂不支持 %s，发布版只有 macOS、Linux 与 Windows", o.GOOS)
	}
	if o.GOOS == "windows" {
		RemoveStale(o.Exe)
	}
	if o.GOARCH != "arm64" && o.GOARCH != "amd64" {
		return fmt.Errorf("暂不支持 %s 架构，发布版只有 arm64 与 amd64", o.GOARCH)
	}

	tag, err := latestTag(ctx, o)
	if err != nil {
		return err
	}
	latest := strings.TrimPrefix(tag, "v")
	cur, curOK := parseVersion(o.Current)
	lat, latOK := parseVersion(latest)
	if !latOK {
		return fmt.Errorf("最新发布版的版本号 %s 无法识别", tag)
	}
	switch {
	case !curOK && !o.Force:
		return fmt.Errorf("当前是开发构建（%s），不自动更新。要换成发布版 %s，运行 ks update --force；之后再用 scripts/deploy.sh 部署会换回源码构建的版本", o.Current, latest)
	case curOK && compare(cur, lat) == 0:
		fmt.Fprintf(o.Out, "已是最新版本 %s，无需更新\n", latest)
		return nil
	case curOK && compare(cur, lat) > 0:
		fmt.Fprintf(o.Out, "当前版本 %s 比最新发布版 %s 新，无需更新\n", o.Current, latest)
		return nil
	}
	if o.Check {
		fmt.Fprintf(o.Out, "有新版本 %s（当前 %s），运行 ks update 安装\n", latest, o.Current)
		return nil
	}

	// 先在 ks 所在目录建临时文件：没有写权限时下载前就报错；同目录才能原子改名。
	dir := filepath.Dir(o.Exe)
	pattern := ".ks.update-*"
	if o.GOOS == "windows" {
		pattern += ".exe" // Windows 按后缀认可执行文件，下面要先跑一次
	}
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("没有写入 %s 的权限，无法替换 %s。用安装 ks 时的用户运行，或用 install.sh 装到自己能写的目录（KS_INSTALL_DIR）", dir, o.Exe)
		}
		return fmt.Errorf("无法在 %s 建临时文件：%v", dir, err)
	}
	tmpPath := tmp.Name()
	replaced := false
	defer func() {
		_ = tmp.Close()
		if !replaced {
			_ = os.Remove(tmpPath)
		}
	}()

	name := fmt.Sprintf("kitestring-%s-%s-%s.tar.gz", latest, o.GOOS, o.GOARCH)
	if o.GOOS == "windows" {
		name = fmt.Sprintf("kitestring-%s-%s-%s.zip", latest, o.GOOS, o.GOARCH)
	}
	dl := fmt.Sprintf("%s/%s/releases/download/%s/", o.Base, o.Repo, tag)
	fmt.Fprintf(o.Out, "下载 ks %s（%s-%s）…\n", latest, o.GOOS, o.GOARCH)
	sums, err := fetch(ctx, o, dl+sumsName)
	if err != nil {
		return err
	}
	want, err := lookupSum(sums, name)
	if err != nil {
		return err
	}
	pkg, err := fetch(ctx, o, dl+name)
	if err != nil {
		return err
	}
	got := sha256.Sum256(pkg)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("%s 校验失败，文件可能不完整或被篡改；当前版本未改动", name)
	}
	bin, err := extractKS(pkg, o.GOOS)
	if err != nil {
		return fmt.Errorf("%s：%v；当前版本未改动", name, err)
	}
	if _, err := tmp.Write(bin); err != nil {
		return fmt.Errorf("写入临时文件失败：%v；当前版本未改动", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("写入临时文件失败：%v；当前版本未改动", err)
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return fmt.Errorf("设置执行权限失败：%v；当前版本未改动", err)
	}
	// 换上去之前先跑一次，确认新文件能在本机运行、版本对得上。
	out, err := exec.CommandContext(ctx, tmpPath, "version").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(out), "ks "+latest+" ") {
		return fmt.Errorf("下载的 ks 无法在本机运行或版本不符（%s）；当前版本未改动", strings.TrimSpace(string(out)))
	}
	if err := replaceExe(tmpPath, o.Exe, o.GOOS); err != nil {
		return fmt.Errorf("替换 %s 失败：%v；当前版本未改动", o.Exe, err)
	}
	replaced = true
	fmt.Fprintf(o.Out, "✓ 已更新到 ks %s（原 %s）→ %s\n", latest, o.Current, o.Exe)
	if o.Restart != nil {
		if err := o.Restart(o.Exe); err != nil {
			return fmt.Errorf("%v。ks 已是新版本，请手动重启常驻服务（macOS：launchctl kickstart -k gui/$(id -u)/ai.nekro.kitestring.daemon；Linux：systemctl --user restart kitestring-daemon；Windows：schtasks /end /tn Kitestring\\daemon 后 schtasks /run /tn Kitestring\\daemon；Hub 所在设备把 daemon 换成 hub 再执行一次）", err)
		}
	}
	return nil
}

// latestTag 从 releases/latest 的跳转地址里取标签，不走 API，避免匿名请求被限流（与 install.sh 一致）。
func latestTag(ctx context.Context, o Options) (string, error) {
	u := fmt.Sprintf("%s/%s/releases/latest", o.Base, o.Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u, nil)
	if err != nil {
		return "", err
	}
	c := *o.Client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		return "", fmt.Errorf("查询最新版本失败，连不上 %s：%v。检查网络或代理后重试", o.Base, err)
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode < 300 || resp.StatusCode >= 400 || loc == "" {
		return "", fmt.Errorf("查询最新版本失败：%s 返回 HTTP %d", u, resp.StatusCode)
	}
	tag := path.Base(strings.TrimRight(loc, "/"))
	if !strings.HasPrefix(tag, "v") || !strings.Contains(loc, "/releases/tag/") {
		return "", fmt.Errorf("还没有发布版本（%s）", loc)
	}
	return tag, nil
}

func fetch(ctx context.Context, o Options, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := o.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载 %s 失败：%v。检查网络或代理后重试；当前版本未改动", path.Base(u), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("下载 %s 失败：HTTP %d；当前版本未改动", path.Base(u), resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return nil, fmt.Errorf("下载 %s 中断：%v。检查网络后重试；当前版本未改动", path.Base(u), err)
	}
	if len(b) > maxDownload {
		return nil, fmt.Errorf("%s 超过 %d MB，不像是 ks 的发布包", path.Base(u), maxDownload>>20)
	}
	return b, nil
}

// lookupSum 在 sha256sum 格式（「<hash>  <文件名>」，二进制模式文件名前带 *）里找 name 的校验值。
func lookupSum(sums []byte, name string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name && len(f[0]) == 64 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("%s 里没有 %s；当前版本未改动", sumsName, name)
}

// replaceExe 把新文件换到 exe 的位置。macOS / Linux 直接原子改名；
// Windows 不能覆盖正在运行的程序（常驻服务、本命令自己都在用它），但可以给它改名：
// 先把旧的挪成 ks.exe.old-<时间>，再把新的改名过去，失败时挪回原处。旧文件见 RemoveStale。
func replaceExe(tmpPath, exe, goos string) error {
	if goos != "windows" {
		return os.Rename(tmpPath, exe)
	}
	old := fmt.Sprintf("%s.old-%d", exe, time.Now().UnixNano())
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, exe); err != nil {
		_ = os.Rename(old, exe)
		return err
	}
	_ = os.Remove(old) // 没有进程在用时当场删掉；还在用时留到下次
	return nil
}

// RemoveStale 清掉以前更新留下的 ks.exe.old-*（那时还有进程在用，删不掉）。
// ks update 开始时清一次；Windows 的常驻服务启动时也清（此时换下来的旧进程已经退出）。
func RemoveStale(exe string) {
	olds, _ := filepath.Glob(exe + ".old-*")
	for _, f := range olds {
		_ = os.Remove(f)
	}
}

// extractKS 从发布包里取出 ks（tar.gz 包内路径为 ./ks，Windows 的 zip 包内为 ks.exe）。
func extractKS(pkg []byte, goos string) ([]byte, error) {
	if goos == "windows" {
		zr, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
		if err != nil {
			return nil, fmt.Errorf("不是有效的 zip：%v", err)
		}
		for _, f := range zr.File {
			if path.Clean(f.Name) == "ks.exe" && !f.FileInfo().IsDir() {
				rc, err := f.Open()
				if err != nil {
					return nil, fmt.Errorf("解包失败：%v", err)
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, maxDownload))
			}
		}
		return nil, errors.New("包里没有 ks.exe")
	}
	gz, err := gzip.NewReader(bytes.NewReader(pkg))
	if err != nil {
		return nil, fmt.Errorf("不是有效的 tar.gz：%v", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil, errors.New("包里没有 ks")
		}
		if err != nil {
			return nil, fmt.Errorf("解包失败：%v", err)
		}
		if h.Typeflag == tar.TypeReg && path.Clean(h.Name) == "ks" {
			return io.ReadAll(io.LimitReader(tr, maxDownload))
		}
	}
}

var verRe = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

// parseVersion 只认正式版本号 X.Y.Z；git describe 生成的 0.2.0-3-gabc、dev、提交号都算开发构建。
func parseVersion(s string) ([3]int, bool) {
	m := verRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return [3]int{}, false
	}
	var v [3]int
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1])
	}
	return v, true
}

func compare(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}
