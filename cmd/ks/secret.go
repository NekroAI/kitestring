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
  ks secret request <凭据名> --purpose 用途 --field '环境变量名|显示名|获取方式' [--field ...] [--req R-x] [--days 7] [--allow '命令 *' ...]
      申请凭据：进用户的「待处理」，用户在网页上填值并授权或拒绝后结果送到你这里。获取方式按服务写清步骤（在哪个页面、点什么、选什么权限），
      指导用户快速取到；--allow 是建议允许的命令（* 单独放最后 = 后面可跟任意参数），不写 = 不限命令；--days 0 = 长期。
  ks secret list
      本项目能用的凭据：名字、状态、字段名、允许的命令（没有值）。
  ks secret run <凭据名> [--req R-x] -- <命令> [参数...]
      授权有效、项目匹配、命令在允许范围内时，把凭据的值作为这条命令的环境变量运行；否则拒绝并说明原因。每次取用都有记录。
  ks secret write <凭据名> <文件路径> [--field 环境变量名] [--force] [--req R-x]
      只在凭据授权了「写入磁盘」时可用：写成 环境变量名=值 的行（--field 只写这一个值的原文），文件权限 0600，路径会被记录。
不要打印、回显值，也不要再让用户在终端里输入凭据。`

func runSecret(args []string) error {
	if len(args) < 1 {
		return errors.New(secretUsage)
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "request":
		return secretRequest(args)
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
func parseSecretField(v string) map[string]string {
	parts := strings.SplitN(v, "|", 3)
	f := map[string]string{"env": strings.TrimSpace(parts[0])}
	if len(parts) > 1 {
		f["label"] = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		f["how"] = strings.TrimSpace(parts[2])
	}
	return f
}

func secretRequest(args []string) error {
	fs := flag.NewFlagSet("secret request", flag.ContinueOnError)
	session := fs.String("session", "", "会话键（tool:session_id），Claude 下可省略")
	purpose := fs.String("purpose", "", "用途：拿它做什么（必填）")
	req := fs.String("req", "", "关联需求 R-x")
	days := fs.Int("days", 7, "建议的有效期（天），0 = 长期")
	var fields, allow stringList
	fs.Var(&fields, "field", "要的值：'环境变量名|显示名|获取方式'，可重复")
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
	fl := make([]map[string]string, len(fields))
	for i, f := range fields {
		fl[i] = parseSecretField(f)
	}
	body := map[string]any{"session_key": key, "name": fs.Arg(0), "purpose": *purpose, "requirement": *req, "days": *days, "fields": fl}
	if len(allow) > 0 {
		body["allowed"] = []string(allow)
	}
	return agentCall("POST", "/agent/secrets/request", body)
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

// secretUse 向纸鸢取值；被拒时错误里写明原因。
func secretUse(key, name, req string, argv []string, path string) (logID string, env map[string]string, err error) {
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
		body["argv"] = argv
	}
	if err := client.New(20*time.Second).Do(ctx, "POST", "/agent/secrets/use", body, &out); err != nil {
		return "", nil, err
	}
	return out.LogID, out.Env, nil
}

// secretFinish 上报退出码；失败不影响命令本身的结果。
func secretFinish(logID string, code int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.New(5*time.Second).Do(ctx, "POST", "/agent/secrets/uses/"+url.PathEscape(logID)+"/finish", map[string]any{"exit_code": code}, nil); err != nil {
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
	logID, env, err := secretUse(key, fs.Arg(0), *req, argv, "")
	if err != nil {
		return err
	}
	code := runWithEnv(argv, env)
	secretFinish(logID, code)
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
	logID, env, err := secretUse(key, fs.Arg(0), *req, nil, path)
	if err != nil {
		return err
	}
	var content string
	if *field != "" {
		v, ok := env[*field]
		if !ok {
			secretFinish(logID, 2)
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
		secretFinish(logID, 1)
		return err
	}
	secretFinish(logID, 0)
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
