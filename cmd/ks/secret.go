package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
)

// 凭据（R-295）：会话申请凭据、在命令的环境变量里取用。没有打印明文的子命令：值只交给要运行的命令（run）或写进授权过的文件（write）。

const secretUsage = `用法：
  ks secret request <凭据名> --purpose 用途 --field '环境变量名|显示名|获取方式' [--field ...] [--plain 环境变量名 ...] [--req R-x] [--days 7] [--allow '命令 *' ...]
      申请凭据：进用户的「待处理」，用户在网页上填值并授权或拒绝后结果送到你这里。获取方式按服务写清步骤（在哪个页面、点什么、选什么权限），
      指导用户快速取到；--allow 是建议允许的命令（* 单独放最后 = 后面可跟任意参数），不写 = 不限命令；--days 0 = 长期。
      --plain 声明某个值不敏感（用户名、Account ID 这类不是密钥的值）：用户在凭据页看到明文，纸鸢各处也不会把同样的文字隐去；
      值默认敏感，用户在申请里可以改。注入命令的规则不变。
  ks secret amend <编号 S-x> --reason 理由 [--add-allow '命令 *' ...] [--remove-allow '命令' ...] [--allow '命令' ... | --any-command]
                  [--days N] [--project 项目名 ...] [--disk | --no-disk] [--replace '环境变量名|获取方式' ...] [--rename 新名称] [--req R-x]
      申请修改已有凭据的授权（生效中、已失效、已撤回都可以）：允许的命令增删改、有效期、项目、写入磁盘。取用因授权范围被拒
      （命令不在允许范围内、未授权写入磁盘、授权到期或被撤销）时用它，不要让用户去凭据页手动改。--add-allow / --remove-allow
      在原来的基础上增删（写错的命令先去掉再加上对的），--allow 整份替换，--any-command 改成不限命令；--days 从批准时算起，
      不写 = 生效中的到期时间不变；--project 是改后的完整项目列表。申请进用户的「待处理」，用户可再调整后保存并授权或拒绝，
      结果送到你这里；批准前原来的授权照常生效。申请里没有值。
      --replace 申请替换某个值（Token 到期、泄露或定期更新）：写环境变量名和新值的获取方式（| 后面，不写 = 沿用原来的），可重复；
      可以只替换值、不改授权范围。用户在抽屉里填新值，你拿不到值；批准后新值生效、旧值作废，凭据名和取用记录不变，照常 ks secret run。
      换 Token 用它，不要新建一条凭据。
      --rename 提出新名称（名称只给人看，编号不变，按编号取用不受影响）。
      允许的命令按空白切成参数逐个比对，* 在参数内匹配任意字符（含空格）：一个参数里含空格时（如 sh -c 后面的整段脚本）
      用 * 代替其中的空格，例如 'sh -c printf*wrangler*secret*put*'，不要拆成多段。
  ks secret list
      本项目能用的凭据：编号 S-x、名称、状态、字段名、允许的命令（没有值）。
  ks secret run <编号 S-x> [--req R-x] -- <命令> [参数...]
      授权有效、项目匹配、命令在允许范围内时，把凭据的值作为这条命令的环境变量运行；否则拒绝并说明原因。每次取用都有记录。
      程序按 PATH 解析成绝对路径后判定、记录：允许规则写裸程序名时只认 PATH 找到的那一个（./wrangler、/tmp/x/wrangler 不算）。
      命令继承当前环境（PATH、NODE_OPTIONS 等），记录里写实际运行的程序路径；这一层只防误用，不要借环境变量绕过。
  ks secret write <编号 S-x> <文件路径> [--field 环境变量名] [--force] [--req R-x]
      只在凭据授权了「写入磁盘」时可用：写成 环境变量名=值 的行（--field 只写这一个值的原文），文件权限 0600，路径会被记录。
每条凭据有稳定编号 S-x（申请结果、ks secret list 里都有）：run / amend / write 写编号，名称可能被改；写名称也认，但名称对上多条时会被拒绝。
不要打印、回显值，也不要再让用户在终端里输入凭据。`

