package hub

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 凭据的加密（R-295）：值用 AES-256-GCM 加密后存进数据库，主密钥不进数据库。
//
// 主密钥放在 KS_HOME 下的单独文件 secret.key（与 hub.db 同目录，权限 0600），首次用到时生成。
// 没用系统钥匙串：Hub 常跑在 launchd / systemd 的后台服务里，macOS 钥匙串在无人登录的会话里会弹授权框或拿不到，
// Linux 上也没有统一的钥匙串；单独文件在两边都可靠，备份数据库（scripts/deploy.sh 只备份 hub.db）不会把主密钥带出去。
// 拿到数据库的人没有主密钥解不开值；同一台机器上能读 KS_HOME 的人两者都能读到——这一层防的是数据库外泄，不防本机。
//
// 密文格式 "v1:" + base64(nonce ‖ 密文)，附加数据是凭据 ID：一条凭据的密文挪到另一条上解不开。
// 主密钥文件丢失或被换掉后旧值解不开，取用时说明原因，到网页上替换值即可恢复。

const (
	secretKeyFile  = "secret.key"
	secretCipherV1 = "v1:"
)

// vault 管主密钥和已解密值的缓存（脱敏用）。
type vault struct {
	mu   sync.Mutex
	path string // 主密钥文件；为空时用附件目录旁的 secret.key，附件目录也没设（单测）时只在内存里
	key  []byte

	redactor redactor  // 当前全部已知值的脱敏器（redact.go），凭据有变化时重建
	once     sync.Once // 第一次有接口请求时建脱敏器（Hub 重启后旧值照样隐去）
}

// SetSecretKeyFile 指定主密钥文件（默认是附件目录旁的 secret.key，即 KS_HOME/secret.key）。
func (h *Hub) SetSecretKeyFile(path string) {
	h.vault.mu.Lock()
	defer h.vault.mu.Unlock()
	h.vault.path, h.vault.key = path, nil
}

// secretKey 读出主密钥，没有时生成。文件权限宽于 0600 时收紧。
func (h *Hub) secretKey() ([]byte, error) {
	v := h.vault
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.key != nil {
		return v.key, nil
	}
	path := v.path
	if path == "" && h.filesDir != "" {
		path = filepath.Join(filepath.Dir(h.filesDir), secretKeyFile)
	}
	if path == "" {
		v.key = make([]byte, 32)
		_, err := rand.Read(v.key)
		return v.key, err
	}
	key, err := loadSecretKey(path)
	if errors.Is(err, fs.ErrNotExist) {
		key, err = createSecretKey(path)
	}
	if err != nil {
		return nil, err
	}
	v.key = key
	return key, nil
}

func loadSecretKey(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("收紧主密钥文件权限失败：%w", err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("主密钥文件 %s 格式不对", path)
	}
	return key, nil
}

func createSecretKey(path string) ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	// O_EXCL：两个请求同时首次生成时，后到的读先写好的那份
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return loadSecretKey(path)
	}
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(base64.StdEncoding.EncodeToString(key) + "\n"); err != nil {
		f.Close()
		return nil, err
	}
	return key, f.Close()
}

func (h *Hub) secretAEAD() (cipher.AEAD, error) {
	key, err := h.secretKey()
	if err != nil {
		return nil, fmt.Errorf("读取凭据主密钥失败：%w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealSecret 把一条凭据的全部值（环境变量名 → 值）加密成密文。
func (h *Hub) sealSecret(id string, vals map[string]string) (string, error) {
	aead, err := h.secretAEAD()
	if err != nil {
		return "", err
	}
	plain, _ := json.Marshal(vals)
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := aead.Seal(nonce, nonce, plain, []byte(id))
	return secretCipherV1 + base64.StdEncoding.EncodeToString(out), nil
}

// errSecretUnreadable：密文解不开（主密钥丢失或被换掉、数据被改）。
var errSecretUnreadable = errors.New("凭据的值无法解密：主密钥文件缺失或已更换，到网页上替换值后可恢复")

// openSecret 解密一条凭据的值；没填过值时返回空表。
func (h *Hub) openSecret(id, sealed string) (map[string]string, error) {
	if sealed == "" {
		return map[string]string{}, nil
	}
	raw, ok := strings.CutPrefix(sealed, secretCipherV1)
	if !ok {
		return nil, errSecretUnreadable
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, errSecretUnreadable
	}
	aead, err := h.secretAEAD()
	if err != nil {
		return nil, err
	}
	if len(data) < aead.NonceSize() {
		return nil, errSecretUnreadable
	}
	plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte(id))
	if err != nil {
		return nil, errSecretUnreadable
	}
	vals := map[string]string{}
	if err := json.Unmarshal(plain, &vals); err != nil {
		return nil, errSecretUnreadable
	}
	return vals, nil
}

// refreshRedactor 重新解密全部凭据，重建脱敏器。凭据新建、替换值、授权申请后调用；解不开的跳过并记日志。
func (h *Hub) refreshRedactor(ctx context.Context) {
	list, err := h.store.ListSecrets(ctx)
	if err != nil {
		h.log.Warn("读取凭据失败，脱敏器未更新", "err", err)
		return
	}
	var known []string
	for _, x := range list {
		vals, err := h.openSecret(x.ID, x.Cipher)
		if err != nil {
			h.log.Warn("凭据的值无法解密，不参与脱敏", "secret", x.Name, "err", err)
			continue
		}
		for _, v := range sensitiveValues(x, vals) { // 不敏感的值（R-318）不参与
			known = append(known, v)
		}
	}
	r := newRedactor(known)
	h.vault.mu.Lock()
	h.vault.redactor = r
	h.vault.mu.Unlock()
}

// currentRedactor 是当前的脱敏器；没有已知值时为空（不脱敏）。第一次用到时先从库里建好。
func (h *Hub) currentRedactor() redactor {
	if h.store != nil {
		h.vault.once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			h.refreshRedactor(ctx)
		})
	}
	h.vault.mu.Lock()
	defer h.vault.mu.Unlock()
	return h.vault.redactor
}
