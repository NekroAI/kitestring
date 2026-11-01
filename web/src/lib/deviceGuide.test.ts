import { test } from 'node:test'
import assert from 'node:assert/strict'
import { defaultPlatform, dshCmd, installCmd, isLoopback, loginCmd, toolsCmd, TOKEN_PLACEHOLDER } from './deviceGuide.ts'

test('安装命令按平台写对 shell 配置文件，并在当前终端加上 PATH', () => {
  assert.match(installCmd('mac'), /install\.sh \| sh\n/)
  assert.match(installCmd('mac'), />> ~\/\.zshrc/)
  assert.match(installCmd('linux'), />> ~\/\.bashrc/)
  assert.match(installCmd('linux'), /\nexport PATH="\$HOME\/\.local\/bin:\$PATH"$/)
})

test('登录命令带完整令牌；重开指引没有令牌时写占位', () => {
  assert.equal(loginCmd('https://ks.example.com', 'kst_abc'), 'ks login --hub https://ks.example.com --token kst_abc')
  assert.equal(loginCmd('https://ks.example.com', null), `ks login --hub https://ks.example.com --token ${TOKEN_PLACEHOLDER}`)
})

test('ks install 按勾选的工具关掉没用的，DSH 不影响它', () => {
  assert.equal(toolsCmd(['claude', 'codex']), 'ks install')
  assert.equal(toolsCmd(['claude']), 'ks install --codex=false')
  assert.equal(toolsCmd(['codex', 'dsh']), 'ks install --claude=false')
  assert.equal(toolsCmd(['dsh']), 'ks install --claude=false --codex=false')
})

test('DSH 插件取与本机 ks 同版本的源码', () => {
  assert.match(dshCmd(), /--branch "v\$v"/)
  assert.match(dshCmd(), /install-dsh-plugin\.sh --profile web$/)
})

test('回环地址要提示换成局域网地址', () => {
  for (const h of ['localhost', '127.0.0.1', '[::1]', 'app.localhost', '0.0.0.0']) assert.equal(isLoopback(h), true, h)
  for (const h of ['ks.example.com', '192.168.1.5', 'host.docker.internal']) assert.equal(isLoopback(h), false, h)
})

test('默认平台跟浏览器所在系统', () => {
  assert.equal(defaultPlatform('Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)'), 'mac')
  assert.equal(defaultPlatform('Mozilla/5.0 (X11; Linux x86_64)'), 'linux')
  assert.equal(defaultPlatform('Mozilla/5.0 (Windows NT 10.0; Win64; x64)'), 'linux')
})