func runSecret(args []string) error {
	if len(args) < 1 {
		return errors.New(secretUsage)
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "request":
		return secretRequest(args)
	case "amend":
		return secretAmend(args)
	case "list":
		return secretList(args)
	case "run":
		return secretRun(args)
	case "write":
		return secretWrite(args)
	case "help", "-h", "--help":
		fmt.Println(secretUsage)
		return nil
	}
	return fmt.Errorf("未知的 secret 子命令：%s\n%s", sub, secretUsage)
}

// secretSessionKey 认出当前会话（显式 --session 优先）。
func secretSessionKey(flagVal string) (string, error) {
	key := cliSession(flagVal)
	if key == "" {
		key = currentSession("")
	}
	if key == "" {
		return "", errors.New("认不出当前会话：在 Claude Code / Codex 会话里运行，或用 --session 指定")
	}
	return key, nil
}

// parseSecretField 解析 --field '环境变量名|显示名|获取方式'（后两段可省）。
func parseSecretField(v string) map[string]any {
	parts := strings.SplitN(v, "|", 3)
	f := map[string]any{"env": strings.TrimSpace(parts[0])}
	if len(parts) > 1 {
		f["label"] = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		f["how"] = strings.TrimSpace(parts[2])
	}
	return f
}

// secretFields 解析全部 --field，并按 --plain 标出不敏感的值；--plain 写了没有的环境变量名时报错。
func secretFields(fields, plain []string) ([]map[string]any, error) {
	fl := make([]map[string]any, len(fields))
	seen := map[string]bool{}
	for i, f := range fields {
		fl[i] = parseSecretField(f)
		seen[fl[i]["env"].(string)] = true
	}
	for _, p := range plain {
		p = strings.TrimSpace(p)
		if !seen[p] {
			return nil, fmt.Errorf("--plain %s：没有这个值，先用 --field 写上", p)
		}
		for _, f := range fl {
			if f["env"] == p {
				f["plain"] = true
			}
		}
	}
	return fl, nil
}

func secretRequest(args []string) error {
	fs := flag.NewFlagSet("secret request", flag.ContinueOnError)
	session := fs.String("session", "", "会话键（tool:session_id），Claude 下可省略")
	purpose := fs.String("purpose", "", "用途：拿它做什么（必填）")
	req := fs.String("req", "", "关联需求 R-x")
	days := fs.Int("days", 7, "建议的有效期（天），0 = 长期")
	var fields, allow, plain stringList
	fs.Var(&fields, "field", "要的值：'环境变量名|显示名|获取方式'，可重复")
	fs.Var(&plain, "plain", "不敏感的值（环境变量名，如用户名、Account ID）：明文显示、不在纸鸢里隐去，可重复")
	fs.Var(&allow, "allow", "建议允许的命令，如 'wrangler pages deploy *'，可重复；不写 = 不限命令")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 || strings.TrimSpace(*purpose) == "" || len(fields) == 0 {
		return errors.New(secretUsage)
	}
	key, err := secretSessionKey(*session)
	if err != nil {
		return err
	}
	fl, err := secretFields(fields, plain)
	if err != nil {
		return err
	}
	body := map[string]any{"session_key": key, "name": fs.Arg(0), "purpose": *purpose, "requirement": *req, "days": *days, "fields": fl}
	if len(allow) > 0 {
		body["allowed"] = []string(allow)
	}
	return agentCall("POST", "/agent/secrets/request", body)
}

func secretAmend(args []string) error {
	fs := flag.NewFlagSet("secret amend", flag.ContinueOnError)
	session := fs.String("session", "", "会话键（tool:session_id），Claude 下可省略")
	reason := fs.String("reason", "", "理由：为什么要改（必填）")
	req := fs.String("req", "", "关联需求 R-x")
	days := fs.Int("days", -1, "改后的有效期（天），从批准时算起，0 = 长期；不写 = 到期时间不变")
	anyCmd := fs.Bool("any-command", false, "改成不限命令")
	disk := fs.Bool("disk", false, "改成允许写入磁盘")
	noDisk := fs.Bool("no-disk", false, "改成不允许写入磁盘")
	var allow, add, remove, projects stringList
	fs.Var(&allow, "allow", "改后允许的命令（整份替换），可重复")
	fs.Var(&add, "add-allow", "加上的命令，可重复")
	fs.Var(&remove, "remove-allow", "去掉的命令，可重复")
	fs.Var(&projects, "project", "改后的授权项目（项目名或 ID，整份替换），可重复")
	rename := fs.String("rename", "", "新名称（编号不变）")
	var replace stringList
	fs.Var(&replace, "replace", "要替换的值：'环境变量名|新值的获取方式'（获取方式可省略），可重复")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 || strings.TrimSpace(*reason) == "" {
		return errors.New(secretUsage)
	}
	if *disk && *noDisk {
		return errors.New("--disk 和 --no-disk 只能写一个")
	}
	key, err := secretSessionKey(*session)
	if err != nil {
		return err
	}
	body := secretAmendBody(key, fs.Arg(0), *reason, *req, *days, *anyCmd, *disk, *noDisk, allow, add, remove, projects, replace)
	if r := strings.TrimSpace(*rename); r != "" {
		body["rename"] = r
	}
	return agentCall("POST", "/agent/secrets/amend", body)
}

// secretAmendBody 拼修改授权申请的请求体：没写的项不发（= 不改）。
func secretAmendBody(key, name, reason, req string, days int, anyCmd, disk, noDisk bool, allow, add, remove, projects, replace []string) map[string]any {
	body := map[string]any{"session_key": key, "name": name, "reason": reason, "requirement": req}
	if len(replace) > 0 { // 替换值（R-325）：「环境变量名|获取方式」
		rs := make([]map[string]string, 0, len(replace))
		for _, r := range replace {
			env, how, _ := strings.Cut(r, "|")
			rs = append(rs, map[string]string{"env": strings.TrimSpace(env), "how": strings.TrimSpace(how)})
		}
		body["replace"] = rs
	}
	if days >= 0 {
		body["days"] = days
	}
	if anyCmd {
		body["any_command"] = true
	}
	if len(allow) > 0 {
		body["allowed"] = allow
	}
	if len(add) > 0 {
		body["add_allowed"] = add
	}
	if len(remove) > 0 {
		body["remove_allowed"] = remove
	}
	if len(projects) > 0 {
		body["projects"] = projects
	}
	if disk {
		body["disk"] = true
	} else if noDisk {
		body["disk"] = false
	}
	return body
}

func secretList(args []string) error {
	fs := flag.NewFlagSet("secret list", flag.ContinueOnError)
	session := fs.String("session", "", "会话键")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	key, err := secretSessionKey(*session)
	if err != nil {
		return err
	}
	return agentCall("GET", "/agent/secrets?session_key="+url.QueryEscape(key), nil)
}

// resolveProgram 把命令的程序解析成绝对路径（exec.LookPath）：裸程序名按 PATH 找，写了路径的照写的算（viaPath = false）。
// 纸鸢按解析结果判定允许的命令，ks 也运行这个路径，记录里的就是实际运行的程序。
func resolveProgram(name string) (abs string, viaPath bool, err error) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", false, fmt.Errorf("找不到命令 %s：%w", name, err)
	}
	if abs, err = filepath.Abs(p); err != nil {
		return "", false, err
	}
	return abs, !strings.Contains(name, "/"), nil
}

// secretUse 向纸鸢取值；被拒时错误里写明原因。prog 是 resolveProgram 解析出的程序（write 时为空）。
func secretUse(key, name, req string, argv []string, prog string, viaPath bool, path string) (logID string, env map[string]string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out struct {
		LogID string            `json:"log_id"`
		Env   map[string]string `json:"env"`
	}
	body := map[string]any{"session_key": key, "name": name, "requirement": req}
	if path != "" {
		body["path"] = path
	} else {
		body["argv"], body["program"], body["via_path"] = argv, prog, viaPath
	}
	if err := client.New(20*time.Second).Do(ctx, "POST", "/agent/secrets/use", body, &out); err != nil {
		return "", nil, err
	}
	return out.LogID, out.Env, nil
}

// secretFinish 上报退出码（只有发起这次取用的会话能报）；失败不影响命令本身的结果。
func secretFinish(key, logID string, code int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.New(5*time.Second).Do(ctx, "POST", "/agent/secrets/uses/"+url.PathEscape(logID)+"/finish", map[string]any{"session_key": key, "exit_code": code}, nil); err != nil {
		fmt.Fprintln(os.Stderr, "ks secret：上报取用结果失败：", err)
	}
}

func secretRun(args []string) error {
	i := slices.Index(args, "--")
	if i < 0 || i == len(args)-1 {
		return errors.New(secretUsage)
	}
	argv := args[i+1:]
	fs := flag.NewFlagSet("secret run", flag.ContinueOnError)
	session := fs.String("session", "", "会话键")
	req := fs.String("req", "", "这次取用为的需求 R-x")
	if err := parseAnywhere(fs, args[:i]); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New(secretUsage)
	}
	key, err := secretSessionKey(*session)
	if err != nil {
		return err
	}
	prog, viaPath, err := resolveProgram(argv[0])
	if err != nil {
		return err
	}
	logID, env, err := secretUse(key, fs.Arg(0), *req, argv, prog, viaPath, "")
	if err != nil {
		return err
	}
	code := runWithEnv(append([]string{prog}, argv[1:]...), env) // 运行的就是纸鸢判定、记录的那个路径
	secretFinish(key, logID, code)
	os.Exit(code)
	return nil
}

// runWithEnv 运行命令，凭据的值只加进它的环境；转发中断信号，返回退出码（启动失败 127，被信号结束 128 + 信号）。
func runWithEnv(argv []string, env map[string]string) int {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "ks secret run：无法启动命令：", err)
		return 127
	}
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sig)
	go func() {
		for s := range sig {
			_ = cmd.Process.Signal(s)
		}
	}()
	err := cmd.Wait()
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	return 1
}

func secretWrite(args []string) error {
	fs := flag.NewFlagSet("secret write", flag.ContinueOnError)
	session := fs.String("session", "", "会话键")
	req := fs.String("req", "", "这次取用为的需求 R-x")
	field := fs.String("field", "", "只写这一个值的原文（环境变量名）")
	force := fs.Bool("force", false, "文件已存在时覆盖")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New(secretUsage)
	}
	path, err := filepath.Abs(fs.Arg(1))
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil && !*force {
		return fmt.Errorf("%s 已存在，确认要覆盖时加 --force", path)
	}
	key, err := secretSessionKey(*session)
	if err != nil {
		return err
	}
	logID, env, err := secretUse(key, fs.Arg(0), *req, nil, "", false, path)
	if err != nil {
		return err
	}
	var content string
	if *field != "" {
		v, ok := env[*field]
		if !ok {
			secretFinish(key, logID, 2)
			return fmt.Errorf("凭据「%s」没有 %s 这个值", fs.Arg(0), *field)
		}
		content = v
	} else {
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		for _, k := range keys {
			var q strings.Builder // 带引号、转义（不转 HTML 字符），dotenv 认
			enc := json.NewEncoder(&q)
			enc.SetEscapeHTML(false)
			_ = enc.Encode(env[k])
			fmt.Fprintf(&b, "%s=%s\n", k, strings.TrimSuffix(q.String(), "\n"))
		}
		content = b.String()
	}
	if err := writePrivate(path, content); err != nil {
		secretFinish(key, logID, 1)
		return err
	}
	secretFinish(key, logID, 0)
	fmt.Printf("已写入 %s（权限 0600）。不要打印或提交这个文件。\n", path)
	return nil
}

// writePrivate 以 0600 写文件（已存在的先收紧权限再覆盖）。
func writePrivate(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
